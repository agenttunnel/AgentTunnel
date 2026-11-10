package providers

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Frames shaped after `command-code -p ... --output-format json`.
const (
	commandCodeToolRunning = `{"type":"event","event":{"type":"tool_running","toolCallId":"c1","toolName":"read_file","description":"main.go"}}`
	commandCodeResultOK    = `{"type":"result","subtype":"success","stopReason":"end_turn","finalText":"done","usage":{"input_tokens":12,"output_tokens":3},"durationMs":10,"sessionId":"s1"}`
	commandCodeResultError = `{"type":"result","subtype":"error","error":"Not authenticated","usage":{"input_tokens":0,"output_tokens":0},"durationMs":4}`
)

func TestCommandCodeInvocationRunsYoloWithJSONOutput(t *testing.T) {
	cs, err := commandCodeInvocation(SessionSpec{Prompt: "fix lint", Model: "typesafe/jev"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "fix lint", "--yolo", "--output-format", "json", "--model", "typesafe/jev"}
	if !reflect.DeepEqual(cs.Args, want) {
		t.Fatalf("args = %v, want %v", cs.Args, want)
	}
	if cs.PromptVia != "arg" {
		t.Fatalf("prompt via %q, want arg", cs.PromptVia)
	}
}

func TestCommandCodeInvocationOmitsModelWhenUnset(t *testing.T) {
	cs, err := commandCodeInvocation(SessionSpec{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "hi", "--yolo", "--output-format", "json"}
	if !reflect.DeepEqual(cs.Args, want) {
		t.Fatalf("args = %v, want %v", cs.Args, want)
	}
}

func TestCommandCodeTranslatesToolRunningEvent(t *testing.T) {
	evs := CommandCodeTranslator{}.Translate(commandCodeToolRunning)
	if len(evs) != 1 || evs[0].Kind != contracts.LogTool {
		t.Fatalf("got %+v", evs)
	}
	if evs[0].Text != "→ Read_file main.go" || evs[0].ToolName != "read_file" {
		t.Fatalf("tool event = %+v", evs[0])
	}
}

func TestCommandCodeIgnoresUnknownEventTypes(t *testing.T) {
	evs := CommandCodeTranslator{}.Translate(`{"type":"event","event":{"type":"tool_done","toolName":"read_file"}}`)
	if len(evs) != 0 {
		t.Fatalf("got %+v, want none", evs)
	}
}

func TestCommandCodeResultCarriesAnswerAndUsage(t *testing.T) {
	evs := CommandCodeTranslator{}.Translate(commandCodeResultOK)
	if len(evs) != 2 {
		t.Fatalf("got %+v", evs)
	}
	if evs[0].Kind != contracts.LogText || evs[0].Text != "done" {
		t.Fatalf("answer event = %+v", evs[0])
	}
	if evs[1].Kind != contracts.LogUsage || evs[1].Usage == nil || evs[1].Usage.InputTokens != 12 {
		t.Fatalf("usage event = %+v", evs[1])
	}
}

func TestCommandCodeErrorResultIsAnError(t *testing.T) {
	evs := CommandCodeTranslator{}.Translate(commandCodeResultError)
	if len(evs) != 1 || evs[0].Kind != contracts.LogError || evs[0].Text != "Not authenticated" {
		t.Fatalf("got %+v", evs)
	}
}

func TestCommandCodeNonJSONLineFallsBackToText(t *testing.T) {
	evs := CommandCodeTranslator{}.Translate("plain line")
	if len(evs) != 1 || evs[0].Kind != contracts.LogText || evs[0].Text != "plain line\n" {
		t.Fatalf("got %+v", evs)
	}
}

// The fake harness is a shell that echoes its argv back inside a result frame,
// so the test proves the driver's real argv reaches the child process.
func TestCommandCodeDriverPassesYoloArgvToChild(t *testing.T) {
	script := `printf '{"type":"result","subtype":"success","finalText":"%s"}\n' "$*"`
	driver := NewGenericDriver(contracts.HarnessCommandCode, "sh", func(spec SessionSpec) (CommandSpec, error) {
		cs, err := commandCodeInvocation(spec)
		if err != nil {
			return CommandSpec{}, err
		}
		return CommandSpec{Binary: "sh", Args: append([]string{"-c", script, "sh"}, cs.Args...), PromptVia: "arg"}, nil
	}, CommandCodeTranslator{})

	a := NewProcessAdapter(driver)
	if err := a.Start(context.Background(), SessionSpec{Prompt: "write it"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	events := collect(t, a)
	if len(events) == 0 || events[0].Kind != contracts.LogText {
		t.Fatalf("events = %+v", events)
	}
	got := events[0].Text
	for _, want := range []string{"-p write it", "--yolo", "--output-format json"} {
		if !strings.Contains(got, want) {
			t.Fatalf("child argv %q missing %q", got, want)
		}
	}
}
