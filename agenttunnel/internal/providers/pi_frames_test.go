package providers

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// These frames were captured from a real `pi --mode json -p "<prompt>"
// --no-session` invocation.

func TestTranslatePiTextDelta(t *testing.T) {
	line := `{"type":"message_update","usage":{"input":468,"output":33,"cacheRead":2733,"cacheWrite":0,"reasoning":33,"cost":{"total":0.000087}},"assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"OK"}}`
	evs := JSONLineTranslator{}.Translate(line)
	if len(evs) != 1 || evs[0].Kind != contracts.LogText || evs[0].Text != "OK" {
		t.Fatalf("got %+v", evs)
	}
}

func TestTranslatePiThinkingDelta(t *testing.T) {
	line := `{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"plan"}}`
	evs := JSONLineTranslator{}.Translate(line)
	if len(evs) != 1 || evs[0].Kind != contracts.LogReasoning || evs[0].Text != "plan" {
		t.Fatalf("got %+v", evs)
	}
}

func TestTranslatePiMessageEndReportsUsageOnce(t *testing.T) {
	line := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"OK"}],"usage":{"input":468,"output":33,"cacheRead":2733,"cacheWrite":0,"reasoning":33,"cost":{"total":0.000087}}}}`
	evs := JSONLineTranslator{}.Translate(line)
	if len(evs) != 1 || evs[0].Kind != contracts.LogUsage || evs[0].Usage == nil {
		t.Fatalf("got %+v", evs)
	}
	u := evs[0].Usage
	if u.InputTokens != 468 || u.OutputTokens != 33 || u.ReasoningTokens != 33 {
		t.Errorf("tokens = %+v", u)
	}
	if u.CachedTokens != 2733 {
		t.Errorf("cached = %d, want 2733", u.CachedTokens)
	}
	if u.CostUSD != 0.000087 {
		t.Errorf("cost = %v, want 0.000087", u.CostUSD)
	}
}

func TestTranslatePiToolExecution(t *testing.T) {
	start := `{"type":"tool_execution_start","toolCallId":"bash_1","toolName":"bash","args":{"command":"pwd"}}`
	evs := JSONLineTranslator{}.Translate(start)
	if len(evs) != 1 || evs[0].Kind != contracts.LogTool || evs[0].Text != "$ pwd" {
		t.Fatalf("start = %+v", evs)
	}

	end := `{"type":"tool_execution_end","toolCallId":"bash_1","toolName":"bash","result":{"content":[{"type":"text","text":"/tmp/repo\n"}],"structuredContent":{"output":"/tmp/repo\n"}},"isError":false}`
	evs = JSONLineTranslator{}.Translate(end)
	if len(evs) != 1 || evs[0].Kind != contracts.LogRaw || evs[0].Text != "/tmp/repo" {
		t.Fatalf("end = %+v", evs)
	}
}

func TestTranslatePiStructuralFramesAreDropped(t *testing.T) {
	for _, line := range []string{
		`{"type":"session","version":3}`,
		`{"type":"agent_start"}`,
		`{"type":"turn_start"}`,
		`{"type":"message_start"}`,
		`{"type":"turn_end"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","delta":"{\"command\":\"pwd\"}"}}`,
	} {
		if evs := (JSONLineTranslator{}).Translate(line); len(evs) != 0 {
			t.Fatalf("%s produced %+v", line, evs)
		}
	}
}
