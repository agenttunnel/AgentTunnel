package orchestrator

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Two harnesses report the same model name. Approving it on claude must not
// approve it on codex.
func TestAutoApprovalDoesNotCrossHarnesses(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.settings.SetHarnessApprovedModels(contracts.HarnessClaude, []string{"shared-model"})
	r.settings.SetHarnessApprovedModels(contracts.HarnessCodex, []string{"codex-only"})

	if !r.autoAllowed(contracts.HarnessClaude, "shared-model") {
		t.Fatal("claude approved shared-model but autoAllowed said no")
	}
	if r.autoAllowed(contracts.HarnessCodex, "shared-model") {
		t.Fatal("shared-model was approved on claude and leaked to codex")
	}
	if !r.autoAllowed(contracts.HarnessCodex, "codex-only") {
		t.Fatal("codex's own approval was not honoured")
	}
}

// A dropped model request leaves exactly one model.fallback audit line with the
// requested model, the resolved one, and why. Honoured requests leave none.
func TestModelFallbackLeavesAuditLine(t *testing.T) {
	r, _, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	role, _ := r.roles.Get(contracts.RoleExecutor)
	role.DefaultHarness = contracts.HarnessClaude
	role.DefaultModel = "role-default"
	r.settings.SetHarnessApprovedModels(contracts.HarnessClaude, []string{"approved-a"})

	var lines []map[string]any
	r.audit = func(kind string, fields map[string]any) {
		if kind == "model.fallback" {
			lines = append(lines, fields)
		}
	}

	// Auto off, other harness: the harness default runs.
	if got := r.resolveModelFor(ctx, "t1", role, contracts.HarnessCodex, "gpt-x", false, false); got != "" {
		t.Fatalf("cross-harness fallback resolved %q, want harness default", got)
	}
	// Auto on, unapproved model on the role's harness: the role default runs.
	if got := r.resolveModelFor(ctx, "t1", role, contracts.HarnessClaude, "unapproved", true, false); got != "role-default" {
		t.Fatalf("unapproved fallback resolved %q, want role-default", got)
	}
	// Auto on, approved model: honoured, no audit.
	if got := r.resolveModelFor(ctx, "t1", role, contracts.HarnessClaude, "approved-a", true, false); got != "approved-a" {
		t.Fatalf("approved model resolved %q", got)
	}
	// No request at all: nothing to fall back from, no audit.
	r.resolveModelFor(ctx, "t1", role, contracts.HarnessClaude, "", true, false)

	if len(lines) != 2 {
		t.Fatalf("fallback audit lines = %d, want 2: %+v", len(lines), lines)
	}
	first := lines[0]
	if first["requested"] != "gpt-x" || first["resolved"] != "harness default" ||
		first["reason"] != "auto mode off" || first["harness"] != "codex" || first["role"] != "executor" {
		t.Fatalf("first fallback line = %+v", first)
	}
	second := lines[1]
	if second["requested"] != "unapproved" || second["resolved"] != "role-default" ||
		second["reason"] != "model not allowed in auto mode" {
		t.Fatalf("second fallback line = %+v", second)
	}
}
