package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// askFixture is a daemon HTTP surface over the command fixture's thread t1. The
// fixture's thread has the peer mailbox on, as every new thread does.
type askFixture struct {
	eng  *domain.Engine
	disp *Dispatcher
	url  string
}

func newAskFixture(t *testing.T) askFixture {
	t.Helper()
	eng, disp := newCommandFixture(t)
	srv := httptest.NewServer(NewServer(disp, eng, disp.runner, "", toolbox.New()).Handler())
	t.Cleanup(srv.Close)
	return askFixture{eng: eng, disp: disp, url: srv.URL}
}

// askReply is what an ask call got back: its status and decoded body.
type askReply struct {
	status int
	body   map[string]any
	text   string
	err    error
}

// postAsk sends a `tool ask` call in the background, because the call blocks
// until the question closes. Its reply arrives on the returned channel.
func (f askFixture) postAsk(ctx context.Context, threadID, agent string, fields map[string]any) <-chan askReply {
	out := make(chan askReply, 1)
	body := map[string]any{"threadId": threadID, "agentId": agent, "kind": "ask"}
	for k, v := range fields {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url+"/tool/call", bytes.NewReader(raw))
		if err != nil {
			out <- askReply{err: err}
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			out <- askReply{err: err}
			return
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		reply := askReply{status: resp.StatusCode, text: strings.TrimSpace(string(data))}
		if resp.StatusCode == http.StatusOK {
			_ = json.Unmarshal(data, &reply.body)
		}
		out <- reply
	}()
	return out
}

// awaitQuestionEvent waits for a question event on a thread and returns it. The
// deadline only bounds a failing run; a passing run returns on the event.
func awaitQuestionEvent(t *testing.T, events <-chan contracts.Event, typ contracts.EventType, match func(contracts.Event) bool) contracts.Event {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type() == typ && (match == nil || match(ev)) {
				return ev
			}
		case <-deadline:
			t.Fatalf("no %s event", typ)
		}
	}
}

// askedID subscribes to a thread, starts an ask and returns the question id once
// the question is recorded.
func (f askFixture) askAndAwait(t *testing.T, ctx context.Context, threadID string, fields map[string]any) (string, <-chan askReply) {
	t.Helper()
	events, unsub := f.eng.Subscribe(contracts.ThreadID(threadID))
	t.Cleanup(unsub)
	reply := f.postAsk(ctx, threadID, "agent-hub", fields)
	ev := awaitQuestionEvent(t, events, contracts.EvtQuestionAsked, nil)
	return ev.Payload.(contracts.QuestionAsked).QuestionID, reply
}

func answerRPC(disp *Dispatcher, threadID, questionID string, selected []string, text string) error {
	params, _ := json.Marshal(map[string]any{"threadId": threadID, "questionId": questionID, "selected": selected, "text": text})
	_, err := disp.Handle(context.Background(), Request{Method: "question.answer", Params: params})
	return err
}

func dismissRPC(disp *Dispatcher, threadID, questionID string) error {
	params, _ := json.Marshal(map[string]any{"threadId": threadID, "questionId": questionID})
	_, err := disp.Handle(context.Background(), Request{Method: "question.dismiss", Params: params})
	return err
}

func mustReply(t *testing.T, ch <-chan askReply) askReply {
	t.Helper()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("ask call: %v", r.err)
		}
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("ask call did not return")
	}
	return askReply{}
}

func questionOf(t *testing.T, eng *domain.Engine, threadID, id string) domain.Question {
	t.Helper()
	st, err := eng.State(context.Background(), contracts.ThreadID(threadID))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range st.Questions {
		if q.QuestionID == id {
			return q
		}
	}
	t.Fatalf("question %s not in state", id)
	return domain.Question{}
}

// The call stays open until the human answers, then returns the answer. The
// answer is in the log before the call wakes, and an answer is taken only once.
func TestAskHoldsTheCallUntilAnswered(t *testing.T) {
	f := newAskFixture(t)
	ctx := t.Context()
	id, reply := f.askAndAwait(t, ctx, "t1", map[string]any{
		"questionKind": "choice", "text": "Which colour?", "options": []string{"red", "blue"},
	})

	if err := answerRPC(f.disp, "t1", id, []string{"blue"}, ""); err != nil {
		t.Fatalf("answer: %v", err)
	}
	r := mustReply(t, reply)
	if r.status != http.StatusOK || r.body["answered"] != true {
		t.Fatalf("reply = %d %+v, want answered", r.status, r.body)
	}
	if got := r.body["selected"]; !equalAny(got, []any{"blue"}) {
		t.Fatalf("selected = %v, want [blue]", got)
	}
	if q := questionOf(t, f.eng, "t1", id); q.Status != domain.QuestionAnswered {
		t.Fatalf("status = %q, want answered", q.Status)
	}
	if err := answerRPC(f.disp, "t1", id, []string{"red"}, ""); err == nil {
		t.Fatal("a second answer to the same question must be refused")
	}
}

