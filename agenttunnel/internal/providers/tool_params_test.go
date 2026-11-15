package providers

import (
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Tool frames carry every argument the harness sent; the log summary must keep
// them instead of dropping all but the path or command.

func TestToolFrameKeepsShellCommandAndParameters(t *testing.T) {
	line := `{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"input":{"command":"set -e\necho one\necho two","timeout":30000}}}}`
	evs := JSONLineTranslator{}.Translate(line)
	if len(evs) != 1 || evs[0].Kind != contracts.LogTool {
		t.Fatalf("got %+v", evs)
	}
	want := "$ set -e\necho one\necho two\n  timeout: 30000"
	if evs[0].Text != want {
		t.Fatalf("text = %q, want %q", evs[0].Text, want)
	}
}

func TestToolFrameListsRemainingParametersSorted(t *testing.T) {
	line := `{"type":"tool_use","part":{"type":"tool","tool":"read","state":{"input":{"filePath":"apps/server","limit":40,"offset":20}}}}`
	evs := JSONLineTranslator{}.Translate(line)
	if len(evs) != 1 || evs[0].Kind != contracts.LogTool {
		t.Fatalf("got %+v", evs)
	}
	want := "→ Read apps/server\n  limit: 40\n  offset: 20"
	if evs[0].Text != want {
		t.Fatalf("text = %q, want %q", evs[0].Text, want)
	}
}

func TestClaudeToolUseKeepsNonPathParameters(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls -la","description":"list files","timeout":120000}}]}}`
	evs := JSONLineTranslator{}.Translate(line)
	if len(evs) != 1 || evs[0].Kind != contracts.LogTool {
		t.Fatalf("got %+v", evs)
	}
	want := "$ ls -la\n  description: list files\n  timeout: 120000"
	if evs[0].Text != want {
		t.Fatalf("text = %q, want %q", evs[0].Text, want)
	}
}

func TestPiToolExecutionStartKeepsArguments(t *testing.T) {
	line := `{"type":"tool_execution_start","toolCallId":"bash_1","toolName":"bash","args":{"command":"pwd","cwd":"/tmp"}}`
	evs := JSONLineTranslator{}.Translate(line)
	if len(evs) != 1 || evs[0].Kind != contracts.LogTool {
		t.Fatalf("got %+v", evs)
	}
	want := "$ pwd\n  cwd: /tmp"
	if evs[0].Text != want {
		t.Fatalf("text = %q, want %q", evs[0].Text, want)
	}
}

func TestToolFrameKeepsNestedAndNonStringValues(t *testing.T) {
	line := `{"type":"tool","part":{"type":"tool","tool":"grep","state":{"input":{"pattern":"TODO","paths":["a.go","b.go"]}}}}`
	evs := JSONLineTranslator{}.Translate(line)
	if len(evs) != 1 || evs[0].Kind != contracts.LogTool {
		t.Fatalf("got %+v", evs)
	}
	want := "→ Grep TODO\n  paths: [\"a.go\",\"b.go\"]"
	if evs[0].Text != want {
		t.Fatalf("text = %q, want %q", evs[0].Text, want)
	}
}

func TestToolFrameKeepsTopLevelArguments(t *testing.T) {
	line := `{"type":"tool","tool":"bash","args":{"command":"pwd","timeout":5}}`
	evs := JSONLineTranslator{}.Translate(line)
	if len(evs) != 1 || evs[0].Kind != contracts.LogTool {
		t.Fatalf("got %+v", evs)
	}
	want := "$ pwd\n  timeout: 5"
	if evs[0].Text != want {
		t.Fatalf("text = %q, want %q", evs[0].Text, want)
	}
}

func TestToolDetailTruncatesHugeArguments(t *testing.T) {
	line := `{"type":"tool","part":{"type":"tool","tool":"write","state":{"input":{"content":"` + strings.Repeat("x", 2000) + `"}}}}`
	evs := JSONLineTranslator{}.Translate(line)
	if len(evs) != 1 || evs[0].Kind != contracts.LogTool {
		t.Fatalf("got %+v", evs)
	}
	if len(evs[0].Text) > 2200 || !strings.Contains(evs[0].Text, "… (truncated)") {
		t.Fatalf("text was not bounded: len=%d", len(evs[0].Text))
	}
}
