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

// commandCodeToolEvent shows a tool as it starts, with its arguments. Other
// event types are not user-visible and are ignored, as the CLI documents.
func commandCodeToolEvent(ev map[string]any) []Event {
	if firstString(ev, "type") != "tool_running" {
		return nil
	}
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
	return []Event{{Kind: contracts.LogTool, Text: summary + toolDetail(args, []string{"description"}), ToolName: name}}
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
