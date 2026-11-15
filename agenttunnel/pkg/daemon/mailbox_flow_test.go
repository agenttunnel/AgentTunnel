package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// flowScript drives one full turn of directed peer messaging:
//
//   - pass 0: the lead dispatches `first` and `second` (which waits on `first`)
//     and sends `second` a message while it still waits;
//   - `first` blocks until the test drops its sync file, so the test can speak
//     for it over /tool/call exactly the way the real tool binary does — one
//     message to a teammate, one report to the lead;
//   - `second` asserts both messages landed in its own prompt;
//   - pass 1: the lead asserts the report arrived in its "## Peer messages".
const flowScript = `case "$AT_ROLE" in
orchestrator)
  case "$AT_PASS" in
    0)
      printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","taskId":"first","task":"do the first thing","role":"executor"}'
      printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","taskId":"second","task":"do the second thing","role":"executor","dependsOn":["first"]}'
      printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"message","to":"second","body":"use the config from the first step"}'
      printf '%s\n' 'dispatched two agents'
      ;;
    *)
      fail=0
      case "$1" in *"## Peer messages"*) ;; *) fail=1 ;; esac
      case "$1" in *"from agent-first: the flaky test is payment.e2e"*) ;; *) fail=1 ;; esac
      if [ "$fail" = 1 ]; then
        printf '%s\n' "pass prompt lacked the subagent report"
        exit 1
      fi
      printf '%s\n' 'all done'
      ;;
  esac ;;
*)
  case "$AT_TASK" in
    *first*)
      while [ ! -f "$AT_PEER_SYNC" ]; do sleep 0.05; done
      printf '%s\n' 'first finished'
      ;;
    *second*)
      fail=0
      case "$1" in *"## Messages from teammates"*) ;; *) fail=1 ;; esac
      case "$1" in *"from agent-orchestrator: use the config from the first step"*) ;; *) fail=1 ;; esac
      case "$1" in *"from agent-first: the budget is five hours"*) ;; *) fail=1 ;; esac
      if [ "$fail" = 1 ]; then
        printf '%s\n' "second prompt lacked teammate messages"
        exit 1
      fi
      printf '%s\n' 'second finished'
      ;;
  esac ;;
esac`

