package orchestrator

import (
	"context"
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

func autoModePtr(v bool) *bool { return &v }

// TestThreadAutoModeDrivesModelResolution covers the composer's per-thread auto
// mode toggle: with the environment default off, flipping the thread on lets
// the orchestrator's requested (approved) model win.
func TestThreadAutoModeDrivesModelResolution(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"x"}\n'`)
	role, _ := r.roles.Get(contracts.RoleExecutor)
	role.DefaultModel = "role-default"
	r.settings.SetApprovedModels([]string{"approved-a"})

	if r.autoModeFor(ctx, "t1") {
		t.Fatal("autoModeFor should be false with everything off")
	}
	if got := r.resolveModel(role, contracts.HarnessOpenCode, "approved-a", r.autoModeFor(ctx, "t1"), false); got != "role-default" {
		t.Fatalf("auto off should use role default: %q", got)
	}

	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "m1", Type: contracts.CmdAutoModeSet, ThreadID: "t1", AutoMode: autoModePtr(true),
	}); err != nil {
		t.Fatal(err)
	}
	if !r.autoModeFor(ctx, "t1") {
		t.Fatal("autoModeFor should be true after the thread toggle")
	}
	if got := r.resolveModel(role, contracts.HarnessOpenCode, "approved-a", r.autoModeFor(ctx, "t1"), false); got != "approved-a" {
		t.Fatalf("thread auto mode should honour the approved request: %q", got)
	}
	if got := r.resolveModel(role, contracts.HarnessOpenCode, "unapproved", r.autoModeFor(ctx, "t1"), false); got != "role-default" {
		t.Fatalf("unapproved request should fall back: %q", got)
	}
}

// autoHarnessRunner builds a runner with a driver per harness that records
// which harness each agent actually spawned with.
func autoHarnessRunner(t *testing.T, seen *sync.Map) (*Runner, context.Context) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.SetClock(fixedClock)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	recorder := func(h contracts.Harness) providers.Driver {
		return providers.NewGenericDriver(h, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
			seen.Store(string(spec.AgentID), string(spec.Harness))
			return providers.CommandSpec{
				Binary: "sh", Args: []string{"-c", `printf '{"type":"text","text":"x"}\n'`},
				PromptVia: "stdin",
			}, nil
		}, providers.JSONLineTranslator{})
	}
	inherited := contextstore.NewInheritedContext(t.TempDir())
	if err := inherited.Ensure(); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(Deps{
		Engine:    eng,
		Registry:  providers.NewRegistry(recorder(contracts.HarnessOpenCode), recorder(contracts.HarnessClaude)),
		Roles:     contracts.NewRoleRegistry(contracts.BuiltinRoles()...),
		Logs:      logstore.New(t.TempDir()),
		Ledger:    usage.NewLedger(),
		Notebook:  contextstore.NewNotebook(),
		Mailbox:   contextstore.NewMailbox(),
		Inherited: inherited,
		Settings:  settings.New(t.TempDir()),
		Tools:     toolbox.New(),
		Clock:     fixedClock,
		Cfg:       Config{MaxPasses: 2, MaxConcurrency: 2},
	})
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "c", Type: contracts.CmdThreadCreate, ThreadID: "t1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "m", Type: contracts.CmdAutoModeSet, ThreadID: "t1", AutoMode: autoModePtr(true),
	}); err != nil {
		t.Fatal(err)
	}
	return r, ctx
}

func runOneTask(t *testing.T, r *Runner, ctx context.Context, task contracts.DispatchTask) {
	t.Helper()
	task.AgentID = "agent-h1"
	if err := r.Dispatch(ctx, "t1", []contracts.DispatchTask{task}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := r.Run(ctx, "t1", []contracts.DispatchTask{task}); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// TestAutoModeHarnessOverride lets the orchestrator pick the harness per task
// in auto mode; a disabled harness falls back to the role's configured one.
func TestAutoModeHarnessOverride(t *testing.T) {
	var seen sync.Map
	r, ctx := autoHarnessRunner(t, &seen)

	runOneTask(t, r, ctx, contracts.DispatchTask{
		Name: "h", Role: contracts.RoleExecutor, Task: "do it",
		Harness: contracts.HarnessClaude,
	})
	v, _ := seen.Load("agent-h1")
	if v != string(contracts.HarnessClaude) {
		t.Fatalf("spawned harness = %v, want claude", v)
	}

	// Disabled harness: back to the role's configured (opencode) default.
	r.settings.SetHarnessEnabled(contracts.HarnessClaude, false)
	seen.Clear()
	runOneTask(t, r, ctx, contracts.DispatchTask{
		Name: "h2", Role: contracts.RoleExecutor, Task: "do it",
		Harness: contracts.HarnessClaude,
	})
	v, _ = seen.Load("agent-h1")
	if v != string(contracts.HarnessOpenCode) {
		t.Fatalf("disabled harness should fall back to opencode, got %v", v)
	}
}

// TestHarnessOverrideIgnoredWithoutAutoMode keeps a requested harness from
// hijacking the runtime when auto mode is off.
func TestHarnessOverrideIgnoredWithoutAutoMode(t *testing.T) {
	var seen sync.Map
	r, ctx := autoHarnessRunner(t, &seen)
	if err := r.engine.Dispatch(ctx, contracts.Command{
		ID: "m2", Type: contracts.CmdAutoModeSet, ThreadID: "t1", AutoMode: autoModePtr(false),
	}); err != nil {
		t.Fatal(err)
	}
	runOneTask(t, r, ctx, contracts.DispatchTask{
		Name: "h", Role: contracts.RoleExecutor, Task: "do it",
		Harness: contracts.HarnessClaude,
	})
	v, _ := seen.Load("agent-h1")
	if v != string(contracts.HarnessOpenCode) {
		t.Fatalf("auto off should use role default opencode, got %v", v)
	}
}
