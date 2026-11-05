package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInheritedContextRPC(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	// Seed a rule and edit the guide over RPC.
	call(t, c, "1", "context.inherited.rule.save", map[string]any{
		"name": "testing style", "body": "Always prefer table-driven tests.",
	})
	call(t, c, "2", "context.inherited.save", map[string]any{
		"guide": "# Inherited context\n\nReply tersely.",
	})

	var view struct {
		Guide string `json:"guide"`
		Log   string `json:"log"`
		Rules []struct {
			Name string `json:"name"`
			Body string `json:"body"`
		} `json:"rules"`
		Rendered string `json:"rendered"`
	}
	json.Unmarshal(call(t, c, "3", "context.inherited.get", nil), &view)

	if !strings.Contains(view.Guide, "Reply tersely.") {
		t.Fatalf("guide = %q", view.Guide)
	}
	if len(view.Rules) != 1 || view.Rules[0].Name != "testing-style" {
		t.Fatalf("rules = %+v", view.Rules)
	}
	if !strings.Contains(view.Rules[0].Body, "table-driven") {
		t.Fatalf("rule body = %q", view.Rules[0].Body)
	}
	// The rendered form is what gets injected into agent prompts.
	if !strings.Contains(view.Rendered, "table-driven") || !strings.Contains(view.Rendered, "Reply tersely.") {
		t.Fatalf("rendered = %q", view.Rendered)
	}

	// Deleting is the way back out.
	call(t, c, "4", "context.inherited.rule.delete", map[string]any{"name": "testing-style"})
	json.Unmarshal(call(t, c, "5", "context.inherited.get", nil), &view)
	if len(view.Rules) != 0 {
		t.Fatalf("rule survived delete: %+v", view.Rules)
	}
}
