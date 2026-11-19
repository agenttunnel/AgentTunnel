package providers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Frames captured from `claude -p --output-format stream-json --verbose`.
const (
	claudeEditTool = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_e","name":"Edit","input":{"file_path":"/repo/main.go","old_string":"func main() {\n\tprintln(\"hi\")\n}","new_string":"func main() {\n\tprintln(\"hello\")\n}"}}]}}`

	claudeEmptyResult = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":""}]}}`

	claudeFailedResult = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Exit code 2\nno such file","is_error":true}]}}`
)

// Frames captured from `codex exec --json`.
const (
	codexCmdStarted   = `{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"bash -lc 'go test ./...'","aggregated_output":"","exit_code":null,"status":"in_progress"}}`
	codexCmdCompleted = `{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"bash -lc 'go test ./...'","aggregated_output":"FAIL\tpkg\n","exit_code":1,"status":"failed"}}`
	codexCmdOK        = `{"type":"item.completed","item":{"id":"item_2","type":"command_execution","command":"true","aggregated_output":"","exit_code":0,"status":"completed"}}`
	codexPatchFrame   = `{"type":"item.completed","item":{"id":"item_3","type":"file_change","changes":[{"path":"/repo/a.go","kind":"update"},{"path":"/repo/b.go","kind":"add"}],"status":"completed"}}`
	codexMessage      = `{"type":"item.completed","item":{"id":"item_4","type":"agent_message","text":"All done."}}`
	codexReasoning    = `{"type":"item.completed","item":{"id":"item_0","type":"reasoning","text":"**Planning**"}}`
	codexTurnDone     = `{"type":"turn.completed","usage":{"input_tokens":1200,"cached_input_tokens":1000,"output_tokens":50}}`
	codexTurnFailed   = `{"type":"turn.failed","error":{"message":"stream disconnected"}}`
)

// A frame captured from `opencode run --format json`: an edit reported once,
// finished, with its unified diff and timing.
const openCodeEditTool = `{"type":"tool_use","part":{"type":"tool","tool":"edit","callID":"c9","state":{"status":"completed","input":{"filePath":"/repo/a.go","oldString":"a","newString":"b"},"output":"","metadata":{"diff":"Index: /repo/a.go\n--- /repo/a.go\n+++ /repo/a.go\n@@ -1,1 +1,1 @@\n-a\n+b\n"},"title":"a.go","time":{"start":1000,"end":1250}}}}`

func TestClaudeToolUseCarriesStructuredCall(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(claudeAssistantTool)
	if len(evs) != 1 || evs[0].Tool == nil {
		t.Fatalf("got %+v", evs)
	}
	got := *evs[0].Tool
	want := contracts.ToolCall{ID: "toolu_1", Name: "Bash", Command: "ls -la", Status: contracts.ToolRunning}
	if got != want {
		t.Fatalf("tool = %+v, want %+v", got, want)
	}
	// The plain line is unchanged for clients that ignore the structure.
	if evs[0].Text != "$ ls -la" {
		t.Fatalf("text = %q", evs[0].Text)
	}
}

func TestClaudeToolResultFoldsByID(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(claudeUserToolResult)
	if len(evs) != 1 || evs[0].Text != "file.txt" || evs[0].Kind != contracts.LogRaw {
		t.Fatalf("got %+v", evs)
	}
	if tool := evs[0].Tool; tool == nil || tool.ID != "toolu_1" || tool.Status != contracts.ToolDone {
		t.Fatalf("tool = %+v", evs[0].Tool)
	}

	// A call that printed nothing still reports that it finished.
	evs = JSONLineTranslator{}.Translate(claudeEmptyResult)
	if len(evs) != 1 || evs[0].Text != "(no output)" || evs[0].Tool.Status != contracts.ToolDone {
		t.Fatalf("empty result = %+v", evs)
	}

	evs = JSONLineTranslator{}.Translate(claudeFailedResult)
	if len(evs) != 1 || evs[0].Kind != contracts.LogError || evs[0].Tool.Status != contracts.ToolFailed {
		t.Fatalf("failed result = %+v", evs)
	}
}

func TestClaudeEditCarriesDiff(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(claudeEditTool)
	if len(evs) != 1 || evs[0].Tool == nil {
		t.Fatalf("got %+v", evs)
	}
	tool := evs[0].Tool
	if tool.Path != "/repo/main.go" || tool.Name != "Edit" {
		t.Fatalf("tool = %+v", tool)
	}
	// Lines both sides share are context; only the changed line is -/+.
	want := " func main() {\n-\tprintln(\"hi\")\n+\tprintln(\"hello\")\n }"
	if tool.Diff != want {
		t.Fatalf("diff =\n%s\nwant\n%s", tool.Diff, want)
	}
}

