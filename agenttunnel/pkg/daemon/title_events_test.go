package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// titleFrame is the pushed thread.event a client sees for a rename.
type titleFrame struct {
	Type    string `json:"type"`
	Payload struct {
		Title string `json:"title"`
	} `json:"payload"`
}

// wireTitleScript shims the harness so the two title generations can be told
// apart: the start call's prompt carries the user message only, the after-cycle
// call also carries the summary. The lead's pass spins until gate exists, which
// the test writes only once the first rename frame has been read — so the turn
// cannot finish before the start-of-thread title has.
func wireTitleScript(gate string) string {
	return fmt.Sprintf(`case "$1" in
*"Name this coding session"*)
  case "$1" in
  *"What the team did"*) printf '{"type":"text","text":"Cycle Aware Title"}\n' ;;
  *) printf '{"type":"text","text":"First Prompt Title"}\n' ;;
  esac
  exit 0 ;;
esac
while [ ! -f %q ]; do :; done
printf '{"type":"text","text":"the first cycle finished"}\n'`, gate)
}

// TestTitleRenameEventsReachSubscribedClients drives a real turn over the wire
// and waits on the pushed thread.event frames: a first thread.renamed from the
// prompt alone while the turn is still running (the lead's pass is parked on
// the gate until it lands), then the turn's reply, then a second thread.renamed
// from the completed cycle's summary.
func TestTitleRenameEventsReachSubscribedClients(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "titled")
	script := wireTitleScript(gate)
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script, "sh", spec.Prompt}, PromptVia: "arg"}, nil
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
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": ""}), &created)
	call(t, c, "2", "thread.subscribe", map[string]any{"threadId": created.ThreadID})

	turnData, _ := json.Marshal(transport.Request{
		ID:     "turn",
		Method: "thread.turn.start",
		Params: mustJSON(t, map[string]any{"threadId": created.ThreadID, "text": "please summarize the repository"}),
	})
	go func() { _ = c.Write(context.Background(), websocket.MessageText, turnData) }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var titles []string
	turnDone := false
	for !turnDone || len(titles) < 2 {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var resp transport.Response
		if json.Unmarshal(msg, &resp) != nil {
			continue
		}
		if resp.Method == "thread.event" {
			var frame titleFrame
			data, _ := json.Marshal(resp.Params)
			if json.Unmarshal(data, &frame) != nil || frame.Type != "thread.renamed" {
				continue
			}
			if len(titles) == 0 {
				// Release the parked lead: the start-of-thread title has
				// landed on a subscribed client while the turn runs.
				if err := os.WriteFile(gate, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			titles = append(titles, frame.Payload.Title)
			continue
		}
		if resp.ID != "turn" {
			continue
		}
		if resp.Error != nil {
			t.Fatalf("turn failed: %s", resp.Error.Message)
		}
		// The gate proves the first rename happened before the turn completed.
		if len(titles) == 0 {
			t.Fatal("turn completed before the thread was titled")
		}
		turnDone = true
	}

	if titles[0] != "First Prompt Title" {
		t.Fatalf("start-of-thread title = %q", titles[0])
	}
	if titles[1] != "Cycle Aware Title" {
		t.Fatalf("after-cycle title = %q", titles[1])
	}

	var state struct {
		Title string `json:"title"`
	}
	json.Unmarshal(call(t, c, "3", "thread.get", map[string]any{"threadId": created.ThreadID}), &state)
	if state.Title != "Cycle Aware Title" {
		t.Fatalf("thread title = %q", state.Title)
	}
}
