package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// postToolMessageRaw posts a /tool/call message the way the tool binary does
// and returns the raw status and body, for cases that must assert rejections
// (postPeerMessage fails the test on any non-200).
func postToolMessageRaw(t *testing.T, baseURL, threadID, from, to, text string) (int, string) {
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
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestSetPeerMailboxRPCGatesMessages covers the thread.setPeerMailbox RPC
// routing itself — the desktop "Peer chat" toggle's only path — and the user
// visible consequence of each toggle state on the /tool/call message flow:
// opting out rejects posts with nothing recorded, opting back in accepts them.
// engine-level coverage exists for the command and the decider gate, but not
// for the RPC switch case and its params decode.
func TestSetPeerMailboxRPCGatesMessages(t *testing.T) {
	d := newTestDaemon(t)
	c, baseURL := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "gate"}), &created)

	var view struct {
		PeerMailbox bool `json:"peerMailbox"`
		Mailbox     []struct {
			From contracts.AgentID `json:"from"`
			To   contracts.AgentID `json:"to"`
		} `json:"mailbox"`
	}
	read := func(id string) {
		t.Helper()
		json.Unmarshal(call(t, c, id, "thread.get", map[string]any{"threadId": created.ThreadID}), &view)
	}

	// Threads come with peer messaging on: the toggle's job is the opt-out.
	read("2")
	if !view.PeerMailbox {
		t.Fatal("peer mailbox should be on by default")
	}

	// Opt out over the RPC (what the desktop toggle does).
	var ok struct {
		OK bool `json:"ok"`
	}
	json.Unmarshal(call(t, c, "3", "thread.setPeerMailbox", map[string]any{
		"threadId": created.ThreadID, "enabled": false,
	}), &ok)
	if !ok.OK {
		t.Fatal("setPeerMailbox should acknowledge")
	}
	read("4")
	if view.PeerMailbox {
		t.Fatal("peer mailbox should be off after the RPC toggle")
	}

	// A message posted while gated is rejected with nothing recorded.
	code, body := postToolMessageRaw(t, baseURL, string(created.ThreadID), "agent-orchestrator", "orchestrator", "psst")
	if code != http.StatusBadRequest || !strings.Contains(body, "peer mailbox is disabled for this thread") {
		t.Fatalf("gated post: status=%d body=%q", code, body)
	}
	read("5")
	if len(view.Mailbox) != 0 {
		t.Fatalf("rejected message was recorded: %+v", view.Mailbox)
	}

	// Opt back in: the same post now goes through and is durable.
	json.Unmarshal(call(t, c, "6", "thread.setPeerMailbox", map[string]any{
		"threadId": created.ThreadID, "enabled": true,
	}), &ok)
	if !ok.OK {
		t.Fatal("setPeerMailbox should acknowledge")
	}
	code, body = postToolMessageRaw(t, baseURL, string(created.ThreadID), "agent-orchestrator", "orchestrator", "back online")
	if code != http.StatusOK || strings.TrimSpace(body) != `{"ok":true}` {
		t.Fatalf("ungated post: status=%d body=%q", code, body)
	}
	read("7")
	if len(view.Mailbox) != 1 || view.Mailbox[0].From != "agent-orchestrator" || view.Mailbox[0].To != "agent-orchestrator" {
		t.Fatalf("mailbox = %+v", view.Mailbox)
	}

	// A whitespace-only body is rejected at the HTTP boundary, not posted.
	code, body = postToolMessageRaw(t, baseURL, string(created.ThreadID), "agent-orchestrator", "orchestrator", "   ")
	if code != http.StatusBadRequest || !strings.Contains(body, "to and text are required") {
		t.Fatalf("empty post: status=%d body=%q", code, body)
	}

	// Toggling an unknown thread fails as an RPC error, not a silent ok.
	_, rpcErr := rawCall(t, c, "8", "thread.setPeerMailbox", map[string]any{
		"threadId": "th_missing", "enabled": true,
	})
	if rpcErr == nil {
		t.Fatal("setPeerMailbox on an unknown thread should fail")
	}
}

// graphScript drives one turn whose traffic the agent graph read model must
// expose: the lead dispatches a researcher, the researcher spends tokens and
// reports to the lead with a printed `tool message` line. The report is
// drained by toolTasks only after the next pass, so it surfaces in the final
// summary's late-peer-messages section (mid-run HTTP delivery is covered by
// TestPeerMessageFlowThroughARealTurn). Commands ride JSON text events with
// trailing newlines: consecutive plain lines are merged before parsing.
const graphScript = `case "$AT_ROLE" in
orchestrator)
  case "$AT_PASS" in
    0)
      printf '{"type":"text","text":"tool dispatch --role executor --name researcher --task \\"map the repo\\"\\n"}\n'
      printf '{"type":"text","text":"dispatched"}\n'
      ;;
    *)
      printf '{"type":"text","text":"all done"}\n'
      ;;
  esac
  printf '{"usage":{"input_tokens":5,"output_tokens":4}}\n'
  ;;
*)
  printf '{"type":"text","text":"tool message --to orchestrator --text \\"mapped the repo\\"\\n"}\n'
  printf '{"usage":{"input_tokens":3,"output_tokens":2}}\n'
  printf '{"type":"text","text":"researched"}\n'
  ;;
esac`

