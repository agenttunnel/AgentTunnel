package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// postAgentToolCall posts one /tool/call body shaped exactly like the tool
// binary's scheduling-tool calls, from the loopback peer an agent's own
// machine always is. It is the mid-turn path a real
// `"$AT_TOOL" tool thread-relaunch --thread T --text ...` takes.
func postAgentToolCall(t *testing.T, baseURL, threadID, kind, target, text string) *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"threadId": threadID,
		"agentId":  "agent-orchestrator",
		"kind":     kind,
		"thread":   target,
		"text":     text,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/tool/call", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("tool %s on %s: %v", kind, threadID, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// waitOutcome waits bounded for an event matching pred. Events already read
// by earlier waits were recorded, so those are scanned first; the outcome
// helpers answer whether the event arrived so the story below can name the
// leg that broke instead of dying on a blind timeout.
func waitOutcome(t *testing.T, w *daemonEventLog, desc string, pred func(contracts.Event) bool) (contracts.Event, bool) {
	t.Helper()
	w.mu.Lock()
	for _, ev := range w.read {
		if pred(ev) {
			w.mu.Unlock()
			return ev, true
		}
	}
	w.mu.Unlock()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-deadline:
			return contracts.Event{}, false
		case ev := <-w.ch:
			w.mu.Lock()
			w.read = append(w.read, ev)
			w.mu.Unlock()
			if pred(ev) {
				return ev, true
			}
		}
	}
}

