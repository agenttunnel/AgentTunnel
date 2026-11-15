package orchestrator

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// TestLeadRuntimeOverridesOnlyTheNamedLead covers per-lead harnesses: a thread
// can run lead 2 on another harness and model while lead 1 keeps the
// orchestrator role's runtime, and an override that changes the harness does
// not carry the role's model across to it.
func TestLeadRuntimeOverridesOnlyTheNamedLead(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"x"}\n'`)
	role, _ := r.roles.Get(contracts.RoleOrchestrator)
	role.DefaultModel = "role-model"
	role.DefaultHarness = contracts.HarnessOpenCode

	other := contracts.HarnessClaude
	if other == role.DefaultHarness {
		other = contracts.HarnessCodex
	}
	set := func(index int, rt contracts.LeadRuntime) {
		t.Helper()
		if err := eng.Dispatch(ctx, contracts.Command{
			ID: "lr", Type: contracts.CmdLeadRuntimeSet, ThreadID: "t1",
			LeadRuntime: &contracts.LeadRuntimeSet{Index: index, Runtime: rt},
		}); err != nil {
			t.Fatal(err)
		}
	}

	set(1, contracts.LeadRuntime{Harness: other, Model: "lead-two-model"})
	if h, m, _ := r.leadRuntime(ctx, "t1", 0, role); h != role.DefaultHarness || m != "role-model" {
		t.Fatalf("lead 1 should keep the role runtime, got %s/%s", h, m)
	}
	if h, m, _ := r.leadRuntime(ctx, "t1", 1, role); h != other || m != "lead-two-model" {
		t.Fatalf("lead 2 override = %s/%s, want %s/lead-two-model", h, m, other)
	}

	// A harness override without a model must not inherit the role's model.
	// Lead 3 exists only on a three-lead team.
	startLead(t, r, ctx, 3)
	set(2, contracts.LeadRuntime{Harness: other})
	if h, m, _ := r.leadRuntime(ctx, "t1", 2, role); h != other || m != "" {
		t.Fatalf("lead 3 harness-only override = %s/%q, want %s/empty", h, m, other)
	}
}

// TestLeadRuntimeUsesConfiguredOrchestratorRuntime covers the persisted setting:
// the Orchestrator role's configured harness/model runs every lead, and a
// thread's per-lead override still takes precedence over it. Clearing the
// override returns the lead to the configured runtime.
func TestLeadRuntimeUsesConfiguredOrchestratorRuntime(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"x"}\n'`)
	role, _ := r.roles.Get(contracts.RoleOrchestrator)

	pinned := contracts.HarnessClaude
	if pinned == role.DefaultHarness {
		pinned = contracts.HarnessCodex
	}
	r.settings.SetRoleConfig(contracts.RoleOrchestrator, settings.RoleConfig{
		Harness: pinned, Model: "pinned-model", Variant: "high",
	})

	if h, m, v := r.leadRuntime(ctx, "t1", 0, role); h != pinned || m != "pinned-model" || v != "high" {
		t.Fatalf("lead runtime = %s/%s/%s, want %s/pinned-model/high", h, m, v, pinned)
	}

	// A thread-level lead override is the more specific choice.
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "lr", Type: contracts.CmdLeadRuntimeSet, ThreadID: "t1",
		LeadRuntime: &contracts.LeadRuntimeSet{
			Index:   0,
			Runtime: contracts.LeadRuntime{Harness: role.DefaultHarness, Model: "thread-model"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if h, m, _ := r.leadRuntime(ctx, "t1", 0, role); h != role.DefaultHarness || m != "thread-model" {
		t.Fatalf("thread override = %s/%s, want %s/thread-model", h, m, role.DefaultHarness)
	}

	// Clearing the thread override falls back to the configured runtime.
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "lr-clear", Type: contracts.CmdLeadRuntimeSet, ThreadID: "t1",
		LeadRuntime: &contracts.LeadRuntimeSet{Index: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if h, m, v := r.leadRuntime(ctx, "t1", 0, role); h != pinned || m != "pinned-model" || v != "high" {
		t.Fatalf("cleared override = %s/%s/%s, want %s/pinned-model/high", h, m, v, pinned)
	}
}
