package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
)

// callMasterRPC sends one request and decodes its reply into out.
func callMasterRPC(t *testing.T, d *Dispatcher, id, method string, params any, out any) {
	t.Helper()
	raw, err := d.Handle(context.Background(), Request{ID: id, Method: method, Params: mustJSONParams(t, params)})
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal %s reply: %v", method, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("decode %s reply: %v", method, err)
	}
}

func mustJSONParams(t *testing.T, params any) json.RawMessage {
	t.Helper()
	if params == nil {
		return nil
	}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return data
}

func threadList(t *testing.T, d *Dispatcher) []ThreadSummary {
	t.Helper()
	raw, err := d.Handle(context.Background(), Request{ID: "list", Method: "thread.list"})
	if err != nil {
		t.Fatalf("thread.list: %v", err)
	}
	data, _ := json.Marshal(raw)
	var out []ThreadSummary
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode thread.list: %v", err)
	}
	return out
}

// A master conversation is durable: send records it, list and open read it
// back, a follow-up reads its history from the thread rather than the client,
// and delete removes it. Master conversations stay out of thread.list.
func TestMasterHistoryLifecycle(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	_, disp := newMasterRPCFixture(t, masterDispatchScript(promptFile, "", "All quiet today."))

	var created MasterThreadDetail
	callMasterRPC(t, disp, "1", "master.history.new", map[string]any{"title": "Home chat"}, &created)
	if created.ID == "" || created.Title != "Home chat" {
		t.Fatalf("created = %+v", created)
	}

	var rows []MasterThreadSummary
	callMasterRPC(t, disp, "2", "master.history.list", nil, &rows)
	if len(rows) != 1 || rows[0].ID != created.ID || rows[0].Title != "Home chat" || rows[0].Messages != 0 {
		t.Fatalf("list = %+v", rows)
	}
	if summaries := threadList(t, disp); len(summaries) != 0 {
		t.Fatalf("thread.list = %+v, want master conversations excluded", summaries)
	}

	var first orchestrator.MasterResult
	callMasterRPC(t, disp, "3", "master.send", map[string]any{"text": "anything running?", "threadId": created.ID}, &first)
	if first.MasterThreadID != created.ID {
		t.Fatalf("MasterThreadID = %q, want %q", first.MasterThreadID, created.ID)
	}
	if first.Reply != "All quiet today." {
		t.Fatalf("Reply = %q", first.Reply)
	}

	var detail MasterThreadDetail
	callMasterRPC(t, disp, "4", "master.history.open", map[string]any{"threadId": created.ID}, &detail)
	if len(detail.Messages) != 2 {
		t.Fatalf("messages = %+v, want the user message and the master reply", detail.Messages)
	}
	if detail.Messages[0].Role != "user" || detail.Messages[0].Text != "anything running?" {
		t.Fatalf("first message = %+v", detail.Messages[0])
	}
	if detail.Messages[1].Role != "assistant" || detail.Messages[1].Text != "All quiet today." {
		t.Fatalf("second message = %+v", detail.Messages[1])
	}

	// The second send passes no client history: the prior exchange must reach
	// the master's prompt from the thread itself. The prompt file holds the
	// prompt of the latest master dispatch.
	os.Remove(promptFile)
	var second orchestrator.MasterResult
	callMasterRPC(t, disp, "5", "master.send", map[string]any{"text": "and now?", "threadId": created.ID}, &second)
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("read master prompt: %v", err)
	}
	for _, want := range []string{"anything running?", "All quiet today.", "and now?"} {
		if !strings.Contains(string(prompt), want) {
			t.Fatalf("second master prompt missing %q:\n%s", want, prompt)
		}
	}

	// The transcript now holds both exchanges, oldest first.
	callMasterRPC(t, disp, "6", "master.history.open", map[string]any{"threadId": created.ID}, &detail)
	if len(detail.Messages) != 4 {
		t.Fatalf("messages after two sends = %+v", detail.Messages)
	}
	if detail.Messages[3].Role != "assistant" || detail.Messages[3].Text != "All quiet today." {
		t.Fatalf("newest message = %+v", detail.Messages[3])
	}

	// The list reflects the message counts.
	callMasterRPC(t, disp, "7", "master.history.list", nil, &rows)
	if len(rows) != 1 || rows[0].Messages != 4 {
		t.Fatalf("list after two sends = %+v", rows)
	}

	// A second conversation lists newest first.
	var other MasterThreadDetail
	callMasterRPC(t, disp, "8", "master.history.new", map[string]any{"title": "Fresh chat"}, &other)
	callMasterRPC(t, disp, "9", "master.history.list", nil, &rows)
	if len(rows) != 2 || rows[0].ID != other.ID {
		t.Fatalf("list = %+v, want the newest conversation first", rows)
	}

	// Neither conversation leaked into the work-thread list.
	if summaries := threadList(t, disp); len(summaries) != 0 {
		t.Fatalf("thread.list = %+v, want master conversations excluded", summaries)
	}
}

