package providers

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// The Command Code CLI is installed as `commandcode` (and `cmd`), never
// `command-code`; the driver must look up the real binary name.
func TestCommandCodeInvocationUsesCommandcodeBinary(t *testing.T) {
	spec, err := commandCodeInvocation(SessionSpec{Prompt: "hi"})
	if err != nil {
		t.Fatalf("commandCodeInvocation: %v", err)
	}
	if spec.Binary != "commandcode" {
		t.Fatalf("Binary = %q, want %q", spec.Binary, "commandcode")
	}
}

func TestCommandCodeRegisteredDriverUsesCommandcodeBinary(t *testing.T) {
	driver, ok := BuiltinRegistry().Get(contracts.HarnessCommandCode)
	if !ok {
		t.Fatal("no driver registered for HarnessCommandCode")
	}
	if got := driver.Binary(); got != "commandcode" {
		t.Fatalf("driver Binary = %q, want %q", got, "commandcode")
	}
}
