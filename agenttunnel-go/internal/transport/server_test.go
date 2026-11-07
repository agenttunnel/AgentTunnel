package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// newMessageFixture assembles the minimum HTTP surface a /tool/call message
// post needs, with a real engine so durable effects are observable.
func newMessageFixture(t *testing.T) (*domain.Engine, *orchestrator.Runner, *Server) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	runner := orchestrator.NewRunner(orchestrator.Deps{
		Engine:  eng,
		Logs:    logstore.New(t.TempDir()),
		Mailbox: contextstore.NewMailbox(),
	})
	disp := NewDispatcher(DispatcherDeps{Engine: eng, Runner: runner})
	return eng, runner, NewServer(disp, eng, runner, "", toolbox.New())
}

func postToolCall(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/tool/call", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHandleToolCallMessage(t *testing.T) {
	ctx := context.Background()
	eng, runner, srv := newMessageFixture(t)

	if err := eng.Dispatch(ctx, contracts.Command{ID: "c0", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := runner.Dispatch(ctx, "t1", []contracts.DispatchTask{{
		AgentID: "agent-hub", Name: "hub", Role: contracts.RoleExecutor, Task: "hub work",
	}}); err != nil {
		t.Fatalf("dispatch hub: %v", err)
	}

	// The peer mailbox is off: rejected with 400, nothing recorded.
	rec := postToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"message","to":"hub","text":"hi"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "peer mailbox is disabled for this thread") {
		t.Fatalf("gate off: status=%d body=%q", rec.Code, rec.Body.String())
	}
	state, _ := eng.State(ctx, "t1")
	if len(state.Mailbox) != 0 {
		t.Fatalf("rejected message was recorded: %+v", state.Mailbox)
	}

	on := true
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c1", Type: contracts.CmdPeerMailboxSet, ThreadID: "t1", PeerMailbox: &on}); err != nil {
		t.Fatalf("enable peer mailbox: %v", err)
	}
	runner.Mailbox().SetEnabled(true)

	// Unknown recipient: 400, nothing recorded.
	rec = postToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"message","to":"ghost","text":"hi"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown agent ghost") {
		t.Fatalf("unknown recipient: status=%d body=%q", rec.Code, rec.Body.String())
	}
	state, _ = eng.State(ctx, "t1")
	if len(state.Mailbox) != 0 {
		t.Fatalf("rejected message was recorded: %+v", state.Mailbox)
	}

	// A real post: 200, one durable event, runtime delivery to the recipient.
	rec = postToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"message","to":"hub","text":"ping the hub"}`)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("valid post: status=%d body=%q", rec.Code, rec.Body.String())
	}
	state, _ = eng.State(ctx, "t1")
	if len(state.Mailbox) != 1 || state.Mailbox[0].To != "agent-hub" || state.Mailbox[0].Body != "ping the hub" {
		t.Fatalf("mailbox = %+v", state.Mailbox)
	}
	if inbox := runner.Mailbox().Inbox("agent-hub"); len(inbox) != 1 {
		t.Fatalf("hub inbox = %+v", inbox)
	}
}

func TestHandleToolCallMessageRequiresToAndText(t *testing.T) {
	_, _, srv := newMessageFixture(t)
	for _, body := range []string{
		`{"threadId":"t1","kind":"message","text":"hi"}`,
		`{"threadId":"t1","kind":"message","to":"hub"}`,
	} {
		if rec := postToolCall(t, srv, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d body=%q", body, rec.Code, rec.Body.String())
		}
	}
}
