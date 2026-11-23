package providers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A solo pass on opencode loads the steering plugin by directory through
// OPENCODE_CONFIG_CONTENT. The plugin drains steering only after a completed
// call, and it reads the daemon's hook envelope like the other glue does.
func TestOpenCodeInvocationLoadsSteeringPlugin(t *testing.T) {
	cs, err := openCodeInvocation(SessionSpec{Prompt: "fix it", ToolSteering: true})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	for _, kv := range cs.Env {
		if rest, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG_CONTENT="); ok {
			content = rest
		}
	}
	if content == "" {
		t.Fatalf("env = %v, want OPENCODE_CONFIG_CONTENT", cs.Env)
	}
	var cfg struct {
		Plugins []string `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(content), &cfg); err != nil || len(cfg.Plugins) != 1 {
		t.Fatalf("config = %q, want one plugin directory (%v)", content, err)
	}
	body, err := os.ReadFile(filepath.Join(cfg.Plugins[0], "index.js"))
	if err != nil {
		t.Fatalf("plugin not written: %v", err)
	}
	for _, want := range []string{`"execute.after"`, `event.status !== "completed"`, `"tool", "steering"`, "hookSpecificOutput"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("plugin lacks %q", want)
		}
	}
}

func TestOpenCodeInvocationWithoutSteeringLoadsNoPlugin(t *testing.T) {
	cs, err := openCodeInvocation(SessionSpec{Prompt: "fix it"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Env) != 0 {
		t.Fatalf("env = %v, want none", cs.Env)
	}
}
