package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
	"github.com/agenttunnel/agenttunnel-go/internal/usage"
)

// newPromptCaptureHarness mirrors newHarness but delivers each pass prompt as
// the script's first positional argument (PromptVia "arg"), so tests can
// capture exactly what the lead saw. MaxPasses is left unset so the default
// budget of 3 applies.
func newPromptCaptureHarness(t *testing.T, script string) (*Runner, *domain.Engine, context.Context, string) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.SetClock(fixedClock)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script, "sh", spec.Prompt}, PromptVia: "arg"}, nil
	}, providers.JSONLineTranslator{})
	inherited := contextstore.NewInheritedContext(t.TempDir())
	if err := inherited.Ensure(); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(Deps{
		Engine:    eng,
		Registry:  providers.NewRegistry(driver),
		Roles:     contracts.NewRoleRegistry(contracts.BuiltinRoles()...),
		Logs:      logstore.New(t.TempDir()),
		Ledger:    usage.NewLedger(),
		Mailbox:   contextstore.NewMailbox(),
		Inherited: inherited,
		Settings:  settings.New(t.TempDir()),
		Tools:     toolbox.New(),
		Clock:     fixedClock,
		Cfg:       Config{MaxConcurrency: 2},
	})
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	return r, eng, ctx, filepath.Join(t.TempDir(), "prompts.log")
}

type capturedPrompt struct{ thread, pass string }

// parseCapturedPrompts reads a prompt log written by a capture script: each
// section starts with a <<<thread|pass>>> marker line and holds the full pass
// prompt that followed it.
func parseCapturedPrompts(t *testing.T, path string) map[capturedPrompt]string {
	t.Helper()
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[capturedPrompt]string{}
	var key capturedPrompt
	var buf strings.Builder
	flush := func() {
		if key.thread != "" {
			out[key] = buf.String()
		}
	}
	for _, line := range strings.Split(string(blob), "\n") {
		if strings.HasPrefix(line, "<<<") && strings.HasSuffix(line, ">>>") {
			flush()
			parts := strings.SplitN(strings.TrimSuffix(strings.TrimPrefix(line, "<<<"), ">>>"), "|", 2)
			if len(parts) == 2 {
				key = capturedPrompt{parts[0], parts[1]}
				buf.Reset()
				continue
			}
		}
		if key.thread != "" {
			buf.WriteString(line)
			buf.WriteString("\n")
		}
	}
	flush()
	return out
}

// A pending request in one thread must never surface in another thread's lead
// prompt, even though both turns share one Runner and the lead agent id is
// thread-agnostic. Drives real RunTurn calls on both threads and inspects the
// prompts the leads actually received.
func TestRunTurnPromptNeverCarriesAnotherThreadsPendingRequest(t *testing.T) {
	script := `if [ "$AT_ROLE" = orchestrator ]; then
  printf '<<<%s|%s>>>\n%s\n' "$AT_THREAD_ID" "$AT_PASS" "$1" >> "$QA_PROMPT_LOG"
fi
printf '{"type":"text","text":"ok"}\n'`
	r, eng, ctx, logPath := newPromptCaptureHarness(t, script)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QA_PROMPT_LOG", logPath)

	// An executor in thread t1 proposed a child; t1's lead never decided it.
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Name: "inner", Task: "inner work",
	})
	New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)

	if err := New(r).RunTurn(ctx, "t2", "unrelated work"); err != nil {
		t.Fatalf("run turn t2: %v", err)
	}
	prompts := parseCapturedPrompts(t, logPath)
	if _, ok := prompts[capturedPrompt{"t2", "0"}]; !ok {
		t.Fatalf("expected t2's pass 0 prompt to be captured: %+v", prompts)
	}
	for key, body := range prompts {
		if key.thread != "t2" {
			continue
		}
		if strings.Contains(body, "Pending spawn requests") || strings.Contains(body, "inner") {
			t.Fatalf("t2 pass %s prompt leaked t1's pending request:\n%s", key.pass, body)
		}
	}
	// t1's request must have survived t2's full turn untouched.
	if got := r.spawns.list("t1"); len(got) != 1 || got[0].Name != "inner" {
		t.Fatalf("t1 pending after t2's turn = %+v", got)
	}

	// Positive control: t1's own turn must list the request in its first pass
	// prompt, proving the capture channel and the pending section both work.
	if err := New(r).RunTurn(ctx, "t1", "decide the request"); err != nil {
		t.Fatalf("run turn t1: %v", err)
	}
	prompts = parseCapturedPrompts(t, logPath)
	body := prompts[capturedPrompt{"t1", "0"}]
	if !strings.Contains(body, "## Pending spawn requests") || !strings.Contains(body, "inner") {
		t.Fatalf("t1 pass 0 prompt missing its own pending request:\n%s", body)
	}
	// The undecided request is dropped when its turn ends...
	if got := r.spawns.list("t1"); len(got) != 0 {
		t.Fatalf("t1 request outlived its turn: %+v", got)
	}
	// ...but only after earning passes up to the default budget of 3.
	for _, pass := range []string{"0", "1", "2"} {
		if _, ok := prompts[capturedPrompt{"t1", pass}]; !ok {
			t.Fatalf("expected a t1 pass %s prompt while the request stayed undecided: %+v", pass, prompts)
		}
	}
}

