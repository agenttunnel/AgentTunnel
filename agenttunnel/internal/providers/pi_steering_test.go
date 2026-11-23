package providers

import (
	"os"
	"strings"
	"testing"
)

// A solo pass on pi loads the steering extension with -e, and the extension
// reads the daemon's hook envelope rather than its own answer format.
func TestPiInvocationLoadsSteeringExtension(t *testing.T) {
	cs, err := piInvocation(SessionSpec{Prompt: "fix it", ToolSteering: true})
	if err != nil {
		t.Fatal(err)
	}
	i := indexOf(cs.Args, "-e")
	if i < 0 || i+1 >= len(cs.Args) {
		t.Fatalf("args = %v, want -e <extension>", cs.Args)
	}
	body, err := os.ReadFile(cs.Args[i+1])
	if err != nil {
		t.Fatalf("extension not written: %v", err)
	}
	for _, want := range []string{`"tool", "steering"`, `"tool_result"`, "hookSpecificOutput"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("extension lacks %q", want)
		}
	}
}

func TestPiInvocationWithoutSteeringLoadsNoExtension(t *testing.T) {
	cs, err := piInvocation(SessionSpec{Prompt: "fix it"})
	if err != nil {
		t.Fatal(err)
	}
	if indexOf(cs.Args, "-e") >= 0 {
		t.Fatalf("args = %v, want no extension", cs.Args)
	}
}
