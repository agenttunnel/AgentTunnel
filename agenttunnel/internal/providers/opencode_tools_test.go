package providers

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Captured from a real `opencode run --auto --format json` that ran a shell
// command and a file read. Tool frames are top-level "tool_use" with the tool and
// its state nested under "part".
const openCodeBashTool = `{"type":"tool_use","timestamp":1,"sessionID":"ses_x","part":{"type":"tool","tool":"bash","callID":"c1","state":{"status":"completed","input":{"command":"echo hello-from-bash"},"output":"hello-from-bash\n","metadata":{"output":"hello-from-bash\n","exit":0,"truncated":false},"title":"echo hello-from-bash","time":{"start":1,"end":2}},"id":"prt_5","sessionID":"ses_x","messageID":"msg_1"}}`

const openCodeReadTool = `{"type":"tool_use","timestamp":2,"sessionID":"ses_x","part":{"type":"tool","tool":"read","callID":"c2","state":{"status":"completed","input":{"filePath":"apps/server"},"output":"package main\n","title":"apps/server"}}}`

func TestTranslateOpenCodeBashToolShowsCommandAndOutput(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(openCodeBashTool)
	if len(evs) != 2 {
		t.Fatalf("want a summary plus output, got %+v", evs)
	}
	if evs[0].Kind != contracts.LogTool || evs[0].Text != "$ echo hello-from-bash" {
		t.Fatalf("summary = %+v", evs[0])
	}
	if evs[0].ToolName != "bash" {
		t.Fatalf("tool name = %q", evs[0].ToolName)
	}
	if evs[1].Kind != contracts.LogRaw || evs[1].Text != "hello-from-bash" {
		t.Fatalf("output = %+v", evs[1])
	}
}

func TestTranslateOpenCodeReadToolNamesThePath(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(openCodeReadTool)
	if len(evs) < 1 {
		t.Fatal("expected an event")
	}
	if evs[0].Kind != contracts.LogTool || evs[0].Text != "→ Read apps/server" {
		t.Fatalf("got %+v", evs[0])
	}
}

func TestTranslateToolWithoutStateStillNamesTheTool(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(`{"type":"tool","part":{"type":"tool","tool":"grep"}}`)
	if len(evs) != 1 || evs[0].Text != "→ Grep" {
		t.Fatalf("got %+v", evs)
	}
}
