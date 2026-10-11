package providers

import (
	"encoding/json"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// InvocationFunc builds the argv for one session.
type InvocationFunc func(spec SessionSpec) (CommandSpec, error)

// GenericDriver is a data-driven Driver: a harness is described by its binary,
// an argv builder, and a line translator.
type GenericDriver struct {
	kind       contracts.Harness
	binary     string
	invoke     InvocationFunc
	translator Translator
}

// NewGenericDriver constructs a driver for a harness.
func NewGenericDriver(kind contracts.Harness, binary string, invoke InvocationFunc, translator Translator) *GenericDriver {
	return &GenericDriver{kind: kind, binary: binary, invoke: invoke, translator: translator}
}

func (g *GenericDriver) Harness() contracts.Harness { return g.kind }
func (g *GenericDriver) Binary() string             { return g.binary }

func (g *GenericDriver) BuildInvocation(spec SessionSpec) (CommandSpec, Translator, error) {
	cs, err := g.invoke(spec)
	if err != nil {
		return CommandSpec{}, nil, err
	}
	return cs, g.translator, nil
}

// JSONLineTranslator parses newline-delimited JSON frames from a harness and maps
// them to normalized events.
//
// Frame shapes vary: some harnesses put fields at the top level, while others
// (opencode) nest the payload under "part". The mapping is intentionally
// tolerant — it prefers recognized keys in the nested scope, then the top level,
// and drops structural frames that carry no user-visible text.
type JSONLineTranslator struct{}

func (JSONLineTranslator) Translate(line string) []Event {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return []Event{{Kind: contracts.LogText, Text: line}}
	}

	scope := raw
	if part, ok := raw["part"].(map[string]any); ok {
		scope = part
	}

	if usage := usageFromFrame(raw, scope); usage != nil {
		return []Event{{Kind: contracts.LogUsage, Usage: usage}}
	}

	frameType := strings.ToLower(firstString(raw, "type", "role", "kind"))
	switch frameType {
	case "tool", "tool_use", "tool_call":
		return toolEvents(scope)
	case "error":
		return singleText(contracts.LogError, firstString(scope, "text", "message", "error"))
	case "step_start", "step-start", "step_finish", "step-finish",
		"message_start", "message_started", "message_update":
		return nil // structural frames with no user-visible text
	}

	kind := contracts.LogText
	switch frameType {
	case "reasoning", "thinking":
		kind = contracts.LogReasoning
	}

	text := firstString(scope, "text", "message", "content", "delta")
	if text == "" {
		text = firstString(raw, "text", "message", "content", "delta")
	}
	if text == "" {
		return nil
	}
	return singleText(kind, text)
}

// toolEvents turns a tool frame into a readable summary plus its output. The
// summary mirrors what the harness shows in its own TUI: `$ <command>` for a
// shell call, `→ Read <path>` for a file read, and so on.
func toolEvents(scope map[string]any) []Event {
	name := firstString(scope, "tool", "name", "toolName")
	state, _ := scope["state"].(map[string]any)
	input, _ := state["input"].(map[string]any)

	summary := ""
	switch {
	case firstString(input, "command", "cmd") != "":
		summary = "$ " + firstString(input, "command", "cmd")
	case firstString(input, "filePath", "path", "file", "pattern", "query", "url") != "":
		summary = "→ " + titleFirst(name) + " " + firstString(input, "filePath", "path", "file", "pattern", "query", "url")
	case firstString(state, "title") != "":
		summary = "→ " + titleFirst(name) + " " + firstString(state, "title")
	case name != "":
		summary = "→ " + titleFirst(name)
	}

	var out []Event
	if summary != "" {
		out = append(out, Event{Kind: contracts.LogTool, Text: summary, ToolName: name})
	}
	if output := firstString(state, "output", "result"); output != "" {
		out = append(out, Event{Kind: contracts.LogRaw, Text: truncate(strings.TrimRight(output, "\n"), 4000)})
	}
	return out
}

func singleText(kind contracts.LogKind, text string) []Event {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []Event{{Kind: kind, Text: text}}
}

func titleFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n… (truncated)"
}

// usageFromFrame looks for token accounting in either the nested scope or the
// top level. opencode reports usage as part.tokens (+ part.cost) on the
// step-finish frame; other harnesses use a top-level "usage" object.
func usageFromFrame(raw, scope map[string]any) *contracts.TokenUsage {
	if usage := usageFromMap(scope); usage != nil {
		return usage
	}
	if usage := usageFromMap(raw); usage != nil {
		return usage
	}
	tokens, ok := scope["tokens"].(map[string]any)
	if !ok {
		return nil
	}
	usage := &contracts.TokenUsage{
		InputTokens:     intField(tokens, "input", "input_tokens", "inputTokens", "prompt_tokens"),
		OutputTokens:    intField(tokens, "output", "output_tokens", "outputTokens", "completion_tokens"),
		ReasoningTokens: intField(tokens, "reasoning", "reasoning_tokens"),
		CachedTokens:    intField(tokens, "cached", "cached_tokens", "cache_read_input_tokens"),
		CostUSD:         floatField(scope, "cost", "cost_usd", "costUsd", "total_cost_usd"),
		ToolUses:        intField(tokens, "tool_uses", "toolUses"),
	}
	if cache, ok := tokens["cache"].(map[string]any); ok {
		usage.CachedTokens += intField(cache, "read") + intField(cache, "write")
	}
	if *usage == (contracts.TokenUsage{}) {
		return nil
	}
	return usage
}

func usageFromMap(m map[string]any) *contracts.TokenUsage {
	u, ok := m["usage"].(map[string]any)
	if !ok {
		return nil
	}
	usage := contracts.TokenUsage{
		InputTokens:  intField(u, "input_tokens", "inputTokens", "prompt_tokens"),
		OutputTokens: intField(u, "output_tokens", "outputTokens", "completion_tokens"),
		CachedTokens: intField(u, "cached_tokens", "cachedTokens", "cache_read_input_tokens"),
		CostUSD:      floatField(u, "cost_usd", "costUsd", "total_cost_usd"),
		ToolUses:     intField(u, "tool_uses", "toolUses"),
	}
	if usage == (contracts.TokenUsage{}) {
		return nil
	}
	return &usage
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func intField(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return n
			}
		}
	}
	return 0
}

func floatField(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return v
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return f
			}
		}
	}
	return 0
}