// TestThreadGetExposesGraphReadModel pins the whole thread.get payload the
// desktop graph view renders from, in one place: agents with their spawn
// edges and terminal status, agentOrder, the directed mailbox entries, and
// per-agent token usage.
func TestThreadGetExposesGraphReadModel(t *testing.T) {
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", graphScript, "sh", spec.Prompt}, PromptVia: "arg"}, nil
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
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "graph"}), &created)
	call(t, c, "2", "thread.subscribe", map[string]any{"threadId": created.ThreadID})

	turnData, _ := json.Marshal(transport.Request{
		ID: "turn", Method: "thread.turn.start",
		Params: mustJSON(t, map[string]any{"threadId": created.ThreadID, "text": "map the repository"}),
	})
	go func() {
		_ = c.Write(context.Background(), websocket.MessageText, turnData)
	}()

	// Wait for the turn to finish before reading the read model.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
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
		Agents map[string]struct {
			Name          string            `json:"name"`
			ParentAgentID contracts.AgentID `json:"parentAgentId"`
			Status        string            `json:"status"`
		} `json:"agents"`
		AgentOrder []contracts.AgentID `json:"agentOrder"`
		Mailbox    []struct {
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
		Usage map[string]struct {
			Usage struct {
				InputTokens  int64 `json:"inputTokens"`
				OutputTokens int64 `json:"outputTokens"`
			} `json:"usage"`
		} `json:"usage"`
	}
	json.Unmarshal(call(t, c, "poll", "thread.get", map[string]any{"threadId": created.ThreadID}), &state)

	// The report reached the lead: the final orchestrator message carries the
	// late peer-message section.
	var final string
	for _, m := range state.Messages {
		if m.Author == "orchestrator" {
			final = m.Text
		}
	}
	if !strings.Contains(final, "Peer messages received after the final pass") ||
		!strings.Contains(final, "mapped the repo") {
		t.Fatalf("final message = %q, want the late peer report", final)
	}

	// Spawn edges: the lead has no parent; the researcher hangs off the lead
	// and reached a terminal status.
	orch, ok := state.Agents["agent-orchestrator"]
	if !ok {
		t.Fatalf("orchestrator missing from agents: %+v", state.Agents)
	}
	if orch.ParentAgentID != "" || orch.Status != "succeeded" {
		t.Fatalf("orchestrator = %+v", orch)
	}
	var researcher contracts.AgentID
	for _, id := range state.AgentOrder {
		if state.Agents[string(id)].Name == "researcher" {
			researcher = id
		}
	}
	if researcher == "" {
		t.Fatalf("researcher missing from agentOrder: %+v", state.AgentOrder)
	}
	if state.Agents[string(researcher)].ParentAgentID != "agent-orchestrator" || state.Agents[string(researcher)].Status != "succeeded" {
		t.Fatalf("researcher = %+v", state.Agents[string(researcher)])
	}
	if state.AgentOrder[0] != "agent-orchestrator" {
		t.Fatalf("agentOrder = %+v, want the lead first", state.AgentOrder)
	}

	// Message edges: exactly the researcher's directed report to the lead.
	if len(state.Mailbox) != 1 {
		t.Fatalf("mailbox = %+v", state.Mailbox)
	}
	msg := state.Mailbox[0]
	if msg.From != researcher || msg.To != "agent-orchestrator" ||
		msg.Kind != "message" || msg.Body != "mapped the repo" || msg.At.IsZero() {
		t.Fatalf("message = %+v (researcher = %s)", msg, researcher)
	}

	// Node metrics: per-agent usage recorded for both graph nodes.
	resUsage, ok := state.Usage[string(researcher)]
	if !ok || resUsage.Usage.InputTokens != 3 || resUsage.Usage.OutputTokens != 2 {
		t.Fatalf("researcher usage = %+v (%+v)", resUsage, state.Usage)
	}
	orchUsage, ok := state.Usage["agent-orchestrator"]
	if !ok || orchUsage.Usage.InputTokens < 5 || orchUsage.Usage.OutputTokens < 4 {
		t.Fatalf("orchestrator usage = %+v (%+v)", orchUsage, state.Usage)
	}
}
