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
		// A non-JSON line is still one complete output line: the scanner
		// stripped its terminator, so restore it. Without it, two adjacent
		// prose lines merge when consumers reassemble the stream, which
		// corrupts printed tool commands next to other output.
		return []Event{{Kind: contracts.LogText, Text: line + "\n"}}
	}

	scope := raw
	if part, ok := raw["part"].(map[string]any); ok {
		scope = part
	}

	frameType := strings.ToLower(firstString(raw, "type", "role", "kind"))

	// Pi streams assistant output as deltas inside message_update and reports
	// token accounting once on the assistant's message_end. Handle those shapes
	// before the generic usage probe: every delta carries cumulative usage, so
	// treating it as a usage frame would both hide the text and overcount spend.
	if frameType == "message_update" {
		delta, _ := raw["assistantMessageEvent"].(map[string]any)
		switch strings.ToLower(firstString(delta, "type")) {
		case "text_delta":
			return singleText(contracts.LogText, firstString(delta, "delta"))
		case "thinking_delta":
			return singleText(contracts.LogReasoning, firstString(delta, "delta"))
		}
		return nil
	}
	if frameType == "message_end" {
		if msg, ok := raw["message"].(map[string]any); ok && firstString(msg, "role") == "assistant" {
			if usage := usageFromValue(msg["usage"]); usage != nil {
				return []Event{{Kind: contracts.LogUsage, Usage: usage}}
			}
		}
		return nil
	}
	if frameType == "tool_execution_start" {
		return executionStart(raw)
	}
	if frameType == "tool_execution_end" {
		return executionEnd(raw)
	}

	if usage := usageFromFrame(raw, scope); usage != nil {
		return []Event{{Kind: contracts.LogUsage, Usage: usage}}
	}

	switch frameType {
	case "tool", "tool_use", "tool_call":
		return toolEvents(scope)
	case "error":
		return singleText(contracts.LogError, firstString(scope, "text", "message", "error"))
	case "stream_event", "message_start", "message_started", "turn_start", "turn_end",
		"agent_start", "agent_end", "session", "step_start", "step-start",
		"step_finish", "step-finish":
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

// executionStart summarizes a Pi tool call. Pi reports the call before it runs,
// with the tool name and its arguments at the top level.
func executionStart(raw map[string]any) []Event {
	name := firstString(raw, "toolName", "tool")
	args, _ := raw["args"].(map[string]any)
	summary := ""
	switch {
	case firstString(args, "command", "cmd") != "":
		summary = "$ " + firstString(args, "command", "cmd")
	case firstString(args, "filePath", "path", "file", "pattern", "query", "url") != "":
		summary = "→ " + titleFirst(name) + " " + firstString(args, "filePath", "path", "file", "pattern", "query", "url")
	case name != "":
		summary = "→ " + titleFirst(name)
	}
	if summary == "" {
		return nil
	}
	return []Event{{Kind: contracts.LogTool, Text: summary, ToolName: name}}
}

// executionEnd emits the tool's result. Pi puts the readable output under
// result.structuredContent.output, with a content-block fallback.
func executionEnd(raw map[string]any) []Event {
	result, _ := raw["result"].(map[string]any)
	output := ""
	if structured, ok := result["structuredContent"].(map[string]any); ok {
		output = firstString(structured, "output", "result")
	}
	if output == "" {
		output = contentText(result["content"])
	}
	if strings.TrimSpace(output) == "" {
		return nil
	}
	return []Event{{Kind: contracts.LogRaw, Text: truncate(strings.TrimRight(output, "\n"), 4000)}}
}

// contentText joins the text blocks of a Pi/Anthropic-style content array.
func contentText(v any) string {
	blocks, ok := v.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		m, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if text := firstString(m, "text"); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
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
	return usageFromValue(m["usage"])
}

// usageFromValue parses the common usage object shapes. Pi reports short keys
// (input/output/cacheRead/cacheWrite/reasoning) and a nested cost object; other
// harnesses use *_tokens keys with a flat cost.
func usageFromValue(v any) *contracts.TokenUsage {
	u, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	usage := contracts.TokenUsage{
		InputTokens:     intField(u, "input", "input_tokens", "inputTokens", "prompt_tokens"),
		OutputTokens:    intField(u, "output", "output_tokens", "outputTokens", "completion_tokens"),
		ReasoningTokens: intField(u, "reasoning", "reasoning_tokens"),
		CachedTokens:    intField(u, "cached", "cached_tokens", "cachedTokens", "cache_read_input_tokens"),
		ToolUses:        intField(u, "tool_uses", "toolUses"),
	}
	if cache, ok := u["cache"].(map[string]any); ok {
		usage.CachedTokens += intField(cache, "read") + intField(cache, "write")
	}
	usage.CachedTokens += intField(u, "cacheRead", "cache_read") + intField(u, "cacheWrite", "cache_write")
	if cost, ok := u["cost"].(map[string]any); ok {
		usage.CostUSD = floatField(cost, "total", "cost", "usd")
	} else {
		usage.CostUSD = floatField(u, "cost_usd", "costUsd", "total_cost_usd")
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