// An answer that is not one of the options is refused and the question stays
// open, so the caller is still waiting for a valid one.
func TestInvalidAnswerLeavesTheQuestionOpen(t *testing.T) {
	f := newAskFixture(t)
	ctx := t.Context()
	id, reply := f.askAndAwait(t, ctx, "t1", map[string]any{
		"questionKind": "choice", "text": "Which?", "options": []string{"a", "b"},
	})

	if err := answerRPC(f.disp, "t1", id, []string{"zzz"}, ""); err == nil {
		t.Fatal("an option the question never offered must be refused")
	}
	if q := questionOf(t, f.eng, "t1", id); q.Status != domain.QuestionPending {
		t.Fatalf("status after a refused answer = %q, want pending", q.Status)
	}
	if err := answerRPC(f.disp, "t1", id, []string{"a"}, ""); err != nil {
		t.Fatalf("valid answer: %v", err)
	}
	if r := mustReply(t, reply); r.body["answered"] != true {
		t.Fatalf("reply = %+v, want answered", r.body)
	}
}

// Dismissal is an answer the agent can act on: the call returns, unanswered,
// with the decline reason.
func TestDismissReturnsDeclined(t *testing.T) {
	f := newAskFixture(t)
	ctx := t.Context()
	id, reply := f.askAndAwait(t, ctx, "t1", map[string]any{
		"questionKind": "multi", "text": "Which ones?", "options": []string{"a", "b"},
	})

	if err := dismissRPC(f.disp, "t1", id); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	r := mustReply(t, reply)
	if r.status != http.StatusOK || r.body["answered"] != false || r.body["reason"] != contracts.ReasonQuestionDeclined {
		t.Fatalf("reply = %d %+v, want declined", r.status, r.body)
	}
}

// When the caller goes away (its turn stopped, or its command was killed), the
// question closes rather than staying pending for nobody.
func TestAskerGoneClosesTheQuestion(t *testing.T) {
	f := newAskFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	events, unsub := f.eng.Subscribe("t1")
	t.Cleanup(unsub)
	reply := f.postAsk(ctx, "t1", "agent-hub", map[string]any{"questionKind": "text", "text": "Anything?"})
	asked := awaitQuestionEvent(t, events, contracts.EvtQuestionAsked, nil)
	id := asked.Payload.(contracts.QuestionAsked).QuestionID

	cancel()
	closed := awaitQuestionEvent(t, events, contracts.EvtQuestionClosed, nil)
	if got := closed.Payload.(contracts.QuestionClosed); got.Answered || got.Reason != "asker gone" {
		t.Fatalf("closed = %+v, want asker gone", got)
	}
	if q := questionOf(t, f.eng, "t1", id); q.Status != domain.QuestionClosedStatus {
		t.Fatalf("status = %q, want closed", q.Status)
	}
	if r := <-reply; r.err == nil {
		t.Fatal("the cancelled caller should see its request fail, not an answer")
	}
}

// A question still open when its turn ends is closed with the turn: nothing is
// left waiting for an answer that cannot arrive.
func TestTurnEndClosesOpenQuestions(t *testing.T) {
	f := newAskFixture(t)
	ctx := t.Context()
	_, release, err := f.disp.beginTurn(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	id, reply := f.askAndAwait(t, ctx, "t1", map[string]any{"questionKind": "text", "text": "Still there?"})

	release()
	r := mustReply(t, reply)
	if r.body["answered"] != false || r.body["reason"] != "turn ended" {
		t.Fatalf("reply = %+v, want turn ended", r.body)
	}
	if q := questionOf(t, f.eng, "t1", id); q.Status != domain.QuestionClosedStatus || q.Reason != "turn ended" {
		t.Fatalf("question = %+v, want closed by the turn end", q)
	}
}

// An unanswered question times out on the server, and the caller learns why.
// The test waits on the closing event, so it passes as soon as the timeout fires.
func TestAskTimesOut(t *testing.T) {
	f := newAskFixture(t)
	events, unsub := f.eng.Subscribe("t1")
	t.Cleanup(unsub)
	reply := f.postAsk(t.Context(), "t1", "agent-hub", map[string]any{"questionKind": "text", "text": "Hurry?", "timeoutSec": 1})
	awaitQuestionEvent(t, events, contracts.EvtQuestionAsked, nil)
	awaitQuestionEvent(t, events, contracts.EvtQuestionClosed, nil)

	r := mustReply(t, reply)
	if r.body["answered"] != false || r.body["reason"] != "timed out" {
		t.Fatalf("reply = %+v, want timed out", r.body)
	}
}

// Two threads each hold a pending question. An answer names the thread it is for,
// so one thread's answer can never close or wake the other's question.
func TestQuestionsStayWithTheirThread(t *testing.T) {
	f := newAskFixture(t)
	ctx := t.Context()
	if err := f.eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2"}); err != nil {
		t.Fatal(err)
	}
	id1, reply1 := f.askAndAwait(t, ctx, "t1", map[string]any{"questionKind": "text", "text": "One?"})
	id2, reply2 := f.askAndAwait(t, ctx, "t2", map[string]any{"questionKind": "text", "text": "Two?"})

	if err := answerRPC(f.disp, "t1", id2, nil, "wrong thread"); err == nil {
		t.Fatal("t2's question must not be answerable through t1")
	}
	if q := questionOf(t, f.eng, "t2", id2); q.Status != domain.QuestionPending {
		t.Fatalf("t2 question = %q after a foreign answer, want pending", q.Status)
	}

	if err := answerRPC(f.disp, "t2", id2, nil, "two"); err != nil {
		t.Fatalf("answer t2: %v", err)
	}
	if r := mustReply(t, reply2); r.body["text"] != "two" {
		t.Fatalf("t2 reply = %+v, want its own answer", r.body)
	}
	if q := questionOf(t, f.eng, "t1", id1); q.Status != domain.QuestionPending {
		t.Fatalf("t1 question = %q after answering t2, want pending", q.Status)
	}

	if err := dismissRPC(f.disp, "t1", id1); err != nil {
		t.Fatalf("dismiss t1: %v", err)
	}
	if r := mustReply(t, reply1); r.body["reason"] != contracts.ReasonQuestionDeclined {
		t.Fatalf("t1 reply = %+v, want declined", r.body)
	}
}