// Sending to a thread that is not a master conversation, or that does not
// exist, is refused rather than silently recorded.
func TestMasterSendRejectsNonMasterThreads(t *testing.T) {
	eng, disp := newMasterRPCFixture(t, "true")

	params, _ := json.Marshal(map[string]any{"projectId": "p1", "title": "Work"})
	out, err := disp.Handle(context.Background(), Request{ID: "1", Method: "thread.create", Params: params})
	if err != nil {
		t.Fatalf("thread.create: %v", err)
	}
	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	data, _ := json.Marshal(out)
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatal(err)
	}
	sendParams, _ := json.Marshal(map[string]any{"text": "hi", "threadId": created.ThreadID})
	if _, err := disp.Handle(context.Background(), Request{ID: "2", Method: "master.send", Params: sendParams}); err == nil ||
		!strings.Contains(err.Error(), "not a master conversation") {
		t.Fatalf("err = %v, want not a master conversation", err)
	}

	sendParams, _ = json.Marshal(map[string]any{"text": "hi", "threadId": "missing"})
	if _, err := disp.Handle(context.Background(), Request{ID: "3", Method: "master.send", Params: sendParams}); err == nil ||
		!strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want does not exist", err)
	}

	// The refused send recorded nothing on the project thread.
	st, _ := eng.State(context.Background(), created.ThreadID)
	if len(st.Messages) != 0 {
		t.Fatalf("messages = %+v, want none", st.Messages)
	}
}

// Deleting a master conversation removes it from the list and tombstones the
// thread; deleting it twice is refused.
func TestMasterHistoryDelete(t *testing.T) {
	eng, disp := newMasterRPCFixture(t, "true")

	var created MasterThreadDetail
	callMasterRPC(t, disp, "1", "master.history.new", nil, &created)

	var ok map[string]bool
	callMasterRPC(t, disp, "2", "master.history.delete", map[string]any{"threadId": created.ID}, &ok)
	if !ok["ok"] {
		t.Fatalf("delete reply = %+v", ok)
	}
	st, _ := eng.State(context.Background(), created.ID)
	if st.Exists {
		t.Fatal("master thread still exists after delete")
	}
	var rows []MasterThreadSummary
	callMasterRPC(t, disp, "3", "master.history.list", nil, &rows)
	if rows == nil || len(rows) != 0 {
		t.Fatalf("list after delete = %+v, want an empty array", rows)
	}

	if _, err := disp.Handle(context.Background(), Request{ID: "4", Method: "master.history.delete", Params: mustJSONParams(t, map[string]any{"threadId": created.ID})}); err == nil ||
		!strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want does not exist on double delete", err)
	}
}

// master.runtime.get/set round-trip the override and its effective runtime,
// validate against the models service, and clear back to the default.
func TestMasterRuntimeRPC(t *testing.T) {
	_, disp := newMasterRPCFixture(t, "true")
	disp.settings.AddModel(contracts.HarnessOpenCode, "model-x")

	var view MasterRuntimeView
	callMasterRPC(t, disp, "1", "master.runtime.get", nil, &view)
	if view.Harness != "" || view.Model != "" {
		t.Fatalf("view = %+v, want no override", view)
	}
	if view.Effective.Harness != string(contracts.HarnessOpenCode) {
		t.Fatalf("effective harness = %q, want the role default", view.Effective.Harness)
	}

	callMasterRPC(t, disp, "2", "master.runtime.set", map[string]any{"harness": "opencode", "model": "model-x"}, &view)
	if view.Harness != "opencode" || view.Model != "model-x" {
		t.Fatalf("view after set = %+v", view)
	}
	if view.Effective.Model != "model-x" {
		t.Fatalf("effective model = %q, want model-x", view.Effective.Model)
	}

	// A fresh decode: the override is visible to a later get, and a refused
	// set never changed it.
	var again MasterRuntimeView
	callMasterRPC(t, disp, "3", "master.runtime.get", nil, &again)
	if again.Harness != "opencode" || again.Model != "model-x" {
		t.Fatalf("get after set = %+v", again)
	}

	if _, err := disp.Handle(context.Background(), Request{ID: "4", Method: "master.runtime.set", Params: mustJSONParams(t, map[string]any{"harness": "nope"})}); err == nil ||
		!strings.Contains(err.Error(), "unknown harness") {
		t.Fatalf("err = %v, want unknown harness", err)
	}
	if _, err := disp.Handle(context.Background(), Request{ID: "5", Method: "master.runtime.set", Params: mustJSONParams(t, map[string]any{"model": "not-a-model"})}); err == nil ||
		!strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v, want model not available", err)
	}
	if _, err := disp.Handle(context.Background(), Request{ID: "6", Method: "master.runtime.set", Params: mustJSONParams(t, map[string]any{})}); err == nil ||
		!strings.Contains(err.Error(), "required") {
		t.Fatalf("err = %v, want required", err)
	}
	var after MasterRuntimeView
	callMasterRPC(t, disp, "7", "master.runtime.get", nil, &after)
	if after.Model != "model-x" {
		t.Fatalf("get after refused sets = %+v", after)
	}

	// An empty string clears the field back to inheritance.
	callMasterRPC(t, disp, "8", "master.runtime.set", map[string]any{"model": ""}, &view)
	if view.Model != "" || view.Effective.Model != "" {
		t.Fatalf("view after clear = %+v", view)
	}
}

