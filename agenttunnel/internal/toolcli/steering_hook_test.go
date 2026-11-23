package toolcli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Pending steering prints the PostToolUse hook output Claude Code reads.
func TestPrintSteeringHookEmitsAdditionalContext(t *testing.T) {
	var code int
	out := captureStdout(t, func() {
		code = printSteeringHook(strings.NewReader(`{"context":"## Steering\n- focus on tests"}`))
	})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var hook struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(bytes.TrimSpace([]byte(out)), &hook); err != nil {
		t.Fatalf("hook output is not JSON: %v\n%s", err, out)
	}
	if hook.HookSpecificOutput.HookEventName != "PostToolUse" || !strings.Contains(hook.HookSpecificOutput.AdditionalContext, "focus on tests") {
		t.Fatalf("hook output = %s", out)
	}
}

// Nothing pending prints nothing, so the hook changes nothing.
func TestPrintSteeringHookSilentWhenNothingPending(t *testing.T) {
	var code int
	out := captureStdout(t, func() {
		code = printSteeringHook(strings.NewReader(`{"context":""}`))
	})
	if code != 0 || out != "" {
		t.Fatalf("exit = %d, out = %q; want silence", code, out)
	}
}
