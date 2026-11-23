package providers

import (
	"os"
	"strings"
	"testing"
)

// A solo pass on Command Code loads the steering mod with --mod. The mod hooks
// afterToolCall, which runs for failed calls too, and reads the daemon's envelope.
func TestCommandCodeInvocationLoadsSteeringMod(t *testing.T) {
	cs, err := commandCodeInvocation(SessionSpec{Prompt: "fix it", ToolSteering: true})
	if err != nil {
		t.Fatal(err)
	}
	i := indexOf(cs.Args, "--mod")
	if i < 0 || i+1 >= len(cs.Args) {
		t.Fatalf("args = %v, want --mod <file>", cs.Args)
	}
	body, err := os.ReadFile(cs.Args[i+1])
	if err != nil {
		t.Fatalf("mod not written: %v", err)
	}
	for _, want := range []string{`afterToolCall`, `"tool", "steering"`, `additionalContext`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("mod lacks %q", want)
		}
	}
}

func TestCommandCodeInvocationWithoutSteeringLoadsNoMod(t *testing.T) {
	cs, err := commandCodeInvocation(SessionSpec{Prompt: "fix it"})
	if err != nil {
		t.Fatal(err)
	}
	if indexOf(cs.Args, "--mod") >= 0 {
		t.Fatalf("args = %v, want no mod", cs.Args)
	}
}