// master.reporter.get/set round-trip the persisted flag.
func TestMasterReporterRPC(t *testing.T) {
	_, disp := newMasterRPCFixture(t, "true")

	var state ControlState
	callMasterRPC(t, disp, "1", "master.reporter.get", nil, &state)
	if state.Enabled {
		t.Fatal("reporter should be off by default")
	}

	callMasterRPC(t, disp, "2", "master.reporter.set", map[string]any{"enabled": true}, &state)
	if !state.Enabled || !disp.settings.MasterReporter() {
		t.Fatalf("state = %+v, settings = %v", state, disp.settings.MasterReporter())
	}

	if _, err := disp.Handle(context.Background(), Request{ID: "3", Method: "master.reporter.set", Params: mustJSONParams(t, map[string]any{})}); err == nil ||
		!strings.Contains(err.Error(), "enabled is required") {
		t.Fatalf("err = %v, want enabled is required", err)
	}
}

// A master reply that was only a rejected action block still records a
// durable exchange: with no prose at all the transcript would otherwise be
// empty text, which the decider refuses — and a failed proposal must not
// break the conversation.
func TestMasterHistoryRecordsRejectedActions(t *testing.T) {
	reply := "```json\n" + `{"type":"stop_thread","threadId":"missing"}` + "\n```"
	_, disp := newMasterRPCFixture(t, masterDispatchScript("/dev/null", "", reply))

	var created MasterThreadDetail
	callMasterRPC(t, disp, "1", "master.history.new", nil, &created)

	var res orchestrator.MasterResult
	callMasterRPC(t, disp, "2", "master.send", map[string]any{"text": "stop it", "threadId": created.ID}, &res)
	if res.Error == "" || res.Reply != "" {
		t.Fatalf("result = %+v, want a rejected proposal and no prose", res)
	}

	var detail MasterThreadDetail
	callMasterRPC(t, disp, "3", "master.history.open", map[string]any{"threadId": created.ID}, &detail)
	if len(detail.Messages) != 2 {
		t.Fatalf("messages = %+v, want the exchange recorded", detail.Messages)
	}
	if !strings.Contains(detail.Messages[1].Text, "No action ran") {
		t.Fatalf("recorded reply = %q, want the rejection recorded", detail.Messages[1].Text)
	}
}

// A master reply that ran actions records what ran, so reopening the
// conversation shows the outcome and not only the prose.
func TestMasterHistoryRecordsExecutedActions(t *testing.T) {
	dir := t.TempDir()
	releaseFile := filepath.Join(dir, "release")
	t.Cleanup(func() { _ = os.WriteFile(releaseFile, nil, 0o644) })
	reply := "On it.\n\n```json\n" +
		`{"type":"start_thread","projectId":"p1","title":"Started work","prompt":"do the work"}` +
		"\n```"
	_, disp := newMasterRPCFixture(t, masterDispatchScript(filepath.Join(dir, "prompt.txt"), releaseFile, reply))

	var created MasterThreadDetail
	callMasterRPC(t, disp, "1", "master.history.new", nil, &created)

	var res orchestrator.MasterResult
	callMasterRPC(t, disp, "2", "master.send", map[string]any{"text": "start the work", "threadId": created.ID}, &res)
	if res.ThreadID == "" {
		t.Fatalf("result = %+v, want a dispatched thread", res)
	}

	var detail MasterThreadDetail
	callMasterRPC(t, disp, "3", "master.history.open", map[string]any{"threadId": created.ID}, &detail)
	if len(detail.Messages) != 2 {
		t.Fatalf("messages = %+v", detail.Messages)
	}
	text := detail.Messages[1].Text
	if !strings.Contains(text, "On it.") || !strings.Contains(text, "start_thread") || !strings.Contains(text, string(res.ThreadID)) {
		t.Fatalf("recorded reply = %q, want the prose plus the executed action", text)
	}
}