// A subagent's question reaches the orchestrator as a peer message once it is
// answered, when the asker names no one.
func TestSubagentAnswerReachesTheOrchestrator(t *testing.T) {
	f := newAskFixture(t)
	// The daemon keeps the runtime mailbox on; the fixture starts with it off.
	f.disp.runner.Mailbox().SetEnabled(true)
	id, reply := f.askAndAwait(t, t.Context(), "t1", map[string]any{"questionKind": "text", "text": "Which db?"})
	if err := answerRPC(f.disp, "t1", id, nil, "postgres"); err != nil {
		t.Fatal(err)
	}
	mustReply(t, reply)

	st, err := f.eng.State(t.Context(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range st.Mailbox {
		if m.From == "user" && m.To == "agent-orchestrator" && strings.Contains(m.Body, "postgres") {
			return
		}
	}
	t.Fatalf("no answer message to the orchestrator in mailbox %+v", st.Mailbox)
}

// A question left pending by a previous daemon run has no caller. The restart
// sweep closes it, and the log records why.
func TestRestartClosesQuestionsLeftPending(t *testing.T) {
	ctx := context.Background()
	store := domain.NewMemoryStore()

	first := domain.NewEngine(store, nil)
	first.Start(ctx)
	if err := first.Dispatch(ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := first.Dispatch(ctx, contracts.Command{
		ID: "q1", Type: contracts.CmdQuestionAsk, ThreadID: "t1",
		QuestionAsk: &contracts.QuestionAsked{QuestionID: "q_old", Kind: contracts.QuestionText, Question: "Still?"},
	}); err != nil {
		t.Fatal(err)
	}
	first.Close()

	second := domain.NewEngine(store, nil)
	second.Start(ctx)
	t.Cleanup(second.Close)
	disp := NewDispatcher(DispatcherDeps{Engine: second})
	if err := disp.CloseStaleQuestions(ctx); err != nil {
		t.Fatal(err)
	}
	if q := questionOf(t, second, "t1", "q_old"); q.Status != domain.QuestionClosedStatus || q.Reason != "daemon restarted" {
		t.Fatalf("question after restart = %+v, want closed by restart", q)
	}
	if err := answerRPC(disp, "t1", "q_old", nil, "late"); err == nil {
		t.Fatal("an answer after restart must be refused")
	}
}

// Bad requests are refused before the caller is held.
func TestAskRefusesBadRequests(t *testing.T) {
	f := newAskFixture(t)
	ctx := t.Context()
	for name, fields := range map[string]map[string]any{
		"unknown kind":    {"questionKind": "dropdown", "text": "?"},
		"choice no opts":  {"questionKind": "choice", "text": "?"},
		"over the limit":  {"questionKind": "text", "text": "?", "timeoutSec": 3601},
		"empty question":  {"questionKind": "text", "text": "  "},
		"text with a set": {"questionKind": "text", "text": "?", "options": []string{"a", "b"}},
	} {
		r := mustReply(t, f.postAsk(ctx, "t1", "agent-hub", fields))
		if r.status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, r.status, r.text)
		}
	}
}

// equalAny compares a decoded JSON array against an expected string slice.
func equalAny(got any, want []any) bool {
	list, ok := got.([]any)
	if !ok || len(list) != len(want) {
		return false
	}
	for i := range want {
		if list[i] != want[i] {
			return false
		}
	}
	return true
}
