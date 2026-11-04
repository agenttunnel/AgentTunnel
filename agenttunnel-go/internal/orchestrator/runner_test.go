package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
	"github.com/agenttunnel/agenttunnel-go/internal/usage"
)

func fixedClock() time.Time { return time.Unix(0, 0).UTC() }

// newHarness builds a runner backed by a shell script standing in for a harness
// CLI, so the full dispatch path can be exercised without a real provider.
func newHarness(t *testing.T, script string) (*Runner, *domain.Engine, context.Context) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.SetClock(fixedClock)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script}, PromptVia: "stdin"}, nil
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
		Notebook:  contextstore.NewNotebook(),
		Mailbox:   contextstore.NewMailbox(),
		Inherited: inherited,
		Settings:  settings.New(t.TempDir()),
		Tools:     toolbox.New(),
		Clock:     fixedClock,
		Cfg:       Config{MaxPasses: 2, MaxConcurrency: 2},
	})
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	return r, eng, ctx
}

func TestRunnerRespectsDependenciesAndRecordsUsage(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "a.done")
	script := fmt.Sprintf(`case "$AT_TASK" in
A*) touch %q ;;
B*) [ -f %q ] || { printf '{"type":"error","text":"B ran before A"}\n'; exit 1; } ;;
esac
printf '{"type":"text","text":"done"}\n'
printf '{"usage":{"input_tokens":10,"output_tokens":5}}\n'`, marker, marker)

	r, eng, ctx := newHarness(t, script)
	tasks := []contracts.DispatchTask{
		{AgentID: "agent-a", Name: "a", Role: contracts.RoleExecutor, Task: "A step"},
		{AgentID: "agent-b", Name: "b", Role: contracts.RoleExecutor, Task: "B step", DependsOn: []string{"a"}},
	}
	if err := r.Dispatch(ctx, "t1", tasks); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := r.Run(ctx, "t1", tasks); err != nil {
		t.Fatalf("run: %v", err)
	}

	state, _ := eng.State(ctx, "t1")
	if state.Agents["agent-a"].Status != contracts.AgentSucceeded {
		t.Fatalf("a status = %s", state.Agents["agent-a"].Status)
	}
	if state.Agents["agent-b"].Status != contracts.AgentSucceeded {
		t.Fatalf("b status = %s", state.Agents["agent-b"].Status)
	}
	if got := r.Logs().Entries("agent-a"); len(got) != 2 {
		t.Fatalf("agent-a logs = %d, want 2", len(got))
	}
	au, ok := r.Ledger().Agent("agent-b")
	if !ok || au.Usage.Total() != 15 {
		t.Fatalf("agent-b usage = %+v", au)
	}
	if _, ok := state.Shared["result_b"]; !ok {
		t.Fatalf("shared context missing result_b: %+v", state.Shared)
	}
}

func TestRunnerCancelsTaskWithFailedDependency(t *testing.T) {
	script := `case "$AT_TASK" in
*Cfail*) exit 1 ;;
esac
printf '{"type":"text","text":"ok"}\n'`
	r, eng, ctx := newHarness(t, script)
	tasks := []contracts.DispatchTask{
		{AgentID: "agent-c", Name: "c", Role: contracts.RoleExecutor, Task: "Cfail"},
		{AgentID: "agent-d", Name: "d", Role: contracts.RoleExecutor, Task: "D step", DependsOn: []string{"c"}},
	}
	if err := r.Dispatch(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}

	state, _ := eng.State(ctx, "t1")
	if state.Agents["agent-c"].Status != contracts.AgentFailed {
		t.Fatalf("c status = %s, want failed", state.Agents["agent-c"].Status)
	}
	if state.Agents["agent-d"].Status != contracts.AgentCancelled {
		t.Fatalf("d status = %s, want cancelled", state.Agents["agent-d"].Status)
	}
}

func TestRunnerAutoModeHonoursApprovedModelsOnly(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"x"}\n'`)
	role, _ := r.roles.Get(contracts.RoleExecutor)
	role.DefaultModel = "role-default"

	r.settings.SetAutoMode(true)
	r.settings.SetApprovedModels([]string{"approved-a"})

	if got := r.resolveModel(role, "approved-a", true); got != "approved-a" {
		t.Fatalf("approved model not honoured: %q", got)
	}
	if got := r.resolveModel(role, "unapproved", true); got != "role-default" {
		t.Fatalf("unapproved model should fall back to role default: %q", got)
	}

	r.settings.SetAutoMode(false)
	if got := r.resolveModel(role, "whatever", r.settings.AutoMode()); got != "role-default" {
		t.Fatalf("auto mode off should use role default: %q", got)
	}
}

func TestRunnerHonoursRoleRuntimeOverride(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"x"}\n'`)
	role, _ := r.roles.Get(contracts.RoleExecutor)

	harness, model, _ := r.roleRuntime(role)
	if harness != role.DefaultHarness || model != role.DefaultModel {
		t.Fatalf("unexpected role default: harness=%s model=%s", harness, model)
	}

	r.settings.SetRoleConfig(contracts.RoleExecutor, settings.RoleConfig{Harness: contracts.HarnessClaude, Model: "sonnet"})
	harness, model, _ = r.roleRuntime(role)
	if harness != contracts.HarnessClaude || model != "sonnet" {
		t.Fatalf("override ignored: harness=%s model=%s", harness, model)
	}
	if got := r.resolveModel(role, "", r.settings.AutoMode()); got != "sonnet" {
		t.Fatalf("resolveModel should use the override: %q", got)
	}
}
