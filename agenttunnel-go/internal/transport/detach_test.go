package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/steering"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
	"github.com/agenttunnel/agenttunnel-go/internal/usage"
)

// detachFixture assembles the real turn pipeline over a scripted sh harness, so
// a test can start a turn over the WebSocket and then take its socket away.
type detachFixture struct {
	eng *domain.Engine
	srv *Server
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func newDetachFixture(t *testing.T, script string) (*detachFixture, contracts.ThreadID) {
	t.Helper()
	ctx := t.Context()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script}, PromptVia: "stdin"}, nil
	}, providers.JSONLineTranslator{})
	store, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"), t.TempDir())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	box := toolbox.New()
	runner := orchestrator.NewRunner(orchestrator.Deps{
		Engine:    eng,
		Registry:  providers.NewRegistry(driver),
		Roles:     contracts.NewRoleRegistry(contracts.BuiltinRoles()...),
		Logs:      logstore.New(t.TempDir()),
		Ledger:    usage.NewLedger(),
		Mailbox:   contextstore.NewMailbox(),
		Inherited: contextstore.NewInheritedContext(t.TempDir()),
		Settings:  store,
		Tools:     box,
		Steering:  steering.New(),
		Cfg:       orchestrator.Config{MaxPasses: 1, MaxConcurrency: 2},
	})
	orch := orchestrator.New(runner)
	disp := NewDispatcher(DispatcherDeps{
		Engine: eng, Runner: runner, Orchestrator: orch,
		Registry: providers.NewRegistry(driver), Roles: contracts.NewRoleRegistry(contracts.BuiltinRoles()...),
		Logs: logstore.New(t.TempDir()), Ledger: usage.NewLedger(), Settings: store,
		BaseCtx: ctx,
	})

	const threadID contracts.ThreadID = "t-detach"
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: threadID, Title: "detach"}); err != nil {
		t.Fatal(err)
	}
	return &detachFixture{eng: eng, srv: NewServer(disp, eng, runner, "secret", box)}, threadID
}

func (f *detachFixture) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	httpSrv := httptest.NewServer(f.srv.Handler())
	t.Cleanup(httpSrv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/ws?token=secret", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

// startDetached sends thread.turn.start with detach and returns the reply once
// it arrives (it is immediate, unlike a foreground turn).
func startDetached(t *testing.T, c *websocket.Conn, threadID contracts.ThreadID, text string) map[string]any {
	t.Helper()
	req := Request{ID: "detach", Method: "thread.turn.start", Params: mustJSON(t, map[string]any{
		"threadId": threadID, "text": text, "detach": true,
	})}
	data, _ := json.Marshal(req)
	if err := c.Write(t.Context(), websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read reply: %v", err)
		}
		var resp Response
		if json.Unmarshal(msg, &resp) != nil || resp.Method != "" || resp.ID != "detach" {
			continue
		}
		if resp.Error != nil {
			t.Fatalf("detach reply: %s", resp.Error.Message)
		}
		var out map[string]any
		if err := json.Unmarshal(mustJSON(t, resp.Result), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
}

// waitForStatus consumes committed events until the agent reaches status.
func waitForStatus(t *testing.T, events <-chan contracts.Event, agent contracts.AgentID, status contracts.AgentStatus) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("agent %s never reached %s", agent, status)
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("event channel closed before %s reached %s", agent, status)
			}
			if p, ok := ev.Payload.(contracts.AgentStatusChanged); ok && p.AgentID == agent && p.Status == status {
				return
			}
		}
	}
}

