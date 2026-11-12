package providers

import (
	"encoding/json"
	"sort"
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
	img        ImageSupport
}

// NewGenericDriver constructs a driver for a harness.
func NewGenericDriver(kind contracts.Harness, binary string, invoke InvocationFunc, translator Translator) *GenericDriver {
	return &GenericDriver{kind: kind, binary: binary, invoke: invoke, translator: translator}
}

// WithImageSupport sets the driver's declared image capability. Unset reads as
// unknown, which degrades to a prompt note.
func (g *GenericDriver) WithImageSupport(s ImageSupport) *GenericDriver {
	g.img = s
	return g
}

// ImageSupport reports the declared image capability.
func (g *GenericDriver) ImageSupport() ImageSupport { return g.img }

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

	// Claude's stream-json frames carry a full Anthropic message: content
	// blocks hold text, thinking, and tool calls, while usage rides on
	// message.usage. The final "result" frame aggregates usage for the whole
	// process, so per-message usage is deliberately not emitted — the ledger
	// adds every usage event and would double count.
	if frameType == "assistant" || frameType == "user" {
		if msg, ok := raw["message"].(map[string]any); ok {
			return contentEvents(msg["content"])
		}
	}
	if frameType == "result" {
		var out []Event
		if isErr, _ := raw["is_error"].(bool); isErr {
			if text := firstString(raw, "result", "error"); text != "" {
				out = append(out, Event{Kind: contracts.LogError, Text: text})
			}
		}
		if usage := usageFromFrame(raw, raw); usage != nil {
			out = append(out, Event{Kind: contracts.LogUsage, Usage: usage})
		}
		return out
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
		"step_finish", "step-finish", "system", "rate_limit_event":
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
// shell call, `→ Read <path>` for a file read, followed by every argument the
// harness sent that the summary does not already show.
func toolEvents(scope map[string]any) []Event {
	name := firstString(scope, "tool", "name", "toolName")
	state, _ := scope["state"].(map[string]any)
	input, _ := state["input"].(map[string]any)
	// As with the rest of this translator, tolerate harnesses that put the
	// call's arguments at the frame's top level instead of under state.
	if len(input) == 0 {
		input, _ = scope["args"].(map[string]any)
	}
	if len(input) == 0 {
		input, _ = scope["input"].(map[string]any)
	}

	summary := toolText(name, input, firstString(state, "title"))
	var out []Event
	if summary != "" {
		out = append(out, Event{Kind: contracts.LogTool, Text: summary, ToolName: name})
	}
	if output := firstString(state, "output", "result"); output != "" {
		out = append(out, Event{Kind: contracts.LogRaw, Text: truncate(strings.TrimRight(output, "\n"), 4000)})
	}
	return out
}

// toolText renders a tool call for the log: the harness-style summary, then the
// remaining arguments as one "key: value" line each. Shell commands stay on the
// summary line verbatim, so a multi-line command keeps its shape in the
// monospace log.
func toolText(name string, args map[string]any, title string) string {
	summary, shown := toolSummary(name, args, title)
	if summary == "" {
		return ""
	}
	return summary + toolDetail(args, shown)
}

// toolSummary names a call the way the harness TUI does and reports the
// argument keys already visible on that line.
func toolSummary(name string, args map[string]any, title string) (string, []string) {
	if command, key := pickString(args, "command", "cmd"); command != "" {
		return "$ " + command, []string{key}
	}
	if target, key := pickString(args, "file_path", "filePath", "path", "file", "pattern", "query", "url"); target != "" {
		return "→ " + titleFirst(name) + " " + target, []string{key}
	}
	if title != "" {
		return "→ " + titleFirst(name) + " " + title, nil
	}
	if name != "" {
		return "→ " + titleFirst(name), nil
	}
	return "", nil
}

// toolDetail lists the arguments a summary does not show, sorted by key so the
// same call always renders the same way. Values stay verbatim (multi-line
// commands and prompts keep their shape); long values are truncated so a Write
// payload cannot flood the log.
func toolDetail(args map[string]any, shown []string) string {
	if len(args) == 0 {
		return ""
	}
	skip := make(map[string]bool, len(shown))
	for _, key := range shown {
		skip[key] = true
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		if !skip[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		value := toolValue(args[key])
		if value == "" {
			continue
		}
		b.WriteString("\n  ")
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(value)
	}
	return truncate(b.String(), 2000)
}

// toolValue renders one argument value: strings verbatim, everything else as
// compact JSON.
func toolValue(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		if strings.TrimSpace(value) == "" {
			return ""
		}
		return truncate(strings.TrimRight(value, "\n"), 800)
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return truncate(string(raw), 800)
	}
}

// executionStart summarizes a Pi tool call. Pi reports the call before it runs,
// with the tool name and its arguments at the top level.
func executionStart(raw map[string]any) []Event {
	name := firstString(raw, "toolName", "tool")
	args, _ := raw["args"].(map[string]any)
	summary := toolText(name, args, "")
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

// contentEvents turns an Anthropic-style content array into events. Claude puts
// text, thinking, tool calls, and tool results in this shape.
func contentEvents(v any) []Event {
	if text, ok := v.(string); ok {
		return singleText(contracts.LogText, text)
	}
	blocks, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []Event
	for _, block := range blocks {
		m, ok := block.(map[string]any)
		if !ok {
			continue
		}
		switch strings.ToLower(firstString(m, "type")) {
		case "text", "output_text":
			out = append(out, singleText(contracts.LogText, firstString(m, "text"))...)
		case "thinking", "reasoning":
			out = append(out, singleText(contracts.LogReasoning,
				firstString(m, "thinking", "text", "reasoning"))...)
		case "tool_use", "tool_call":
			out = append(out, toolUseEvent(m)...)
		case "tool_result":
			out = append(out, toolResultEvent(m)...)
		}
	}
	return out
}

// toolUseEvent summarizes an Anthropic tool_use block the way the harness
// renders it: `$ <command>` for a shell call, `→ Read <path>` for a file, plus
// any remaining arguments.
func toolUseEvent(block map[string]any) []Event {
	name := firstString(block, "name", "tool")
	input, _ := block["input"].(map[string]any)
	summary := toolText(name, input, "")
	if summary == "" {
		return nil
	}
	return []Event{{Kind: contracts.LogTool, Text: summary, ToolName: name}}
}

func toolResultEvent(block map[string]any) []Event {
	text := ""
	switch content := block["content"].(type) {
	case string:
		text = content
	case []any:
		text = contentText(content)
	}
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	kind := contracts.LogRaw
	if isErr, _ := block["is_error"].(bool); isErr {
		kind = contracts.LogError
	}
	return []Event{{Kind: kind, Text: truncate(text, 4000)}}
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
		addFrameCost(usage, scope)
		return usage
	}
	if usage := usageFromMap(raw); usage != nil {
		addFrameCost(usage, raw)
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

// addFrameCost picks up cost reported beside a usage object rather than inside
// it, as Claude's result frame does with total_cost_usd.
func addFrameCost(u *contracts.TokenUsage, frame map[string]any) {
	if u.CostUSD == 0 {
		u.CostUSD = floatField(frame, "total_cost_usd", "cost_usd", "costUsd")
	}
}

// usageFromValue parses the common usage object shapes. Pi reports short keys
// (input/output/cacheRead/cacheWrite/reasoning) and a nested cost object; other
// harnesses use *_tokens keys with a flat cost. Claude separates cache reads
// from cache writes, so both count as cached tokens.
func usageFromValue(v any) *contracts.TokenUsage {
	u, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	usage := contracts.TokenUsage{
		InputTokens:     intField(u, "input", "input_tokens", "inputTokens", "prompt_tokens"),
		OutputTokens:    intField(u, "output", "output_tokens", "outputTokens", "completion_tokens"),
		ReasoningTokens: intField(u, "reasoning", "reasoning_tokens"),
		CachedTokens: intField(u, "cached", "cached_tokens", "cachedTokens",
			"cache_read_input_tokens", "cacheReadInputTokens"),
		ToolUses: intField(u, "tool_uses", "toolUses"),
	}
	usage.CachedTokens += intField(u, "cache_creation_input_tokens", "cacheCreationInputTokens")
	if details, ok := u["output_tokens_details"].(map[string]any); ok {
		usage.ReasoningTokens += intField(details, "thinking_tokens", "reasoning_tokens")
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
	value, _ := pickString(m, keys...)
	return value
}

// pickString returns the first non-empty string among keys, plus the key it
// came from so callers can skip that argument when listing the rest.
func pickString(m map[string]any, keys ...string) (string, string) {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v, k
		}
	}
	return "", ""
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
