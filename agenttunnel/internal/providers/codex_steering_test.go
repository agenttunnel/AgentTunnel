package providers

import (
	"reflect"
	"testing"
)

// A solo pass on codex wires the steering hook as a config override, so no
// file is written to the user's codex home. The trust bypass rides along only
// when this codex has the flag.
func TestCodexSteeringArgsWireHookAsConfigOverride(t *testing.T) {
	want := []string{
		"-c", `hooks.PostToolUse=[{hooks=[{type="command",command='"$AT_TOOL" tool steering',timeout=10}]}]`,
		"--dangerously-bypass-hook-trust",
	}
	if got := codexSteeringArgs(true); !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	if got := codexSteeringArgs(false); !reflect.DeepEqual(got, want[:2]) {
		t.Fatalf("args without trust flag = %v, want %v", got, want[:2])
	}
}
