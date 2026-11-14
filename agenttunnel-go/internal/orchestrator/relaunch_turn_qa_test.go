package orchestrator

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// The relaunch must not only record a turn on the target: the target's new
// turn has to run, the way the thread.turn.start RPC runs one, or the
// report-back instruction inside the relaunch prompt is never seen by any
// agent and the controller's thread-results read shows nothing new.
func TestRelaunchedThreadRunsItsNewTurn(t *testing.T) {
	var r *Runner
	var mu sync.Mutex
	prompts := map[contracts.ThreadID][]string{}
	var posted sync.Once
	r, eng, ctx := newHarnessInvoking(t, func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		mu.Lock()
		prompts[spec.ThreadID] = append(prompts[spec.ThreadID], spec.Prompt)
		mu.Unlock()
		if spec.ThreadID == "t1" {
			posted.Do(func() {
				r.tools.Post("t1", relaunchCall("t2", "go deeper on the parser"))
			})
		}
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", `printf '{"type":"text","text":"done"}\n'`}, PromptVia: "stdin"}, nil
	})
	createThread(t, eng, ctx, "t2")
	settleTrigger(t, eng, ctx, "t2")

	events, stop := eng.SubscribeAll()
	defer stop()
	if err := New(r).RunTurn(ctx, "t1", "check your dependencies"); err != nil {
		t.Fatalf("turn: %v", err)
	}

	// The relaunch's target turn is asynchronous — the caller's own turn
	// does not wait for it — so its marks are read from the event stream,
	// not from a single state snapshot: the control claim, the
	// controller-authored opener, the lead dispatched to execute the
	// relaunch prompt, and that lead settling.
	var controlled, opener, dispatched, settled, released bool
	var openerText string
	deadline := time.After(20 * time.Second)
waited:
	for !(settled && released) {
		select {
		case <-deadline:
			t.Fatalf("the relaunched thread's turn never ran or never settled: controlled=%v opener=%v dispatched=%v settled=%v released=%v", controlled, opener, dispatched, settled, released)
		case ev := <-events:
			if ev.ThreadID != "t2" {
				continue
			}
			switch p := ev.Payload.(type) {
			case contracts.ThreadControlled:
				if p.Controller == "t1" {
					controlled = true
				}
				if p.Controller == "" {
					released = true
				}
			case contracts.MessageSent:
				if p.Author == "t1" {
					opener = true
					openerText = p.Text
				}
			case contracts.TaskDispatched:
				dispatched = true
			case contracts.AgentStatusChanged:
				if p.Status.Terminal() {
					settled = true
				}
			}
			continue waited
		}
	}
	if !controlled {
		t.Fatalf("the relaunch never claimed control of the target")
	}
	if !opener {
		t.Fatalf("the relaunch never recorded a controller-authored opener")
	}
	if !strings.Contains(openerText, "go deeper on the parser") || !strings.Contains(openerText, "relaunched by thread t1") {
		t.Fatalf("relaunch opener = %q, want the relaunch prompt with the report-back instruction", openerText)
	}
	// The target's new turn ran: its lead was dispatched, and its session
	// was handed the relaunch prompt.
	if !dispatched {
		t.Fatalf("the relaunched thread's turn never dispatched a lead to execute the relaunch prompt")
	}
	mu.Lock()
	got := append([]string(nil), prompts["t2"]...)
	mu.Unlock()
	if len(got) == 0 || !strings.Contains(got[0], "go deeper on the parser") {
		t.Fatalf("relaunched lead prompts = %q, want the relaunch prompt", got)
	}

	// The turn ended, so control was released with it: a claim that outlived
	// its turn would misattribute every later turn on the target to the
	// controller. The permission to relaunch again is untouched — it rides
	// on the caller's TriggeredFrom record.
	st, _ := eng.State(ctx, "t2")
	if st.ControlledBy != "" {
		t.Fatalf("controlledBy = %q after the relaunched turn ended, want control released", st.ControlledBy)
	}
	if st.Turn != 1 {
		t.Fatalf("turn = %d, want the relaunch turn", st.Turn)
	}
}