// No agent-running RPC operates on a master conversation: it has no worktree
// and no roster, and its transcript is chat history, not work output. This is
// the work-surface mirror of masterThreadState's refusal.
func TestWorkRPCsRefuseMasterConversation(t *testing.T) {
	eng, disp := newMasterRPCFixture(t, "true")

	var created MasterThreadDetail
	callMasterRPC(t, disp, "1", "master.history.new", map[string]any{"title": "Home"}, &created)

	cases := []struct {
		method string
		params map[string]any
	}{
		{"thread.turn.start", map[string]any{"threadId": created.ID, "text": "work"}},
		{"thread.turn.start", map[string]any{"threadId": created.ID, "text": "work", "detach": true}},
		{"thread.council.start", map[string]any{"threadId": created.ID, "topic": "ship it"}},
		{"thread.steer", map[string]any{"threadId": created.ID, "text": "steer"}},
		{"thread.btw", map[string]any{"threadId": created.ID, "question": "why"}},
	}
	for i, tc := range cases {
		raw, err := disp.Handle(context.Background(), Request{
			ID: fmt.Sprintf("w%d", i), Method: tc.method, Params: mustJSONParams(t, tc.params),
		})
		if err == nil || !strings.Contains(err.Error(), "master conversation") {
			t.Fatalf("%s = %v, %v; want the master-conversation refusal", tc.method, raw, err)
		}
	}

	// A master conversation is not a branch source either.
	forkParams := mustJSONParams(t, map[string]any{"threadId": created.ID, "messageId": "m1"})
	if _, err := disp.Handle(context.Background(), Request{ID: "f", Method: "thread.fork", Params: forkParams}); err == nil ||
		!strings.Contains(err.Error(), "master conversation") {
		t.Fatalf("fork err = %v, want the master-conversation refusal", err)
	}

	// No agent ran and nothing was recorded: the conversation is untouched.
	st := waitThreadState(t, eng, created.ID, func(s *domain.ThreadState) bool {
		return len(s.Messages) == 0 && s.Turn == 0
	})
	if len(st.Agents) != 0 {
		t.Fatalf("agents = %d, want none", len(st.Agents))
	}
}

// masterParkingScript parks the master's model call on a release file before
// it answers, so a send is observably in flight and only finishes when the
// test releases it. Non-master sessions answer at once.
func masterParkingScript(promptFile, releaseFile, reply string) string {
	frame, _ := json.Marshal(map[string]string{"type": "text", "text": reply})
	return fmt.Sprintf(`case "$0" in
*"home screen"*)
  while [ ! -f %q ]; do sleep 0.01; done
  printf '%%s' "$0" > %q
  cat <<'MASTER_OUT'
%s
MASTER_OUT
  ;;
*)
  printf '{"type":"text","text":"turn finished"}\n'
  ;;
esac
`, releaseFile, promptFile, string(frame))
}

