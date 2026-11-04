package daemon

import (
	"encoding/json"
	"testing"
)

func containsStr(list []string, want string) bool {
	for _, m := range list {
		if m == want {
			return true
		}
	}
	return false
}

func TestModelsAddAppearsInHarnessList(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var added struct {
		Harness string   `json:"harness"`
		Models  []string `json:"models"`
	}
	json.Unmarshal(call(t, c, "1", "models.add", map[string]any{
		"harness": "opencode",
		"model":   "my-provider/model-x",
	}), &added)
	if !containsStr(added.Models, "my-provider/model-x") {
		t.Fatalf("added model missing: %v", added.Models)
	}

	var listed struct {
		Models map[string][]string `json:"models"`
	}
	json.Unmarshal(call(t, c, "2", "harness.list", nil), &listed)
	if !containsStr(listed.Models["opencode"], "my-provider/model-x") {
		t.Fatalf("harness.list missing custom model: %v", listed.Models["opencode"])
	}
}

func TestModelsRefreshWithoutDiscoveryFallsBackToCatalog(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var res struct {
		Harness string   `json:"harness"`
		Models  []string `json:"models"`
		Error   string   `json:"error"`
	}
	json.Unmarshal(call(t, c, "1", "models.refresh", map[string]any{"harness": "claude"}), &res)
	if len(res.Models) == 0 {
		t.Fatalf("expected catalog fallback: %+v", res)
	}
	if res.Error != "" {
		t.Fatalf("unexpected discovery error: %s", res.Error)
	}
}
