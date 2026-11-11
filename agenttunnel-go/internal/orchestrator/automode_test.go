package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/models"
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
// which harness each agent actually spawned with. The model and variant land
// under "<agent>/model" and "<agent>/variant" in the same map.
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
			seen.Store(string(spec.AgentID)+"/model", spec.Model)
			seen.Store(string(spec.AgentID)+"/variant", spec.Variant)
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

// TestRuntimeChoicesListsEnabledHarnesses pins what the orchestrator's runtime
// menu looks like in auto mode: the enabled harnesses.
func TestRuntimeChoicesListsEnabledHarnesses(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"x"}\n'`)

	choices := r.runtimeChoices()
	if !strings.Contains(choices, "- opencode\n") {
		t.Fatalf("choices missing opencode: %q", choices)
	}
	if !strings.Contains(choices, "- claude\n") {
		t.Fatalf("choices missing claude: %q", choices)
	}

	r.settings.SetHarnessEnabled(contracts.HarnessClaude, false)
	choices = r.runtimeChoices()
	if strings.Contains(choices, "- claude") {
		t.Fatalf("disabled harness still offered: %q", choices)
	}
}

// TestRuntimeChoicesListsEveryModel checks that a harness with more models than
// the old prompt cap still offers each one, so the orchestrator can pick any.
func TestRuntimeChoicesListsEveryModel(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"x"}\n'`)
	var list []string
	for i := range 12 {
		list = append(list, fmt.Sprintf("model-%02d", i))
	}
	catalog := models.Default()
	catalog.Set(contracts.HarnessOpenCode, list)
	r.models = models.NewService(catalog, nil, r.settings)

	choices := r.runtimeChoices()
	for _, m := range list {
		if !strings.Contains(choices, m) {
			t.Fatalf("choices missing %s: %q", m, choices)
		}
	}
	if strings.Contains(choices, "…") {
		t.Fatalf("choices still truncated: %q", choices)
	}
}

// assertSpawn checks the harness, model, and variant one agent spawned with, as
// recorded by autoHarnessRunner.
func assertSpawn(t *testing.T, seen *sync.Map, agent string, harness contracts.Harness, model, variant string) {
	t.Helper()
	h, _ := seen.Load(agent)
	m, _ := seen.Load(agent + "/model")
	v, _ := seen.Load(agent + "/variant")
	if h != string(harness) || m != model || v != variant {
		t.Fatalf("spawn = (%v, %q, %q), want (%s, %q, %q)", h, m, v, harness, model, variant)
	}
}

// TestAutoModeRoleDefaultNotCarriedAcrossHarness: when auto mode moves a task
// to another harness, the role's model and variant belong to its own harness
// and must not follow. With no requested model the other harness runs its
// default.
func TestAutoModeRoleDefaultNotCarriedAcrossHarness(t *testing.T) {
	var seen sync.Map
	r, ctx := autoHarnessRunner(t, &seen)
	r.settings.SetRoleConfig(contracts.RoleExecutor, settings.RoleConfig{Model: "role-model", Variant: "role-variant"})

	runOneTask(t, r, ctx, contracts.DispatchTask{
		Name: "h", Role: contracts.RoleExecutor, Task: "do it",
		Harness: contracts.HarnessClaude,
	})
	assertSpawn(t, &seen, "agent-h1", contracts.HarnessClaude, "", "")
}

// TestAutoModeRequestedModelWinsOverRoleDefault: in auto mode a requested model
// the chosen harness allows replaces the role's configured model, on the role's
// own harness and on another one.
func TestAutoModeRequestedModelWinsOverRoleDefault(t *testing.T) {
	var seen sync.Map
	r, ctx := autoHarnessRunner(t, &seen)
	r.settings.SetRoleConfig(contracts.RoleExecutor, settings.RoleConfig{Model: "role-model"})

	runOneTask(t, r, ctx, contracts.DispatchTask{
		Name: "h", Role: contracts.RoleExecutor, Task: "do it",
		Model: "picked-model",
	})
	assertSpawn(t, &seen, "agent-h1", contracts.HarnessOpenCode, "picked-model", "")

	seen.Clear()
	runOneTask(t, r, ctx, contracts.DispatchTask{
		Name: "h2", Role: contracts.RoleExecutor, Task: "do it",
		Harness: contracts.HarnessClaude, Model: "claude-picked",
	})
	assertSpawn(t, &seen, "agent-h1", contracts.HarnessClaude, "claude-picked", "")
}

// TestRoleDefaultAppliesWithoutAutoMode: with auto mode off a requested model
// is ignored and the role's configured model runs.
func TestRoleDefaultAppliesWithoutAutoMode(t *testing.T) {
	var seen sync.Map
	r, ctx := autoHarnessRunner(t, &seen)
	r.settings.SetRoleConfig(contracts.RoleExecutor, settings.RoleConfig{Model: "role-model"})
	if err := r.engine.Dispatch(ctx, contracts.Command{
		ID: "m3", Type: contracts.CmdAutoModeSet, ThreadID: "t1", AutoMode: autoModePtr(false),
	}); err != nil {
		t.Fatal(err)
	}

	runOneTask(t, r, ctx, contracts.DispatchTask{
		Name: "h", Role: contracts.RoleExecutor, Task: "do it",
		Model: "picked-model",
	})
	assertSpawn(t, &seen, "agent-h1", contracts.HarnessOpenCode, "role-model", "")
}