// A detached turn belongs to the daemon, not the client socket: closing the
// socket right after the reply must not cancel it, and its events must land in
// the durable log.
func TestDetachedTurnSurvivesClientDisconnect(t *testing.T) {
	f, threadID := newDetachFixture(t, `printf '{"type":"text","text":"worked"}\n'`)
	events, unsub := f.eng.Subscribe(threadID)
	defer unsub()

	c := f.dial(t)
	out := startDetached(t, c, threadID, "do the work")
	if out["ok"] != true || out["detached"] != true || out["threadId"] != string(threadID) {
		t.Fatalf("reply = %+v", out)
	}

	// The client goes away the moment it has its receipt.
	c.CloseNow()

	waitForStatus(t, events, "agent-orchestrator", contracts.AgentSucceeded)

	st, err := f.eng.State(t.Context(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Agents["agent-orchestrator"].Status != contracts.AgentSucceeded {
		t.Fatalf("orchestrator = %+v", st.Agents["agent-orchestrator"])
	}
	var sawUser, sawOrchestrator bool
	for _, m := range st.Messages {
		switch m.Author {
		case "user":
			sawUser = true
		case "orchestrator":
			sawOrchestrator = true
		}
	}
	if !sawUser || !sawOrchestrator {
		t.Fatalf("messages = %+v, want the user turn and the orchestrator's report", st.Messages)
	}

	// The turn's events are durable, not just streamed to the gone client.
	committed, err := f.eng.Events(t.Context(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	types := map[contracts.EventType]bool{}
	for _, ev := range committed {
		types[ev.Type()] = true
	}
	for _, want := range []contracts.EventType{contracts.EvtMessageSent, contracts.EvtTaskDispatched, contracts.EvtAgentStatusChanged} {
		if !types[want] {
			t.Fatalf("event log is missing %s: %v", want, types)
		}
	}
}

// daemon.shutdown acknowledges before it triggers the assembly's shutdown, so
// `agenttunnel daemon stop` always gets its reply.
func TestDaemonShutdownRepliesThenTriggersCallback(t *testing.T) {
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(t.Context())
	t.Cleanup(eng.Close)
	runner := orchestrator.NewRunner(orchestrator.Deps{Engine: eng, Logs: logstore.New(t.TempDir())})
	triggered := make(chan struct{})
	disp := NewDispatcher(DispatcherDeps{
		Engine: eng, Runner: runner,
		Shutdown: func() { close(triggered) },
	})
	srv := NewServer(disp, eng, runner, "", toolbox.New())
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	req := Request{ID: "shut", Method: "daemon.shutdown"}
	data, _ := json.Marshal(req)
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var resp Response
		if json.Unmarshal(msg, &resp) != nil || resp.Method != "" || resp.ID != "shut" {
			continue
		}
		if resp.Error != nil {
			t.Fatalf("shutdown reply: %s", resp.Error.Message)
		}
		var out map[string]any
		_ = json.Unmarshal(mustJSON(t, resp.Result), &out)
		if out["ok"] != true {
			t.Fatalf("reply = %+v", out)
		}
		break
	}
	select {
	case <-triggered:
	case <-ctx.Done():
		t.Fatal("shutdown callback was never invoked")
	}
}

// thread.turn.stop is the one thing that cancels a detached turn.
func TestThreadTurnStopCancelsDetachedTurn(t *testing.T) {
	f, threadID := newDetachFixture(t, `sleep 30; printf '{"type":"text","text":"late"}\n'`)
	events, unsub := f.eng.Subscribe(threadID)
	defer unsub()

	client := f.dial(t)
	out := startDetached(t, client, threadID, "run long")
	if out["detached"] != true {
		t.Fatalf("reply = %+v", out)
	}
	waitForStatus(t, events, "agent-orchestrator", contracts.AgentRunning)
	client.CloseNow()

	// A fresh connection stops the still-running detached turn.
	stopper := f.dial(t)
	req := Request{ID: "stop", Method: "thread.turn.stop", Params: mustJSON(t, map[string]any{"threadId": threadID})}
	data, _ := json.Marshal(req)
	if err := stopper.Write(t.Context(), websocket.MessageText, data); err != nil {
		t.Fatalf("stop write: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopped := false
	for !stopped {
		_, msg, err := stopper.Read(ctx)
		if err != nil {
			t.Fatalf("stop reply: %v", err)
		}
		var resp Response
		if json.Unmarshal(msg, &resp) != nil || resp.Method != "" || resp.ID != "stop" {
			continue
		}
		if resp.Error != nil {
			t.Fatalf("stop: %s", resp.Error.Message)
		}
		var res map[string]any
		_ = json.Unmarshal(mustJSON(t, resp.Result), &res)
		stopped = res["stopped"] == true
	}
	waitForStatus(t, events, "agent-orchestrator", contracts.AgentCancelled)
}
