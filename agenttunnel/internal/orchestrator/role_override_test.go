package orchestrator

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// newRoleRuntimeRunner wires the minimum a role-runtime resolution needs: an
// engine to read thread snapshots from and a settings store for the lower layer.
func newRoleRuntimeRunner(t *testing.T) (*Runner, *domain.Engine) {
	t.Helper()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(context.Background())
	t.Cleanup(eng.Close)
	store, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"), t.TempDir())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	return NewRunner(Deps{
		Engine:   eng,
		Settings: store,
		Roles:    contracts.NewRoleRegistry(contracts.BuiltinRoles()...),
	}), eng
}

func setRoleOverride(t *testing.T, eng *domain.Engine, threadID contracts.ThreadID, role contracts.RoleID, rt contracts.LeadRuntime) {
	t.Helper()
	if err := eng.Dispatch(context.Background(), contracts.Command{
		Type: contracts.CmdRoleRuntimeSet, ThreadID: threadID,
		RoleRuntime: &contracts.RoleRuntimeSet{Role: role, Runtime: rt},
	}); err != nil {
		t.Fatalf("set role runtime: %v", err)
	}
}

// The thread snapshot wins over settings, and empty override fields inherit the
// lower layer. Two threads resolve independently, with no shared mutable map.
func TestRoleRuntimeThreadOverridePrecedence(t *testing.T) {
	ctx := context.Background()
	r, eng := newRoleRuntimeRunner(t)
	for _, id := range []contracts.ThreadID{"t1", "t2"} {
		if err := eng.Dispatch(ctx, contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: id}); err != nil {
			t.Fatal(err)
		}
	}
	r.settings.SetRoleConfig(contracts.RoleExecutor, settings.RoleConfig{
		Harness: contracts.HarnessClaude, Model: "sonnet", Variant: "v1",
	})
	executor, ok := r.roles.Get(contracts.RoleExecutor)
	if !ok {
		t.Fatal("executor role missing")
	}

	harness, model, variant := r.roleRuntimeFor(ctx, "t1", executor)
	if harness != contracts.HarnessClaude || model != "sonnet" || variant != "v1" {
		t.Fatalf("settings layer = %s/%s/%s", harness, model, variant)
	}

	// A partial override: the model wins, the harness and variant inherit.
	setRoleOverride(t, eng, "t1", contracts.RoleExecutor, contracts.LeadRuntime{Model: "thread-model"})
	harness, model, variant = r.roleRuntimeFor(ctx, "t1", executor)
	if harness != contracts.HarnessClaude || model != "thread-model" || variant != "v1" {
		t.Fatalf("thread override = %s/%s/%s, want claude/thread-model/v1", harness, model, variant)
	}

	// The other thread still reads settings; a snapshot from before a change
	// keeps its value.
	if _, model, _ := r.roleRuntimeFor(ctx, "t2", executor); model != "sonnet" {
		t.Fatalf("t2 model = %q, want sonnet", model)
	}
	snap, _ := eng.State(ctx, "t1")
	setRoleOverride(t, eng, "t1", contracts.RoleExecutor, contracts.LeadRuntime{Model: "later"})
	if got, _ := snap.RoleRuntimeFor(contracts.RoleExecutor); got.Model != "thread-model" {
		t.Fatalf("old snapshot model = %q, want thread-model", got.Model)
	}

	// The dispatch record's harness follows the thread override too.
	setRoleOverride(t, eng, "t1", contracts.RoleExecutor, contracts.LeadRuntime{Harness: contracts.HarnessCodex})
	if got := r.dispatchHarness(ctx, "t1", contracts.RoleExecutor); got != contracts.HarnessCodex {
		t.Fatalf("dispatch harness = %s, want codex", got)
	}
}

// A thread override that names only a harness must not inherit the settings
// model of the harness it replaced: sending "sonnet" to codex is the bug this
// pins. The new harness runs its own default unless the override names a model.
func TestRoleRuntimeHarnessOnlyOverrideDropsModel(t *testing.T) {
	ctx := context.Background()
	r, eng := newRoleRuntimeRunner(t)
	if err := eng.Dispatch(ctx, contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	r.settings.SetRoleConfig(contracts.RoleExecutor, settings.RoleConfig{
		Harness: contracts.HarnessClaude, Model: "sonnet", Variant: "v1",
	})
	executor, ok := r.roles.Get(contracts.RoleExecutor)
	if !ok {
		t.Fatal("executor role missing")
	}

	setRoleOverride(t, eng, "t1", contracts.RoleExecutor, contracts.LeadRuntime{Harness: contracts.HarnessCodex})
	harness, model, variant := r.roleRuntimeFor(ctx, "t1", executor)
	if harness != contracts.HarnessCodex || model != "" || variant != "" {
		t.Fatalf("harness-only override = %s/%q/%q, want codex/empty/empty", harness, model, variant)
	}
	// resolveModelFor must not fall back to the settings model either.
	if got := r.resolveModelFor(ctx, "t1", executor, contracts.HarnessCodex, "", false, false); got != "" {
		t.Fatalf("resolveModelFor = %q, want empty for the new harness", got)
	}
	// The dispatch record follows the same resolution.
	if got := r.dispatchHarness(ctx, "t1", contracts.RoleExecutor); got != contracts.HarnessCodex {
		t.Fatalf("dispatch harness = %s, want codex", got)
	}

	// An explicit model in the same override is used; it belongs to the new harness.
	setRoleOverride(t, eng, "t1", contracts.RoleExecutor, contracts.LeadRuntime{
		Harness: contracts.HarnessCodex, Model: "gpt-5",
	})
	if h, m, v := r.roleRuntimeFor(ctx, "t1", executor); h != contracts.HarnessCodex || m != "gpt-5" || v != "" {
		t.Fatalf("harness+model override = %s/%q/%q, want codex/gpt-5/empty", h, m, v)
	}
}

func TestResolveModelUsesThreadRoleOverride(t *testing.T) {
	ctx := context.Background()
	r, eng := newRoleRuntimeRunner(t)
	if err := eng.Dispatch(ctx, contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	executor, ok := r.roles.Get(contracts.RoleExecutor)
	if !ok {
		t.Fatal("executor role missing")
	}

	// No request: the role's thread-scoped model is the answer.
	setRoleOverride(t, eng, "t1", contracts.RoleExecutor, contracts.LeadRuntime{Model: "pinned"})
	if got := r.resolveModelFor(ctx, "t1", executor, contracts.HarnessOpenCode, "", false, false); got != "pinned" {
		t.Fatalf("resolveModelFor = %q, want pinned", got)
	}
	// A different thread does not inherit it.
	if err := eng.Dispatch(ctx, contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: "t2"}); err != nil {
		t.Fatal(err)
	}
	if got := r.resolveModelFor(ctx, "t2", executor, contracts.HarnessOpenCode, "", false, false); got != "" {
		t.Fatalf("t2 resolveModelFor = %q, want empty", got)
	}
}
