package providers

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// These frames were captured from a real `opencode run --auto --format json
// --thinking "<prompt>"` invocation.
const openCodeStepStart = `{"type":"step_start","timestamp":1791662168571,"sessionID":"ses_x","part":{"id":"prt_1","messageID":"msg_1","sessionID":"ses_x","type":"step-start"}}`
const openCodeReasoning = `{"type":"reasoning","timestamp":1791662168741,"sessionID":"ses_x","part":{"id":"prt_2","messageID":"msg_1","sessionID":"ses_x","type":"reasoning","text":"The user is asking me to reply with only the single word \"pong\".","time":{"start":1,"end":2}}}`
const openCodeText = `{"type":"text","timestamp":1791662168741,"sessionID":"ses_x","part":{"id":"prt_3","messageID":"msg_1","sessionID":"ses_x","type":"text","text":"pong","time":{"start":3,"end":4}}}`
const openCodeStepFinish = `{"type":"step_finish","timestamp":1791662168741,"sessionID":"ses_x","part":{"id":"prt_4","reason":"stop","messageID":"msg_1","sessionID":"ses_x","type":"step-finish","tokens":{"total":7906,"input":400,"output":3,"reasoning":15,"cache":{"write":0,"read":7488}},"cost":0}}`

func TestTranslateOpenCodeTextFrame(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(openCodeText)
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %+v", evs)
	}
	if evs[0].Kind != contracts.LogText || evs[0].Text != "pong" {
		t.Fatalf("got %+v", evs[0])
	}
}

func TestTranslateOpenCodeReasoningFrame(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(openCodeReasoning)
	if len(evs) != 1 || evs[0].Kind != contracts.LogReasoning {
		t.Fatalf("got %+v", evs)
	}
}

func TestTranslateOpenCodeStructuralFramesAreDropped(t *testing.T) {
	// step_start carries no text and no tokens, so it yields nothing.
	if evs := (JSONLineTranslator{}).Translate(openCodeStepStart); len(evs) != 0 {
		t.Fatalf("expected step_start to produce no event, got %+v", evs)
	}
}

func TestTranslateOpenCodeUsageFromNestedTokens(t *testing.T) {
	evs := JSONLineTranslator{}.Translate(openCodeStepFinish)
	if len(evs) != 1 || evs[0].Kind != contracts.LogUsage || evs[0].Usage == nil {
		t.Fatalf("got %+v", evs)
	}
	u := evs[0].Usage
	if u.InputTokens != 400 {
		t.Errorf("input = %d, want 400", u.InputTokens)
	}
	if u.OutputTokens != 3 {
		t.Errorf("output = %d, want 3 (reasoning is reported separately)", u.OutputTokens)
	}
	if u.ReasoningTokens != 15 {
		t.Errorf("reasoning = %d, want 15", u.ReasoningTokens)
	}
	if u.CachedTokens != 7488 {
		t.Errorf("cached = %d, want 7488 (cache.read)", u.CachedTokens)
	}
}
