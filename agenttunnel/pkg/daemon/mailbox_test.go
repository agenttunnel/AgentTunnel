package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// mailboxScript drives one full turn of peer messaging without a real model:
// the lead dispatches a researcher on pass 0; the researcher reports back with
// a printed `tool message` line (exactly once, via a marker file, so a second
// turn proves no re-delivery). The report must reach the lead's next pass
// prompt — the same pass the mid-run HTTP path delivers in — and must not be
// delivered again in a later turn.
const mailboxScript = `case "$AT_ROLE" in
orchestrator)
  if [ "$AT_PASS" = "0" ]; then
    printf '%s\n' 'tool dispatch --role executor --name researcher --task "find the flaky test"'
  else
    seen="${TMPDIR:-/tmp}/at-e2e-lead-$AT_THREAD_ID"
    case "$1" in
      *"the flaky test is payment.e2e"*)
        if [ -e "$seen" ]; then
          printf '{"type":"error","text":"the report was re-delivered"}\n'
          exit 1
        fi
        : > "$seen"
        ;;
      *)
        if [ ! -e "$seen" ]; then
          printf '{"type":"error","text":"pass prompt lacked the researcher report"}\n'
          exit 1
        fi
        ;;
    esac
  fi
  printf '{"type":"text","text":"pass-%s"}\n' "$AT_PASS"
  ;;
*)
  marker="${TMPDIR:-/tmp}/at-e2e-mailbox-$AT_THREAD_ID"
  if [ ! -e "$marker" ]; then
    printf '%s\n' 'tool message --to orchestrator --text "the flaky test is payment.e2e"'
    : > "$marker"
  fi
  printf '{"type":"text","text":"researched"}\n'
  ;;
esac`

type mailboxThreadState struct {
	Mailbox []struct {
		From string    `json:"from"`
		To   string    `json:"to"`
		Kind string    `json:"kind"`
		Body string    `json:"body"`
		At   time.Time `json:"at"`
	} `json:"mailbox"`
	Messages []struct {
		Author string `json:"author"`
		Text   string `json:"text"`
	} `json:"messages"`
}

func TestPeerMailboxEndToEnd(t *testing.T) {
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", mailboxScript, "sh", spec.Prompt}, PromptVia: "arg"}, nil
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
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "mailbox e2e"}), &created)
	call(t, c, "2", "thread.subscribe", map[string]any{"threadId": created.ThreadID})

	// Run the turn while listening on the same connection: every committed
	// event — including the researcher's mailbox.posted — must stream by
	// before the turn-start reply lands.
	turn := transport.Request{
		ID:     "turn1",
		Method: "thread.turn.start",
		Params: mustJSON(t, map[string]any{"threadId": created.ThreadID, "text": "find the flaky test"}),
	}
	go func() {
		data, _ := json.Marshal(turn)
		_ = c.Write(context.Background(), websocket.MessageText, data)
	}()

	sawMailboxEvent := false
	var posted struct {
		Type    string `json:"type"`
		Payload struct {
			From string `json:"from"`
			To   string `json:"to"`
			Kind string `json:"kind"`
			Body string `json:"body"`
		} `json:"payload"`
	}
	for {
		_, msg, err := c.Read(readCtx(t))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var resp transport.Response
		if err := json.Unmarshal(msg, &resp); err != nil {
			continue
		}
		if resp.Method == "thread.event" {
			var ev struct {
				Type    string `json:"type"`
				Payload struct {
					From string `json:"from"`
					To   string `json:"to"`
					Kind string `json:"kind"`
					Body string `json:"body"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(mustJSON(t, resp.Params), &ev); err != nil {
				t.Fatalf("event: %v", err)
			}
			if ev.Type == string(contracts.EvtMailboxPosted) {
				sawMailboxEvent = true
				posted = ev
			}
			continue
		}
		if resp.ID != "turn1" {
			continue
		}
		if resp.Error != nil {
			for _, e := range d.Logs.Entries(created.ThreadID) {
				t.Logf("orchestrator %s: %s", e.Kind, e.Text)
			}
			t.Fatalf("turn failed: %s", resp.Error.Message)
		}
		break
	}

	// (a) the durable post streamed to the subscriber with its full payload.
	if !sawMailboxEvent {
		t.Fatal("no mailbox.posted event reached the subscriber")
	}
	if posted.Type != string(contracts.EvtMailboxPosted) ||
		posted.Payload.From != "agent-researcher-1" || posted.Payload.To != "agent-orchestrator" ||
		posted.Payload.Kind != "message" || posted.Payload.Body != "the flaky test is payment.e2e" {
		t.Fatalf("event = %+v", posted)
	}

	// (b) the read model carries it with a timestamp, for the graph arrows.
	var state mailboxThreadState
	json.Unmarshal(call(t, c, "3", "thread.get", map[string]any{"threadId": created.ThreadID}), &state)
	if len(state.Mailbox) != 1 ||
		state.Mailbox[0].From != "agent-researcher-1" || state.Mailbox[0].To != "agent-orchestrator" ||
		state.Mailbox[0].Kind != "message" || state.Mailbox[0].Body != "the flaky test is payment.e2e" {
		t.Fatalf("state.mailbox = %+v", state.Mailbox)
	}
	if state.Mailbox[0].At.IsZero() {
		t.Fatalf("mailbox entry has no timestamp: %+v", state.Mailbox[0])
	}

	// (c) the report reached the lead's final pass prompt (the script fails
	// the harness otherwise), so the turn's last message is plain prose.
	final := ""
	for _, m := range state.Messages {
		if m.Author == "orchestrator" {
			final = m.Text
		}
	}
	if final != "pass-1" {
		t.Fatalf("final message = %q", final)
	}

	// (d) a second turn does not re-see the message.
	call(t, c, "4", "thread.turn.start", map[string]any{"threadId": created.ThreadID, "text": "summarize"})
	json.Unmarshal(call(t, c, "5", "thread.get", map[string]any{"threadId": created.ThreadID}), &state)
	if len(state.Mailbox) != 1 {
		t.Fatalf("the researcher's only message was duplicated: %+v", state.Mailbox)
	}
	sawBody := 0
	final = ""
	for _, m := range state.Messages {
		if strings.Contains(m.Text, "the flaky test is payment.e2e") {
			sawBody++
		}
		if m.Author == "orchestrator" {
			final = m.Text
		}
	}
	// The report lives in the durable mailbox and the prompts it was drained
	// into — it must never spill into the lead's summary prose.
	if sawBody != 0 {
		t.Fatalf("message body appeared %d times in the transcript, want 0", sawBody)
	}
	if strings.Contains(final, "Peer messages") {
		t.Fatalf("second turn re-delivered the message: %q", final)
	}
}

func readCtx(t *testing.T) (ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}
