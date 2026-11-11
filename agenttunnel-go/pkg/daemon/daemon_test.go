package daemon

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

const e2eScript = `if [ "$AT_PASS" = "0" ]; then
  printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","taskId":"impl","task":"implement","role":"executor"}'
fi
printf '{"type":"text","text":"report-%s"}\n' "$AT_PASS"
printf '{"usage":{"input_tokens":3,"output_tokens":2}}\n'`

func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	return newTestDaemonWith(t, Options{})
}

// newTestDaemonWith assembles a daemon for tests that need to override an
// option (e.g. the WhatsApp adapter) while sharing the scripted harness.
func newTestDaemonWith(t *testing.T, opts Options) *Daemon {
	t.Helper()
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", e2eScript}, PromptVia: "stdin"}, nil
	}, providers.JSONLineTranslator{})
	opts.StateDir = t.TempDir()
	opts.Token = "secret"
	opts.Registry = providers.NewRegistry(driver)
	opts.Config = orchestrator.Config{MaxPasses: 2, MaxConcurrency: 2}
	d, err := New(context.Background(), opts)
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

func dialWS(t *testing.T, d *Daemon) (*websocket.Conn, string) {
	t.Helper()
	srv := httptest.NewServer(d.Server.Handler())
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?token=secret"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c, srv.URL
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

func call(t *testing.T, c *websocket.Conn, id, method string, params any) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := transport.Request{ID: id, Method: method, Params: mustJSON(t, params)}
	data, _ := json.Marshal(req)
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read %s: %v", method, err)
		}
		var resp transport.Response
		if err := json.Unmarshal(msg, &resp); err != nil {
			continue
		}
		if resp.Method == "thread.event" {
			continue // streamed event, not our reply
		}
		if resp.ID != id {
			continue
		}
		if resp.Error != nil {
			t.Fatalf("%s error: %s", method, resp.Error.Message)
		}
		return mustJSON(t, resp.Result)
	}
}

func TestDaemonRejectsBadToken(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.Server.Handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws?token=wrong", nil)
	if err == nil {
		t.Fatal("expected dial to fail without a valid token")
	}
	if resp != nil && resp.StatusCode != 401 {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestDaemonEndToEnd(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var roles []contracts.Role
	json.Unmarshal(call(t, c, "1", "roles.list", nil), &roles)
	if len(roles) < 5 {
		t.Fatalf("roles = %d, want >= 5", len(roles))
	}

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "2", "thread.create", map[string]any{"title": "e2e"}), &created)
	if created.ThreadID == "" {
		t.Fatal("no thread id returned")
	}

	call(t, c, "3", "thread.subscribe", map[string]any{"threadId": created.ThreadID})
	call(t, c, "4", "thread.turn.start", map[string]any{"threadId": created.ThreadID, "text": "build it"})

	var state struct {
		Title    string `json:"title"`
		Messages []struct {
			Author string `json:"author"`
			Text   string `json:"text"`
		} `json:"messages"`
		Agents map[string]contracts.Agent `json:"agents"`
	}
	json.Unmarshal(call(t, c, "5", "thread.get", map[string]any{"threadId": created.ThreadID}), &state)

	if state.Title != "e2e" {
		t.Fatalf("title = %q", state.Title)
	}
	if _, ok := state.Agents["agent-orchestrator"]; !ok {
		t.Fatalf("orchestrator agent missing: %+v", state.Agents)
	}
	if _, ok := state.Agents["agent-impl"]; !ok {
		t.Fatalf("impl agent missing: %+v", state.Agents)
	}
	hasOrchMessage := false
	for _, m := range state.Messages {
		if m.Author == "orchestrator" {
			hasOrchMessage = true
		}
	}
	if !hasOrchMessage {
		t.Fatalf("no orchestrator message: %+v", state.Messages)
	}

	var u contracts.ThreadUsage
	json.Unmarshal(call(t, c, "6", "usage.get", nil), &u)
	if u.Total().Total() == 0 {
		t.Fatal("no usage recorded")
	}

	var logs []contracts.LogEntry
	json.Unmarshal(call(t, c, "7", "agents.logs", map[string]any{"threadId": created.ThreadID, "agentId": "agent-impl"}), &logs)
	if len(logs) == 0 {
		t.Fatal("no agent logs recorded")
	}
}

func TestDaemonStreamsThreadEvents(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "stream"}), &created)
	call(t, c, "2", "thread.subscribe", map[string]any{"threadId": created.ThreadID})

	turn := transport.Request{
		ID:     "3",
		Method: "thread.turn.start",
		Params: mustJSON(t, map[string]any{"threadId": created.ThreadID, "text": "go"}),
	}
	go func() {
		data, _ := json.Marshal(turn)
		_ = c.Write(context.Background(), websocket.MessageText, data)
	}()

	// The turn reply arrives only after the turn settles. Returning on the
	// dispatch event alone would race the agents still writing logs under the
	// TempDir state dir, and the TempDir cleanup would fail.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sawDispatch := false
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (sawDispatch=%v)", err, sawDispatch)
		}
		var resp transport.Response
		if err := json.Unmarshal(msg, &resp); err != nil {
			continue
		}
		if resp.Method == "thread.event" {
			var ev struct {
				Type string `json:"type"`
			}
			json.Unmarshal(mustJSON(t, resp.Params), &ev)
			if ev.Type == string(contracts.EvtTaskDispatched) {
				sawDispatch = true
			}
			continue
		}
		if resp.ID != turn.ID {
			continue
		}
		if resp.Error != nil {
			t.Fatalf("turn failed: %s", resp.Error.Message)
		}
		break
	}
	if !sawDispatch {
		t.Fatal("no task-dispatched event streamed")
	}
}
