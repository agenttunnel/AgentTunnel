package providers

import (
	"encoding/json"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// CodexTranslator maps `codex exec --json` JSONL.
//
// Work arrives as items: item.started when a command or tool call begins,
// item.completed when it (or a message) is done. Token usage rides once on
// turn.completed. Anything else, including non-JSON lines, goes through the
// generic translator.
type CodexTranslator struct{}

func (CodexTranslator) Translate(line string) []Event {
	var raw map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &raw); err != nil {
		return JSONLineTranslator{}.Translate(line)
	}
	if msg, ok := raw["msg"].(map[string]any); ok {
		return codexLegacy(msg)
	}
	switch firstString(raw, "type") {
	case "item.started", "item.updated":
		item, _ := raw["item"].(map[string]any)
		return codexItemStarted(item, firstString(raw, "type") == "item.updated")
	case "item.completed":
		item, _ := raw["item"].(map[string]any)
		return codexItemCompleted(item)
	case "turn.completed":
		if usage := codexUsage(raw["usage"]); usage != nil {
			return []Event{{Kind: contracts.LogUsage, Usage: usage}}
		}
		return nil
	case "turn.failed":
		errObj, _ := raw["error"].(map[string]any)
		return singleText(contracts.LogError, firstString(errObj, "message"))
	case "error":
		return singleText(contracts.LogError, firstString(raw, "message"))
	case "thread.started", "turn.started":
		return nil
	}
	return JSONLineTranslator{}.Translate(line)
}

// codexLegacy reads the {"id","msg"} envelope that Codex releases before the
// item schema printed for --json. Only whole messages, commands and errors are
// shown; deltas and bookkeeping events are dropped.
func codexLegacy(msg map[string]any) []Event {
	switch firstString(msg, "type") {
	case "agent_message":
		return singleText(contracts.LogText, firstString(msg, "message"))
	case "agent_reasoning":
		return singleText(contracts.LogReasoning, firstString(msg, "text"))
	case "exec_command_begin":
		var parts []string
		if argv, ok := msg["command"].([]any); ok {
			for _, a := range argv {
				if s, ok := a.(string); ok {
					parts = append(parts, s)
				}
			}
		}
		command := strings.Join(parts, " ")
		id := firstString(msg, "call_id")
		return []Event{{
			Kind: contracts.LogTool, Text: "$ " + command, ToolName: "shell",
			Tool: toolCall(id, "shell", map[string]any{"command": command}, contracts.ToolRunning),
		}}
	case "exec_command_end":
		var exit *int
		failed := false
		if code, ok := msg["exit_code"].(float64); ok {
			n := int(code)
			exit, failed = &n, n != 0
		}
		output := firstString(msg, "aggregated_output", "formatted_output")
		if output == "" {
			output = firstString(msg, "stdout") + firstString(msg, "stderr")
		}
		return toolResult(firstString(msg, "call_id"), failed, output, exit)
	case "error", "stream_error":
		return singleText(contracts.LogError, firstString(msg, "message"))
	}
	return nil
}

// codexItemStarted shows a call as it begins. Only the first sighting of a
// call is shown; item.updated repeats it while it runs.
func codexItemStarted(item map[string]any, update bool) []Event {
	id := firstString(item, "id")
	switch firstString(item, "type") {
	case "command_execution":
		if update {
			return nil
		}
		command := firstString(item, "command")
		return []Event{{
			Kind: contracts.LogTool, Text: "$ " + command, ToolName: "shell",
			Tool: &contracts.ToolCall{ID: id, Name: "shell", Command: command, Status: contracts.ToolRunning},
		}}
	case "mcp_tool_call":
		if update {
			return nil
		}
		name := codexMCPName(item)
		args, _ := item["arguments"].(map[string]any)
		return []Event{{
			Kind: contracts.LogTool, Text: "→ " + name + toolDetail(args, nil), ToolName: name,
			Tool: &contracts.ToolCall{ID: id, Name: name, Status: contracts.ToolRunning},
		}}
	case "todo_list":
		return codexTodos(item)
	}
	return nil
}