// waitForHarnessPrompt blocks until the thread's harness prompt has been
// recorded (the driver's invocation races the dispatch event it follows).
// Timeouts bound failure only.
func waitForHarnessPrompt(t *testing.T, f *scheduleDaemonFixture, id contracts.ThreadID) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		f.promptsMu.Lock()
		prompts := append([]string(nil), f.prompts[id]...)
		f.promptsMu.Unlock()
		if len(prompts) > 0 {
			return prompts[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no harness prompt was recorded for %s", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestScheduleStoryEndToEnd walks the thread-scheduling user story over the
// real RPC and tool surfaces: schedule A on B+C, B and C finish scripted
// turns, A is triggered with the deps' results embedded in its prompt, A's
// agent reads a dependency through the thread-results tool and relaunches the
// other through the thread-relaunch tool, and the relaunched thread shows the
// controller and starts its new turn.
func TestScheduleStoryEndToEnd(t *testing.T) {
	f := newScheduleDaemon(t, t.TempDir())
	c, baseURL := dialWS(t, f.Daemon)
	watch := watchEngine(f.Daemon)
	t.Cleanup(watch.close)

	a := createThread(t, c, "Alpha")
	b := createThread(t, c, "Bravo")
	dep := createThread(t, c, "Charlie")

	// Story step 2: B and C each run their own scripted turn to the end.
	call(t, c, "tb", "thread.turn.start", map[string]any{"threadId": b, "text": "build bravo"})
	watch.wait(t, "bravo turn end", waitForTurnEnd(b))
	call(t, c, "tc", "thread.turn.start", map[string]any{"threadId": dep, "text": "build charlie"})
	watch.wait(t, "charlie turn end", waitForTurnEnd(dep))

	// Story step 1: schedule A on B+C through the thread.schedule RPC. A's
	// lead parks mid-turn so the test can interleave the tool calls a real
	// agent would run from inside its pass.
	f.park(a)
	call(t, c, "sched", "thread.schedule", map[string]any{
		"threadId": a, "dependsOn": []contracts.ThreadID{b, dep}, "prompt": "integrate everything",
	})

	// Story step 3: both deps are finished, so A fires immediately. Its
	// prompt carries the scheduled prompt, the compiled RESULTS of both
	// deps, and the offer of the scheduling tools.
	watch.wait(t, "alpha trigger", waitForTrigger(a))
	watch.wait(t, "alpha lead dispatched", func(ev contracts.Event) bool {
		_, ok := ev.Payload.(contracts.TaskDispatched)
		return ok && ev.ThreadID == a
	})
	prompt := waitForHarnessPrompt(t, f, a)
	for _, want := range []string{
		"integrate everything", "RESULTS",
		"report-" + string(b), "report-" + string(dep),
		"tool thread-results --thread", "tool thread-relaunch --thread",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("story step 3: triggered prompt missing %q", want)
		}
	}

	// Story step 5: A's agent reads Charlie's results through the real tool
	// surface. Story step 4: it relaunches Bravo with a new prompt.
	if resp := postAgentToolCall(t, baseURL, string(a), "thread-results", string(dep), ""); resp.StatusCode != http.StatusOK {
		t.Errorf("story step 5: thread-results tool call = %s", resp.Status)
	}
	if resp := postAgentToolCall(t, baseURL, string(a), "thread-relaunch", string(b), "go deeper on the parser"); resp.StatusCode != http.StatusOK {
		t.Errorf("story step 4: thread-relaunch tool call = %s", resp.Status)
	}

	f.release(a)
	watch.wait(t, "alpha turn end", waitForTurnEnd(a))

	// Whatever the pass-boundary drain concluded reached A's log as agent
	// output: the results answer or a refusal naming why.
	resultsOut, ok := waitOutcome(t, watch, "thread-results outcome", func(ev contracts.Event) bool {
		p, is := ev.Payload.(contracts.AgentOutput)
		if !is || ev.ThreadID != a {
			return false
		}
		return strings.HasPrefix(p.Text, "thread "+string(dep)) || strings.HasPrefix(p.Text, "tool thread-results failed")
	})
	if !ok {
		t.Errorf("story step 5: the thread-results answer never reached Alpha's log")
	} else if p := resultsOut.Payload.(contracts.AgentOutput); strings.HasPrefix(p.Text, "tool thread-results failed") {
		t.Errorf("story step 5: thread-results was refused: %s", p.Text)
	}

	relaunchOut, ok := waitOutcome(t, watch, "thread-relaunch outcome", func(ev contracts.Event) bool {
		p, is := ev.Payload.(contracts.AgentOutput)
		if !is || ev.ThreadID != a {
			return false
		}
		return strings.HasPrefix(p.Text, "→ relaunched") || strings.HasPrefix(p.Text, "tool thread-relaunch failed")
	})
	if !ok {
		t.Errorf("story step 4: the thread-relaunch call never produced an outcome in Alpha's log")
	} else if p := relaunchOut.Payload.(contracts.AgentOutput); strings.HasPrefix(p.Text, "tool thread-relaunch failed") {
		t.Errorf("story step 4: thread-relaunch was refused: %s", p.Text)
	}

	// Story step 4: B shows the controller's claim — Alpha relaunched it — and
	// a new turn was opened with the new prompt. Control is scoped to the
	// relaunched turn: the claim is observed on the event stream, and it is
	// released once that turn has ended.
	_, controlled := waitOutcome(t, watch, "bravo control claim", func(ev contracts.Event) bool {
		p, is := ev.Payload.(contracts.ThreadControlled)
		return is && ev.ThreadID == b && p.Controller == a
	})
	if !controlled {
		t.Errorf("story step 4: bravo never showed ControlledBy=%s", a)
	}
	st, err := f.Engine.State(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	var opener bool
	for _, m := range st.Messages {
		if m.Author == string(a) && strings.Contains(m.Text, "go deeper on the parser") {
			opener = true
		}
	}
	if !opener {
		t.Errorf("story step 4: bravo has no controller-authored opener carrying the relaunch prompt: %+v", st.Messages)
	}

	if controlled && opener {
		// Story step 4's second half: the relaunched turn must actually run —
		// bravo's lead is dispatched again and its second harness prompt
		// carries the relaunch's prompt. History already holds bravo's first
		// dispatch, so the wait is for the second one.
		isDispatch := func(ev contracts.Event) bool {
			_, is := ev.Payload.(contracts.TaskDispatched)
			return is && ev.ThreadID == b
		}
		watch.mu.Lock()
		dispatches := 0
		for _, ev := range watch.read {
			if isDispatch(ev) {
				dispatches++
			}
		}
		watch.mu.Unlock()
		deadline := time.After(20 * time.Second)
		for dispatches < 2 {
			select {
			case <-deadline:
				t.Errorf("story step 4: bravo's relaunched turn never ran — the opener was recorded but no lead was dispatched to execute it")
				return
			case ev := <-watch.ch:
				watch.mu.Lock()
				watch.read = append(watch.read, ev)
				watch.mu.Unlock()
				if isDispatch(ev) {
					dispatches++
				}
			}
		}
		watch.wait(t, "bravo turn 2 end", waitForTurnEnd(b))
		f.promptsMu.Lock()
		prompts := append([]string(nil), f.prompts[b]...)
		f.promptsMu.Unlock()
		if len(prompts) != 2 || !strings.Contains(prompts[1], "go deeper on the parser") {
			t.Errorf("story step 4: bravo prompts = %q, want the original turn plus the relaunch prompt", prompts)
		}
		// The relaunched turn ended, so the control it was run under was
		// released with it — control that outlived its turn would misattribute
		// every later turn on bravo to alpha.
		if _, released := waitOutcome(t, watch, "bravo control release", func(ev contracts.Event) bool {
			p, is := ev.Payload.(contracts.ThreadControlled)
			return is && ev.ThreadID == b && p.Controller == ""
		}); !released {
			t.Errorf("story step 4: bravo's relaunch control was never released after its turn ended")
		}
	}
}
