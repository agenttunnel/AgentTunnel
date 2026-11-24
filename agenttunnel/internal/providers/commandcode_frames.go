package providers

import (
	"encoding/json"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// CommandCodeTranslator maps `commandcode -p --output-format json` NDJSON.
//
// The CLI emits "event" frames for tool progress (`{"type":"event","event":{...}}`)
// and one final "result" frame that carries the answer in finalText. There are
// no streamed text deltas, so the answer must come from the result frame. Any
// other frame, or a non-JSON line, goes through the generic translator.
type CommandCodeTranslator struct{}

func (CommandCodeTranslator) Translate(line string) []Event {
	var raw map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &raw); err != nil {
		return JSONLineTranslator{}.Translate(line)
	}
	switch firstString(raw, "type") {
	case "event":
		ev, _ := raw["event"].(map[string]any)
		return commandCodeToolEvent(ev)
	case "result":
		return commandCodeResult(raw)
	}
	return JSONLineTranslator{}.Translate(line)
}

// commandCodeToolEvent shows a tool as it starts, with its arguments, and
// closes the same call when the CLI reports its outcome. The arguments arrive
// on "tool_queued" (its `input`); "tool_running" carries only the name and a
// nullable description, so it would show an empty card. Other event types are
// not user-visible and are ignored.
func commandCodeToolEvent(ev map[string]any) []Event {
	switch firstString(ev, "type") {
	case "tool_queued":
		return commandCodeToolStart(ev)
	case "tool_completed":
		return toolResult(firstString(ev, "toolCallId"), false, commandCodeOutput(ev["result"]), nil)
	case "tool_errored":
		return toolResult(firstString(ev, "toolCallId"), true, firstString(ev, "error"), nil)
	}
	return nil
}

func commandCodeToolStart(ev map[string]any) []Event {
	name := firstString(ev, "toolName")
	if name == "" {
		return nil
	}
	summary := "→ " + titleFirst(name)
	if desc := firstString(ev, "description"); desc != "" {
		summary += " " + desc
	}
	args, _ := ev["args"].(map[string]any)
	if len(args) == 0 {
		args, _ = ev["input"].(map[string]any)
	}
	call := toolCall(firstString(ev, "toolCallId"), name, args, contracts.ToolRunning)
	return []Event{{Kind: contracts.LogTool, Text: summary + toolDetail(args, []string{"description"}), ToolName: name, Tool: call}}
}

// commandCodeOutput joins a completed tool's text blocks into its output.
func commandCodeOutput(result any) string {
	blocks, _ := result.([]any)
	var parts []string
	for _, b := range blocks {
		if m, ok := b.(map[string]any); ok {
			if text := firstString(m, "text"); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// commandCodeResult emits the final answer (or the failure) and the run's usage.
// finalText is empty on error, so the error field carries the reason.
func commandCodeResult(raw map[string]any) []Event {
	var out []Event
	if firstString(raw, "subtype") == "error" {
		text := firstString(raw, "error")
		if m, ok := raw["error"].(map[string]any); ok {
			text = firstString(m, "message", "error")
		}
		if text == "" {
			text = "Command Code run failed"
		}
		out = append(out, Event{Kind: contracts.LogError, Text: text})
	} else {
		out = append(out, singleText(contracts.LogText, firstString(raw, "finalText"))...)
	}
	if usage := usageFromFrame(raw, raw); usage != nil {
		out = append(out, Event{Kind: contracts.LogUsage, Usage: usage})
	}
	return out
}
