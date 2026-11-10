package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// awaitFrame reads streamed thread.event frames until one of the wanted type
// arrives, and returns its params. The deadline only bounds a failing run.
func awaitFrame(t *testing.T, c *websocket.Conn, eventType string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %s: %v", eventType, err)
		}
		var frame struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(msg, &frame) != nil || frame.Method != "thread.event" {
			continue
		}
		if frame.Params["type"] == eventType {
			return frame.Params
		}
	}
}

// askOverHTTP posts a blocking `tool ask` to a daemon's HTTP surface in the
// background. The reply, or the error, arrives on the channel.
func askOverHTTP(ctx context.Context, baseURL string, body map[string]any) <-chan map[string]any {
	out := make(chan map[string]any, 1)
	raw, _ := json.Marshal(body)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/tool/call", bytes.NewReader(raw))
		if err != nil {
			out <- map[string]any{"error": err.Error()}
			return
		}
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			out <- map[string]any{"error": err.Error()}
			return
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		var reply map[string]any
		_ = json.Unmarshal(data, &reply)
		if resp.StatusCode != http.StatusOK {
			reply = map[string]any{"status": resp.StatusCode, "body": string(data)}
		}
		out <- reply
	}()
	return out
}

// An agent's question is answered from the desktop's socket (the embedded daemon
// and a headless atd run this same path): the blocked ask returns the answer.
func TestAskAnsweredOverTheSocket(t *testing.T) {
	d := newTestDaemon(t)
	ctx := t.Context()
	if err := d.Engine.Dispatch(ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	c, _ := dialWS(t, d)
	call(t, c, "1", "thread.subscribe", map[string]any{"threadId": "t1"})

	srv := httptest.NewServer(d.Server.Handler())
	t.Cleanup(srv.Close)
	reply := askOverHTTP(ctx, srv.URL, map[string]any{
		"threadId": "t1", "agentId": "agent-orchestrator", "kind": "ask",
		"questionKind": "choice", "text": "Ship it?", "options": []string{"yes", "not yet"},
	})

	asked := awaitFrame(t, c, string(contracts.EvtQuestionAsked))
	qid := asked["payload"].(map[string]any)["questionId"].(string)
	call(t, c, "2", "question.answer", map[string]any{"threadId": "t1", "questionId": qid, "selected": []string{"yes"}})

	got := <-reply
	if got["answered"] != true || got["selected"].([]any)[0] != "yes" {
		t.Fatalf("ask reply = %+v, want the answer yes", got)
	}
}

// A daemon that dies while an agent waits leaves the question pending in its log.
// The next daemon over the same state directory closes it, so the UI never shows
// a question nobody can answer.
func TestPendingQuestionClosedAfterDaemonRestart(t *testing.T) {
	stateDir := t.TempDir()
	open := func() *Daemon {
		t.Helper()
		driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(providers.SessionSpec) (providers.CommandSpec, error) {
			return providers.CommandSpec{Binary: "sh", Args: []string{"-c", e2eScript}, PromptVia: "stdin"}, nil
		}, providers.JSONLineTranslator{})
		d, err := New(context.Background(), Options{
			StateDir: stateDir,
			Token:    "secret",
			Registry: providers.NewRegistry(driver),
			Config:   orchestrator.Config{MaxPasses: 2, MaxConcurrency: 2},
		})
		if err != nil {
			t.Fatalf("daemon: %v", err)
		}
		return d
	}

	d1 := open()
	if err := d1.Engine.Dispatch(t.Context(), contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	events, unsub := d1.Engine.Subscribe("t1")
	defer unsub()
	srv := httptest.NewServer(d1.Server.Handler())
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	reply := askOverHTTP(ctx, srv.URL, map[string]any{
		"threadId": "t1", "agentId": "agent-orchestrator", "kind": "ask",
		"questionKind": "text", "text": "Still there?",
	})
	for ev := range events {
		if ev.Type() == contracts.EvtQuestionAsked {
			break
		}
	}

	// The daemon goes down first, then its waiting caller: the caller's close can
	// no longer be recorded, which is exactly the crash case.
	d1.Close()
	cancel()
	<-reply

	d2 := open()
	t.Cleanup(d2.Close)
	st, err := d2.Engine.State(t.Context(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Questions) != 1 || st.Questions[0].Status != "closed" || st.Questions[0].Reason != "daemon restarted" {
		t.Fatalf("questions after restart = %+v, want one closed by the restart", st.Questions)
	}
}