// codexItemCompleted emits a finished item: a message, reasoning, or the
// outcome of a call started earlier. File changes and web searches are only
// reported finished, so they arrive as a single structured tool line.
func codexItemCompleted(item map[string]any) []Event {
	id := firstString(item, "id")
	failed := strings.EqualFold(firstString(item, "status"), "failed") ||
		strings.EqualFold(firstString(item, "status"), "declined")
	switch firstString(item, "type") {
	case "agent_message":
		return singleText(contracts.LogText, firstString(item, "text"))
	case "reasoning":
		return singleText(contracts.LogReasoning, firstString(item, "text"))
	case "command_execution":
		var exit *int
		if code, ok := item["exit_code"].(float64); ok {
			n := int(code)
			exit = &n
			failed = failed || n != 0
		}
		return toolResult(id, failed, firstString(item, "aggregated_output"), exit)
	case "mcp_tool_call":
		output := ""
		if errObj, ok := item["error"].(map[string]any); ok {
			output, failed = firstString(errObj, "message"), true
		} else if result, ok := item["result"].(map[string]any); ok {
			output = contentText(result["content"])
		}
		return toolResult(id, failed, output, nil)
	case "file_change":
		return codexFileChange(id, item, failed)
	case "web_search":
		query := firstString(item, "query")
		return []Event{{
			Kind: contracts.LogTool, Text: "→ Search " + query, ToolName: "web_search",
			Tool: &contracts.ToolCall{ID: id, Name: "web_search", Status: contracts.ToolDone},
		}}
	case "error":
		return singleText(contracts.LogError, firstString(item, "message"))
	}
	return nil
}

// codexFileChange lists a patch's files. Codex's exec stream names the files
// and how each changed, but not the hunks, so there is no diff.
func codexFileChange(id string, item map[string]any, failed bool) []Event {
	changes, _ := item["changes"].([]any)
	var lines []string
	path := ""
	for _, c := range changes {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		p := firstString(m, "path")
		if path == "" {
			path = p
		}
		verb := "Edit"
		switch firstString(m, "kind") {
		case "add":
			verb = "Add"
		case "delete":
			verb = "Delete"
		}
		lines = append(lines, "→ "+verb+" "+p)
	}
	if len(lines) == 0 {
		return nil
	}
	status := contracts.ToolDone
	if failed {
		status = contracts.ToolFailed
	}
	return []Event{{
		Kind: contracts.LogTool, Text: strings.Join(lines, "\n"), ToolName: "apply_patch",
		Tool: &contracts.ToolCall{ID: id, Name: "apply_patch", Path: path, Status: status},
	}}
}

func codexMCPName(item map[string]any) string {
	server, tool := firstString(item, "server"), firstString(item, "tool")
	if server == "" {
		return tool
	}
	return server + "." + tool
}

// codexTodos renders the agent's plan as a checklist. It is not a call, so it
// carries no structure.
func codexTodos(item map[string]any) []Event {
	items, _ := item["items"].([]any)
	var b strings.Builder
	b.WriteString("→ Plan")
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		mark := "[ ]"
		if done, _ := m["completed"].(bool); done {
			mark = "[x]"
		}
		b.WriteString("\n  " + mark + " " + firstString(m, "text"))
	}
	return []Event{{Kind: contracts.LogTool, Text: b.String(), ToolName: "todo_list"}}
}

// codexUsage reads turn.completed usage. OpenAI counts cached input inside
// input_tokens; the ledger keeps them apart, as Claude reports them.
func codexUsage(v any) *contracts.TokenUsage {
	u, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	cached := intField(u, "cached_input_tokens")
	usage := contracts.TokenUsage{
		InputTokens:  intField(u, "input_tokens") - cached,
		OutputTokens: intField(u, "output_tokens"),
		CachedTokens: cached,
	}
	if usage.InputTokens < 0 {
		usage.InputTokens = 0
	}
	if usage == (contracts.TokenUsage{}) {
		return nil
	}
	return &usage
}
