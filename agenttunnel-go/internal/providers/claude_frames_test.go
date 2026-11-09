package providers

import (
	"reflect"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Frames captured from `claude -p --output-format stream-json --verbose`.
const (
	claudeAssistantText = `{"type":"assistant","message":{"model":"claude-sonnet-5-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":2,"cache_creation_input_tokens":8508,"cache_read_input_tokens":9013,"output_tokens":4}},"session_id":"s1"}`

	claudeAssistantTool = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls -la"}}]},"session_id":"s1"}`

	claudeUserToolResult = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"file.txt"}],"is_error":false}]},"session_id":"s1"}`

	claudeResult = `{"type":"result","subtype":"success","is_error":false,"result":"ok","total_cost_usd":0.0349773,"usage":{"input_tokens":2,"cache_creation_input_tokens":8508,"cache_read_input_tokens":9013,"output_tokens":4,"output_tokens_details":{"thinking_tokens":7}},"session_id":"s1"}`

	claudeErrorResult = `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"too many turns","usage":{"input_tokens":1,"output_tokens":1}}`
)

func TestTranslateClaudeAssistantText(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(claudeAssistantText)
	if len(evs) != 1 || evs[0].Kind != contracts.LogText || evs[0].Text != "ok" {
		t.Fatalf("got %+v", evs)
	}
	// Per-message usage must not be emitted: the result frame aggregates it.
	if evs[0].Usage != nil {
		t.Fatalf("assistant frame emitted usage: %+v", evs[0].Usage)
	}
}

func TestTranslateClaudeToolUse(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(claudeAssistantTool)
	if len(evs) != 1 || evs[0].Kind != contracts.LogTool {
		t.Fatalf("got %+v", evs)
	}
	if evs[0].Text != "$ ls -la" || evs[0].ToolName != "Bash" {
		t.Fatalf("got %+v", evs[0])
	}
}

func TestTranslateClaudeToolResult(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(claudeUserToolResult)
	if len(evs) != 1 || evs[0].Kind != contracts.LogRaw || evs[0].Text != "file.txt" {
		t.Fatalf("got %+v", evs)
	}
}

func TestTranslateClaudeResultUsage(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(claudeResult)
	if len(evs) != 1 || evs[0].Kind != contracts.LogUsage || evs[0].Usage == nil {
		t.Fatalf("got %+v", evs)
	}
	u := evs[0].Usage
	if u.InputTokens != 2 || u.OutputTokens != 4 {
		t.Errorf("tokens = %+v", u)
	}
	if u.CachedTokens != 8508+9013 {
		t.Errorf("cached = %d, want %d", u.CachedTokens, 8508+9013)
	}
	if u.ReasoningTokens != 7 {
		t.Errorf("reasoning = %d, want 7", u.ReasoningTokens)
	}
	if u.CostUSD != 0.0349773 {
		t.Errorf("cost = %v, want 0.0349773", u.CostUSD)
	}
}

func TestTranslateClaudeErrorResult(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(claudeErrorResult)
	if len(evs) != 2 {
		t.Fatalf("got %+v", evs)
	}
	if evs[0].Kind != contracts.LogError || evs[0].Text != "too many turns" {
		t.Fatalf("error event = %+v", evs[0])
	}
	if evs[1].Kind != contracts.LogUsage || evs[1].Usage == nil || evs[1].Usage.InputTokens != 1 {
		t.Fatalf("usage event = %+v", evs[1])
	}
}

func TestClaudeInvocationRequestsStreamJSON(t *testing.T) {
	cs, err := claudeInvocation(SessionSpec{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "hi"}
	if !reflect.DeepEqual(cs.Args, want) {
		t.Fatalf("args = %v, want %v", cs.Args, want)
	}
}