func TestEditDiffShapes(t *testing.T) {
	multi := map[string]any{"file_path": "f", "edits": []any{
		map[string]any{"old_string": "a", "new_string": "b"},
		map[string]any{"old_string": "c", "new_string": "d"},
	}}
	if got := editDiff("MultiEdit", multi); got != "-a\n+b\n@@\n-c\n+d" {
		t.Fatalf("multi = %q", got)
	}
	if got := editDiff("Write", map[string]any{"file_path": "f", "content": "x\ny\n"}); got != "+x\n+y" {
		t.Fatalf("write = %q", got)
	}
	// "content" is only a file body on a write.
	if got := editDiff("Grep", map[string]any{"content": "x"}); got != "" {
		t.Fatalf("grep = %q", got)
	}
	big := editDiff("Write", map[string]any{"content": strings.Repeat("0123456789\n", 4000)})
	if len(big) > toolDiffCap+40 || !strings.HasSuffix(big, "… (diff truncated)") {
		t.Fatalf("big diff not capped: len %d", len(big))
	}
}

func TestCodexCommandStartAndFailure(t *testing.T) {
	evs := CodexTranslator{}.Translate(codexCmdStarted)
	if len(evs) != 1 || evs[0].Kind != contracts.LogTool || evs[0].Text != "$ bash -lc 'go test ./...'" {
		t.Fatalf("start = %+v", evs)
	}
	if tool := evs[0].Tool; tool == nil || tool.ID != "item_1" || tool.Status != contracts.ToolRunning || tool.Command != "bash -lc 'go test ./...'" {
		t.Fatalf("start tool = %+v", evs[0].Tool)
	}

	evs = CodexTranslator{}.Translate(codexCmdCompleted)
	if len(evs) != 1 || evs[0].Kind != contracts.LogError || evs[0].Text != "FAIL\tpkg" {
		t.Fatalf("completed = %+v", evs)
	}
	tool := evs[0].Tool
	if tool == nil || tool.ID != "item_1" || tool.Status != contracts.ToolFailed || tool.ExitCode == nil || *tool.ExitCode != 1 {
		t.Fatalf("completed tool = %+v", tool)
	}

	evs = CodexTranslator{}.Translate(codexCmdOK)
	if len(evs) != 1 || evs[0].Text != "(no output)" || evs[0].Tool.Status != contracts.ToolDone || *evs[0].Tool.ExitCode != 0 {
		t.Fatalf("ok = %+v", evs)
	}
}

func TestCodexFileChangeIsOneToolLine(t *testing.T) {
	evs := CodexTranslator{}.Translate(codexPatchFrame)
	if len(evs) != 1 || evs[0].Kind != contracts.LogTool {
		t.Fatalf("got %+v", evs)
	}
	if evs[0].Text != "→ Edit /repo/a.go\n→ Add /repo/b.go" {
		t.Fatalf("text = %q", evs[0].Text)
	}
	if tool := evs[0].Tool; tool.Path != "/repo/a.go" || tool.Status != contracts.ToolDone {
		t.Fatalf("tool = %+v", tool)
	}
}

func TestCodexMessagesAndUsage(t *testing.T) {
	if evs := (CodexTranslator{}).Translate(codexMessage); len(evs) != 1 || evs[0].Kind != contracts.LogText || evs[0].Text != "All done." {
		t.Fatalf("message = %+v", evs)
	}
	if evs := (CodexTranslator{}).Translate(codexReasoning); len(evs) != 1 || evs[0].Kind != contracts.LogReasoning {
		t.Fatalf("reasoning = %+v", evs)
	}
	evs := CodexTranslator{}.Translate(codexTurnDone)
	if len(evs) != 1 || evs[0].Usage == nil {
		t.Fatalf("usage = %+v", evs)
	}
	if u := *evs[0].Usage; u.InputTokens != 200 || u.CachedTokens != 1000 || u.OutputTokens != 50 {
		t.Fatalf("usage = %+v", u)
	}
	if evs := (CodexTranslator{}).Translate(codexTurnFailed); len(evs) != 1 || evs[0].Kind != contracts.LogError {
		t.Fatalf("turn failed = %+v", evs)
	}
	for _, structural := range []string{`{"type":"thread.started","thread_id":"x"}`, `{"type":"turn.started"}`} {
		if evs := (CodexTranslator{}).Translate(structural); len(evs) != 0 {
			t.Fatalf("%s = %+v", structural, evs)
		}
	}
	// A stray prose line on stderr still reaches the log.
	if evs := (CodexTranslator{}).Translate("warning: thing"); len(evs) != 1 || evs[0].Kind != contracts.LogText {
		t.Fatalf("prose = %+v", evs)
	}
}

// fakeCodex puts a codex on PATH whose `exec --help` prints help, and
// counts how often it ran.
func fakeCodex(t *testing.T, help string) (runs func() int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake codex is a shell script")
	}
	dir := t.TempDir()
	count := filepath.Join(dir, "runs")
	// PATH holds only the fake, so the script sticks to shell builtins.
	script := "#!/bin/sh\necho x >> '" + count + "'\nprintf '%s\\n' '" + help + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}
}

