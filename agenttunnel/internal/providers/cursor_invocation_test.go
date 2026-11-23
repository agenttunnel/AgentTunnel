package providers

import (
	"reflect"
	"testing"
)

func TestCursorInvocationRunsYoloInPrintMode(t *testing.T) {
	cs, err := cursorInvocation(SessionSpec{Prompt: "fix lint", Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--print", "--yolo", "--model", "gpt-5", "fix lint"}
	if !reflect.DeepEqual(cs.Args, want) {
		t.Fatalf("args = %v, want %v", cs.Args, want)
	}
	if cs.PromptVia != "arg" {
		t.Fatalf("prompt via %q, want arg", cs.PromptVia)
	}
}

func TestCursorInvocationOmitsModelWhenUnset(t *testing.T) {
	cs, err := cursorInvocation(SessionSpec{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--print", "--yolo", "hi"}
	if !reflect.DeepEqual(cs.Args, want) {
		t.Fatalf("args = %v, want %v", cs.Args, want)
	}
}
