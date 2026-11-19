package providers

import (
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

const (
	// toolOutputCap bounds a tool result line, structured or not.
	toolOutputCap = 4000
	// toolDiffCap bounds an edit's diff: a whole-file Write must not ride the
	// WebSocket in full.
	toolDiffCap = 16 << 10
)

// toolCall is the structured side of a tool call, built from the same
// arguments its summary line shows. A harness with no call id gets none: a
// result could not be folded into it.
func toolCall(id, name string, args map[string]any, status contracts.ToolStatus) *contracts.ToolCall {
	if id == "" {
		return nil
	}
	command, _ := pickString(args, "command", "cmd")
	path, _ := pickString(args, "file_path", "filePath", "path", "file")
	return &contracts.ToolCall{
		ID:      id,
		Name:    name,
		Command: command,
		Path:    path,
		Status:  status,
		Diff:    editDiff(name, args),
	}
}

// toolResult is a call's outcome: the output as its plain line, with the
// final status attached when the call has an id. A structured result always
// has text, so the status reaches clients even when the tool printed nothing.
func toolResult(id string, failed bool, output string, exit *int) []Event {
	kind, status := contracts.LogRaw, contracts.ToolDone
	if failed {
		kind, status = contracts.LogError, contracts.ToolFailed
	}
	text := truncate(strings.TrimRight(output, "\n"), toolOutputCap)
	if id == "" {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []Event{{Kind: kind, Text: text}}
	}
	if strings.TrimSpace(text) == "" {
		text = "(no output)"
	}
	return []Event{{Kind: kind, Text: text, Tool: &contracts.ToolCall{ID: id, Status: status, ExitCode: exit}}}
}

// editDiff renders an edit's arguments as diff lines: old/new string pairs
// (Claude's Edit and MultiEdit, opencode's edit, Pi's edit) and a whole-file
// Write. Other tools have no diff.
func editDiff(name string, args map[string]any) string {
	var hunks []string
	if edits, ok := args["edits"].([]any); ok {
		for _, e := range edits {
			if m, ok := e.(map[string]any); ok {
				hunks = append(hunks, pairDiff(m)...)
			}
		}
	} else {
		hunks = pairDiff(args)
	}
	if len(hunks) == 0 && strings.EqualFold(name, "write") {
		if content, ok := args["content"].(string); ok {
			hunks = append(hunks, lineDiff("", content))
		}
	}
	return capDiff(strings.Join(hunks, "\n@@\n"))
}

func pairDiff(m map[string]any) []string {
	old, _ := pickString(m, "old_string", "oldString", "oldText")
	updated, _ := pickString(m, "new_string", "newString", "newText")
	if old == "" && updated == "" {
		return nil
	}
	return []string{lineDiff(old, updated)}
}

// lineDiff shows a replacement as removed then added lines. Lines both sides
// share at the start and end are context, so an edit that quotes surrounding
// code to anchor itself still reads as the change it made.
func lineDiff(old, updated string) string {
	a := splitLines(old)
	b := splitLines(updated)
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var out []string
	for _, l := range a[:pre] {
		out = append(out, " "+l)
	}
	for _, l := range a[pre : len(a)-suf] {
		out = append(out, "-"+l)
	}
	for _, l := range b[pre : len(b)-suf] {
		out = append(out, "+"+l)
	}
	for _, l := range a[len(a)-suf:] {
		out = append(out, " "+l)
	}
	return strings.Join(out, "\n")
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// capDiff cuts a diff on a line boundary and marks the cut.
func capDiff(diff string) string {
	if len(diff) <= toolDiffCap {
		return diff
	}
	cut := strings.LastIndexByte(diff[:toolDiffCap], '\n')
	if cut < 0 {
		cut = toolDiffCap
	}
	return diff[:cut] + "\n… (diff truncated)"
}
