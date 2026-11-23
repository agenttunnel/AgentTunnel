package daemon

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// approvedView reads the approval fields of a settings.get reply.
type approvedView struct {
	ApprovedModels    []string            `json:"approvedModels"`
	ApprovedByHarness map[string][]string `json:"approvedByHarness"`
}

func readApproved(t *testing.T, raw json.RawMessage) approvedView {
	t.Helper()
	var v approvedView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode settings view: %v", err)
	}
	return v
}

// Two harnesses reporting the same model name keep separate allowlists over
// the wire: approving it for claude must not approve it for codex.
func TestApprovedByHarnessKeepsSharedModelNamesSeparate(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	call(t, c, "1", "settings.set", map[string]any{
		"approvedByHarness": map[string][]string{"claude": {"shared-model"}},
	})
	v := readApproved(t, call(t, c, "2", "settings.get", nil))
	if !slices.Equal(v.ApprovedByHarness["claude"], []string{"shared-model"}) {
		t.Fatalf("claude = %v, want [shared-model]", v.ApprovedByHarness["claude"])
	}
	if got := v.ApprovedByHarness["codex"]; len(got) != 0 {
		t.Fatalf("codex = %v, want none", got)
	}
	if !slices.Equal(v.ApprovedModels, []string{"shared-model"}) {
		t.Fatalf("union = %v, want [shared-model]", v.ApprovedModels)
	}

	// Approving the same name for codex must leave claude's list alone.
	call(t, c, "3", "settings.set", map[string]any{
		"approvedByHarness": map[string][]string{"codex": {"shared-model"}},
	})
	v = readApproved(t, call(t, c, "4", "settings.get", nil))
	if !slices.Equal(v.ApprovedByHarness["claude"], []string{"shared-model"}) {
		t.Fatalf("claude after codex write = %v", v.ApprovedByHarness["claude"])
	}
	if !slices.Equal(v.ApprovedByHarness["codex"], []string{"shared-model"}) {
		t.Fatalf("codex = %v, want [shared-model]", v.ApprovedByHarness["codex"])
	}

	// Clearing claude with an empty list leaves codex approved.
	call(t, c, "5", "settings.set", map[string]any{
		"approvedByHarness": map[string][]string{"claude": {}},
	})
	v = readApproved(t, call(t, c, "6", "settings.get", nil))
	if len(v.ApprovedByHarness["claude"]) != 0 {
		t.Fatalf("claude = %v after clear, want none", v.ApprovedByHarness["claude"])
	}
	if !slices.Equal(v.ApprovedByHarness["codex"], []string{"shared-model"}) {
		t.Fatalf("codex lost its approval on claude clear: %v", v.ApprovedByHarness["codex"])
	}
}

// Clients that predate approvedByHarness send the flat approvedModels list,
// which applies to every harness.
func TestLegacyFlatApprovedModelsAppliesToEveryHarness(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	call(t, c, "1", "settings.set", map[string]any{
		"approvedModels": []string{"flat-model"},
	})
	v := readApproved(t, call(t, c, "2", "settings.get", nil))
	for _, h := range []string{"claude", "codex"} {
		if !slices.Equal(v.ApprovedByHarness[h], []string{"flat-model"}) {
			t.Fatalf("%s = %v, want [flat-model]", h, v.ApprovedByHarness[h])
		}
	}
	if !slices.Equal(v.ApprovedModels, []string{"flat-model"}) {
		t.Fatalf("union = %v, want [flat-model]", v.ApprovedModels)
	}
}

// An unknown harness key refuses the whole patch, so the other fields in the
// same request must not be applied.
func TestApprovedByHarnessRejectsUnknownHarness(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	resp := callRaw(t, c, "1", "settings.set", map[string]any{
		"autoMode":          true,
		"approvedByHarness": map[string][]string{"no-such-harness": {"m"}},
	})
	if resp.Error == nil {
		t.Fatal("unknown harness accepted, want error")
	}
	if !strings.Contains(resp.Error.Message, "unknown harness") {
		t.Fatalf("error = %q, want unknown harness", resp.Error.Message)
	}

	var view struct {
		AutoMode bool `json:"autoMode"`
	}
	if err := json.Unmarshal(call(t, c, "2", "settings.get", nil), &view); err != nil {
		t.Fatal(err)
	}
	if view.AutoMode {
		t.Fatal("autoMode applied from a refused patch")
	}
}