// A failed lead pass ends the turn with an error; the deferred cleanup must
// still drop that thread's pending requests while other threads keep theirs.
func TestRunTurnClearsPendingRequestsWhenPassFails(t *testing.T) {
	script := `if [ "$AT_ROLE" = orchestrator ]; then
  printf '{"type":"error","text":"boom"}\n'
  exit 1
fi
printf '{"type":"text","text":"ok"}\n'`
	r, eng, ctx := newHarness(t, script)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2"}); err != nil {
		t.Fatal(err)
	}
	for _, thread := range []contracts.ThreadID{"t1", "t2"} {
		r.tools.Post(thread, toolbox.Call{
			Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
			Name: "inner", Task: "undecided work",
		})
		New(r).toolTasks(context.Background(), thread, "agent-orchestrator", nil, nil)
	}

	if err := New(r).RunTurn(ctx, "t1", "do the thing"); err == nil {
		t.Fatal("expected the failed lead pass to surface an error")
	}
	if got := r.spawns.list("t1"); len(got) != 0 {
		t.Fatalf("t1 request survived a failed turn: %+v", got)
	}
	if got := r.spawns.list("t2"); len(got) != 1 {
		t.Fatalf("clearing t1 must not touch t2: %+v", got)
	}
}

// A reject from anyone but the lead is ignored exactly like a non-lead
// approve: nothing runs, the request stays, and the attempt is audited.
func TestNonLeadRejectIsIgnored(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Name: "inner", Task: "x",
	})
	New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)

	var mu sync.Mutex
	var decisions []map[string]any
	r.audit = func(kind string, fields map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		if kind == "spawn.decision-ignored" {
			decisions = append(decisions, fields)
		}
	}
	r.tools.Post("t1", toolbox.Call{Kind: toolbox.KindReject, AgentID: "agent-outer-2", Name: "inner"})
	if tasks := New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil); len(tasks) != 0 {
		t.Fatalf("a non-lead reject must not run or alter anything: %+v", tasks)
	}
	if got := r.spawns.list("t1"); len(got) != 1 || got[0].Name != "inner" {
		t.Fatalf("the request should survive a non-lead reject: %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(decisions) != 1 {
		t.Fatalf("spawn.decision-ignored entries = %+v", decisions)
	}
	if decisions[0]["kind"] != "reject" || decisions[0]["agentId"] != "agent-outer-2" {
		t.Fatalf("decision audit entry = %+v", decisions[0])
	}
}

// A harness that prints the `tool spawn` command instead of executing it must
// get the same gated loop as one that goes through the binary: the printed
// line routes through the agent's stream scan, pends as a request, surfaces in
// the lead's next pass prompt, and the printed approve runs it parented on the
// proposer.
func TestPrintedSpawnProposalRunsTheGatedLoop(t *testing.T) {
	script := `case "$AT_ROLE" in
orchestrator)
  case "$AT_PASS" in
  0) printf '%s\n' 'tool dispatch --role executor --name outer --task "outer work"' ;;
  1)
    case "$1" in
      *"Pending spawn requests"*"inner"*"inner work"*) ;;
      *) printf '{"type":"error","text":"pass prompt lacked the pending request"}\n'; exit 1 ;;
    esac
    printf '%s\n' 'tool approve --name inner'
    ;;
  esac
  printf '{"type":"text","text":"report-%s"}\n' "$AT_PASS"
  ;;
*)
  printf '%s\n' 'tool spawn --role executor --name inner --task "inner work"'
  printf '{"type":"text","text":"outer findings"}\n'
  ;;
esac`

	r, eng, ctx := newPromptHarness(t, script)
	if err := New(r).RunTurn(ctx, "t1", "please build the feature"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	state, _ := eng.State(ctx, "t1")
	inner := state.Agents["agent-inner-2"]
	if inner.Status != contracts.AgentSucceeded {
		t.Fatalf("inner status = %s, agents = %+v", inner.Status, state.Agents)
	}
	if inner.ParentAgentID != "agent-outer-1" {
		t.Fatalf("inner parent = %q, want the proposer agent-outer-1", inner.ParentAgentID)
	}
	if inner.Task != "inner work" {
		t.Fatalf("approved request not preserved: %+v", inner)
	}
	// The printed proposal line left the executor's findings: the request is
	// the action, not a result.
	shared := state.Shared["result_outer"]
	if strings.Contains(shared, "tool spawn") {
		t.Fatalf("printed spawn line leaked into findings: %q", shared)
	}
}

// A harness that speaks protocol frames gets the same routing: a spawn frame
// from a subagent pends as a request and runs only after the lead approves.
func TestSpawnFrameRunsTheGatedLoop(t *testing.T) {
	script := `case "$AT_ROLE" in
orchestrator)
  case "$AT_PASS" in
  0) printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","taskId":"outer","task":"outer work","role":"executor"}' ;;
  1) printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"approve","taskId":"inner"}' ;;
  esac
  printf '{"type":"text","text":"report-%s"}\n' "$AT_PASS"
  ;;
*)
  printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"spawn","taskId":"inner","task":"inner work","role":"executor"}'
  printf '{"type":"text","text":"outer findings"}\n'
  ;;
esac`

	r, eng, ctx := newPromptHarness(t, script)
	if err := New(r).RunTurn(ctx, "t1", "please build the feature"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	state, _ := eng.State(ctx, "t1")
	// Protocol dispatches mint plain agent-<taskId> ids; the approved request
	// is parented on that id.
	inner := state.Agents["agent-inner-1"]
	if inner.Status != contracts.AgentSucceeded {
		t.Fatalf("inner status = %s, agents = %+v", inner.Status, state.Agents)
	}
	if inner.ParentAgentID != "agent-outer" || inner.Task != "inner work" {
		t.Fatalf("approved frame request not preserved: %+v", inner)
	}
}
