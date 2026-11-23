package providers

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A solo pass asks claude to take steering between tool calls: the invocation
// carries a --settings hook that runs the steering command after both tool
// outcomes.
func TestClaudeInvocationWiresSteeringHook(t *testing.T) {
	cs, err := claudeInvocation(SessionSpec{Prompt: "fix it", ToolSteering: true})
	if err != nil {
		t.Fatal(err)
	}
	i := indexOf(cs.Args, "--settings")
	if i < 0 || i+1 >= len(cs.Args) {
		t.Fatalf("args = %v, want --settings <json>", cs.Args)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(cs.Args[i+1]), &settings); err != nil {
		t.Fatalf("settings are not JSON: %v", err)
	}
	for _, event := range []string{"PostToolUse", "PostToolUseFailure"} {
		groups := settings.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s hooks = %+v, want one command", event, groups)
		}
		if got := groups[0].Hooks[0]; got.Type != "command" || got.Command != `"$AT_TOOL" tool steering` {
			t.Fatalf("%s hook = %+v", event, got)
		}
	}
	if cs.Args[len(cs.Args)-1] != "fix it" {
		t.Fatalf("prompt is not the last argument: %v", cs.Args)
	}
}

func TestClaudeInvocationWithoutSteeringHasNoHook(t *testing.T) {
	cs, err := claudeInvocation(SessionSpec{Prompt: "fix it"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "fix it"}; !reflect.DeepEqual(cs.Args, want) {
		t.Fatalf("args = %v, want %v", cs.Args, want)
	}
}

func indexOf(xs []string, want string) int {
	for i, x := range xs {
		if x == want {
			return i
		}
	}
	return -1
}