// Two sends into one conversation cannot interleave: the first claims the
// conversation's busy slot before its user message is recorded, and the second
// is refused before anything it says reaches the transcript or the prompt.
func TestMasterSendRejectsConcurrentSend(t *testing.T) {
	dir := t.TempDir()
	releaseFile := filepath.Join(dir, "release")
	t.Cleanup(func() { _ = os.WriteFile(releaseFile, nil, 0o644) })
	eng, disp := newMasterRPCFixture(t, masterParkingScript(filepath.Join(dir, "prompt.txt"), releaseFile, "first reply"))

	var created MasterThreadDetail
	callMasterRPC(t, disp, "1", "master.history.new", nil, &created)

	type sendOutcome struct {
		res orchestrator.MasterResult
		err error
	}
	done := make(chan sendOutcome, 1)
	go func() {
		out, err := disp.Handle(context.Background(), Request{
			ID: "a", Method: "master.send",
			Params: mustJSONParams(t, map[string]any{"text": "first question", "threadId": created.ID}),
		})
		if err != nil {
			done <- sendOutcome{err: err}
			return
		}
		done <- sendOutcome{res: out.(orchestrator.MasterResult)}
	}()

	// The first send records its user message before the model call parks:
	// that transcript entry is the in-flight barrier.
	waitThreadState(t, eng, created.ID, func(s *domain.ThreadState) bool { return len(s.Messages) == 1 })

	if _, err := disp.Handle(context.Background(), Request{
		ID: "b", Method: "master.send",
		Params: mustJSONParams(t, map[string]any{"text": "second question", "threadId": created.ID}),
	}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("concurrent send err = %v, want the busy-conversation refusal", err)
	}

	// Release the first send and wait for it to finish recording its reply.
	if err := os.WriteFile(releaseFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	outcome := <-done
	if outcome.err != nil {
		t.Fatalf("first send: %v", outcome.err)
	}
	if outcome.res.Reply != "first reply" || outcome.res.MasterThreadID != created.ID {
		t.Fatalf("first result = %+v", outcome.res)
	}

	// The transcript holds exactly the first exchange, under the split
	// authors that keep master chat out of per-turn state.
	st := waitThreadState(t, eng, created.ID, func(s *domain.ThreadState) bool { return len(s.Messages) == 2 })
	if st.Messages[0].Author != contracts.AuthorMasterUser || st.Messages[0].Text != "first question" {
		t.Fatalf("user message = %+v", st.Messages[0])
	}
	if st.Messages[1].Author != contracts.AuthorMaster || st.Messages[1].Text != "first reply" {
		t.Fatalf("reply = %+v", st.Messages[1])
	}
	for _, m := range st.Messages {
		if strings.Contains(m.Text, "second question") {
			t.Fatalf("refused send leaked into the transcript: %+v", st.Messages)
		}
	}
	// The split authors keep master chat out of per-turn state: a conversation
	// with an exchange must have opened no turns (the projector bumps Turn for
	// author "user" only).
	if st.Turn != 0 || len(st.Turns) != 0 {
		t.Fatalf("turn = %d (rows %d), want master exchanges to open no turns", st.Turn, len(st.Turns))
	}
}

// failingMasterReplyStore refuses the append that would record the master's
// reply, so the degraded reply path can be exercised deterministically: every
// other append (thread create, the user's message) passes through.
type failingMasterReplyStore struct {
	*domain.MemoryStore
}

func (s *failingMasterReplyStore) Append(ctx context.Context, id contracts.ThreadID, payloads []contracts.EventPayload, clock domain.Clock) ([]contracts.Event, error) {
	for _, p := range payloads {
		if msg, ok := p.(contracts.MessageSent); ok && msg.Author == contracts.AuthorMaster {
			return nil, errors.New("store refused the reply append")
		}
	}
	return s.MemoryStore.Append(ctx, id, payloads, clock)
}

// A reply that cannot be recorded does not fail the send: the exchange already
// ran, so the client keeps the answer and the correlation id, with the failure
// folded into a non-fatal warning.
func TestMasterSendReplyRecordFailureKeepsReply(t *testing.T) {
	_, disp := newMasterRPCFixtureStore(t, masterDispatchScript("/dev/null", "", "Saved on disk."),
		&failingMasterReplyStore{MemoryStore: domain.NewMemoryStore()})

	var created MasterThreadDetail
	callMasterRPC(t, disp, "1", "master.history.new", nil, &created)

	var res orchestrator.MasterResult
	callMasterRPC(t, disp, "2", "master.send", map[string]any{"text": "hello there", "threadId": created.ID}, &res)
	if res.Reply != "Saved on disk." {
		t.Fatalf("Reply = %q, want the model's answer kept", res.Reply)
	}
	if res.MasterThreadID != created.ID {
		t.Fatalf("MasterThreadID = %q, want the correlation kept", res.MasterThreadID)
	}
	if !strings.Contains(res.Warning, "could not be saved") {
		t.Fatalf("Warning = %q, want the non-fatal warning", res.Warning)
	}

	// The user's message stayed durable; only the reply append failed.
	var detail MasterThreadDetail
	callMasterRPC(t, disp, "3", "master.history.open", map[string]any{"threadId": created.ID}, &detail)
	if len(detail.Messages) != 1 || detail.Messages[0].Role != "user" || detail.Messages[0].Text != "hello there" {
		t.Fatalf("messages = %+v, want the recorded user message only", detail.Messages)
	}
}