func TestCodexInvocationStreamsJSON(t *testing.T) {
	runs := fakeCodex(t, "Usage: codex exec [OPTIONS] [PROMPT]\n      --json  Print events to stdout as JSONL")
	for range 2 {
		cs, err := codexInvocation(SessionSpec{Prompt: "hi", Variant: "high"})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(cs.Args[:2], []string{"exec", "--json"}) || cs.Args[len(cs.Args)-1] != "hi" {
			t.Fatalf("args = %v", cs.Args)
		}
		if !slices.Contains(cs.Args, `model_reasoning_effort="high"`) {
			t.Fatalf("variant not passed as reasoning effort: %v", cs.Args)
		}
	}
	if n := runs(); n != 1 {
		t.Fatalf("help probed %d times, want once", n)
	}
}

func TestCodexWithoutJSONRunsPlain(t *testing.T) {
	fakeCodex(t, "Usage: codex exec [OPTIONS] [PROMPT]\n      --image <FILE>")
	cs, err := codexInvocation(SessionSpec{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(cs.Args, "--json") || cs.Args[0] != "exec" {
		t.Fatalf("args = %v", cs.Args)
	}
}

// Codex releases before the item schema printed {"id","msg"} events for --json.
func TestCodexLegacyEnvelope(t *testing.T) {
	if evs := (CodexTranslator{}).Translate(`{"id":"0","msg":{"type":"agent_message","message":"Done."}}`); len(evs) != 1 || evs[0].Kind != contracts.LogText || evs[0].Text != "Done." {
		t.Fatalf("message = %+v", evs)
	}
	evs := CodexTranslator{}.Translate(`{"id":"0","msg":{"type":"exec_command_begin","call_id":"c1","command":["bash","-lc","ls"],"cwd":"/r"}}`)
	if len(evs) != 1 || evs[0].Text != "$ bash -lc ls" || evs[0].Tool == nil || evs[0].Tool.ID != "c1" {
		t.Fatalf("begin = %+v", evs)
	}
	evs = CodexTranslator{}.Translate(`{"id":"0","msg":{"type":"exec_command_end","call_id":"c1","stdout":"a\n","stderr":"","exit_code":0}}`)
	if len(evs) != 1 || evs[0].Text != "a" || evs[0].Tool.Status != contracts.ToolDone {
		t.Fatalf("end = %+v", evs)
	}
	if evs := (CodexTranslator{}).Translate(`{"id":"0","msg":{"type":"agent_message_delta","delta":"Do"}}`); len(evs) != 0 {
		t.Fatalf("delta = %+v", evs)
	}
}

func TestOpenCodeEditCarriesDiffAndDuration(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(openCodeEditTool)
	if len(evs) != 1 || evs[0].Tool == nil {
		t.Fatalf("got %+v", evs)
	}
	tool := evs[0].Tool
	if tool.ID != "c9" || tool.Status != contracts.ToolDone || tool.DurationMs != 250 || tool.Path != "/repo/a.go" {
		t.Fatalf("tool = %+v", tool)
	}
	if !strings.Contains(tool.Diff, "@@ -1,1 +1,1 @@\n-a\n+b") {
		t.Fatalf("diff = %q", tool.Diff)
	}

	evs = JSONLineTranslator{}.Translate(openCodeBashTool)
	if len(evs) != 2 || evs[0].Tool.ExitCode == nil || *evs[0].Tool.ExitCode != 0 || evs[1].Tool.ID != "c1" {
		t.Fatalf("bash = %+v", evs)
	}
}

func TestPiToolCallFoldsByID(t *testing.T) {
	start := JSONLineTranslator{}.Translate(`{"type":"tool_execution_start","toolCallId":"bash_1","toolName":"bash","args":{"command":"pwd"}}`)
	end := JSONLineTranslator{}.Translate(`{"type":"tool_execution_end","toolCallId":"bash_1","toolName":"bash","result":{"content":[{"type":"text","text":"boom"}]},"isError":true}`)
	if len(start) != 1 || start[0].Tool == nil || start[0].Tool.ID != "bash_1" || start[0].Tool.Command != "pwd" {
		t.Fatalf("start = %+v", start)
	}
	if len(end) != 1 || end[0].Tool == nil || end[0].Tool.ID != "bash_1" || end[0].Tool.Status != contracts.ToolFailed {
		t.Fatalf("end = %+v", end)
	}
}

// The wire addition is optional: an entry without a call serializes exactly as
// before, so old clients and stored logs read the same.
func TestLogEntryWithoutToolOmitsField(t *testing.T) {
	raw, err := json.Marshal(contracts.LogEntry{AgentID: "a", Kind: contracts.LogText, Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tool") {
		t.Fatalf("plain entry carries tool: %s", raw)
	}
}