func newFlowDaemon(t *testing.T) *Daemon {
	t.Helper()
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", flowScript, "sh", spec.Prompt}, PromptVia: "arg"}, nil
	}, providers.JSONLineTranslator{})
	d, err := New(context.Background(), Options{
		StateDir: t.TempDir(),
		Token:    "secret",
		Registry: providers.NewRegistry(driver),
		Config:   orchestrator.Config{MaxPasses: 2, MaxConcurrency: 2},
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

func TestPeerMessageFlowThroughARealTurn(t *testing.T) {
	d := newFlowDaemon(t)
	c, baseURL := dialWS(t, d)

	// No setPeerMailbox call: threads come with peer messaging on, and the
	// state must say so.
	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "peers"}), &created)
	var gate struct {
		PeerMailbox bool `json:"peerMailbox"`
	}
	json.Unmarshal(call(t, c, "2", "thread.get", map[string]any{"threadId": created.ThreadID}), &gate)
	if !gate.PeerMailbox {
		t.Fatal("peer mailbox should be on by default")
	}

	// Harness children inherit this process's environment at spawn time: the
	// sync file lets `first` hold the turn open until the test has spoken for
	// it. The file itself is only written later.
	sync := filepath.Join(t.TempDir(), "peer-sync")
	t.Setenv("AT_PEER_SYNC", sync)

	call(t, c, "3", "thread.subscribe", map[string]any{"threadId": created.ThreadID})
	turnData, _ := json.Marshal(transport.Request{
		ID: "turn", Method: "thread.turn.start",
		Params: mustJSON(t, map[string]any{"threadId": created.ThreadID, "text": "work together"}),
	})
	go func() {
		_ = c.Write(context.Background(), websocket.MessageText, turnData)
	}()

	// Wait until `second` is dispatched — by then `first` is running and
	// blocked on the sync file, so the messages below land mid-run. Anchor on
	// the dispatch event: the lead's message to `second` also names the agent
	// and fires earlier.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if strings.Contains(string(msg), string(contracts.EvtTaskDispatched)) && strings.Contains(string(msg), "agent-second") {
			break
		}
	}
	postPeerMessage(t, baseURL, string(created.ThreadID), "agent-first", "second", "the budget is five hours")
	postPeerMessage(t, baseURL, string(created.ThreadID), "agent-first", "orchestrator", "the flaky test is payment.e2e")

	if err := os.WriteFile(sync, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wait for the turn to finish before reading the durable record.
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var resp transport.Response
		if json.Unmarshal(msg, &resp) != nil || resp.Method == "thread.event" || resp.ID != "turn" {
			continue
		}
		if resp.Error != nil {
			t.Fatalf("turn failed: %s", resp.Error.Message)
		}
		break
	}

	var state struct {
		Mailbox []struct {
			From contracts.AgentID `json:"from"`
			To   contracts.AgentID `json:"to"`
			Kind string            `json:"kind"`
			Body string            `json:"body"`
			At   time.Time         `json:"at"`
		} `json:"mailbox"`
		Messages []struct {
			Author string `json:"author"`
			Text   string `json:"text"`
		} `json:"messages"`
		Agents map[string]struct {
			Status string `json:"status"`
		} `json:"agents"`
	}
	json.Unmarshal(call(t, c, "poll", "thread.get", map[string]any{"threadId": created.ThreadID}), &state)

	if len(state.Mailbox) != 3 {
		t.Fatalf("mailbox = %+v", state.Mailbox)
	}
	got := map[string]bool{}
	for _, m := range state.Mailbox {
		got[string(m.From)+" -> "+string(m.To)+": "+m.Body] = true
		if m.Kind != "message" || m.At.IsZero() {
			t.Fatalf("entry = %+v", m)
		}
	}
	for _, want := range []string{
		"agent-orchestrator -> agent-second: use the config from the first step",
		"agent-first -> agent-second: the budget is five hours",
		"agent-first -> agent-orchestrator: the flaky test is payment.e2e",
	} {
		if !got[want] {
			t.Fatalf("missing %q in %+v", want, state.Mailbox)
		}
	}

	// The scripted assertions proved prompt delivery inside the harness; a
	// failed script surfaces as a failed agent, so require both green.
	failed := ""
	for _, id := range []string{"agent-first", "agent-second"} {
		agent, ok := state.Agents[id]
		if !ok {
			t.Fatalf("agent %s missing: %+v", id, state.Agents)
		}
		if agent.Status != "succeeded" {
			failed += id + "=" + agent.Status + " "
		}
	}
	// Every agent's raw trail, for diagnosing delivery failures.
	for _, id := range []string{"agent-orchestrator", "agent-first", "agent-second"} {
		var logs []struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		json.Unmarshal(call(t, c, "logs-"+id, "agents.logs", map[string]any{"agentId": id}), &logs)
		for _, l := range logs {
			t.Logf("TRAIL %s %s: %s", id, l.Kind, l.Text)
		}
	}
	if failed != "" {
		t.Fatalf("agent statuses: %s", failed)
	}
	final := state.Messages[len(state.Messages)-1].Text
	if final != "all done" {
		t.Fatalf("final message = %q", final)
	}
}

// postPeerMessage performs the exact HTTP call an agent's tool binary makes.
func postPeerMessage(t *testing.T, baseURL, threadID, from, to, text string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"threadId": threadID, "agentId": from, "kind": "message", "to": to, "text": text,
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/tool/call", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post message: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post message %s -> %s: status %d", from, to, resp.StatusCode)
	}
}
