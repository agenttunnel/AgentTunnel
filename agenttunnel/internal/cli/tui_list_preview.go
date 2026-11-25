package cli

import (
	"fmt"
	"strings"
)

// tuiPreviewMinCols is the narrowest list that still has room for the preview
// beside it when the user asked for it with ctrl+b.
const tuiPreviewMinCols = 90

// tuiModeLabel words how a thread runs: one agent on a harness, or the team.
func tuiModeLabel(row threadListRow) string {
	if rt := row.SoloRuntime; rt != nil {
		return "solo · " + tuiRuntime(string(rt.Harness), rt.Model, rt.Variant)
	}
	return "orchestrator team"
}

// tuiSoloBadge marks a solo thread in the harness's own color, with a space
// after it; other threads get no badge at all.
func tuiSoloBadge(row threadListRow) tuiRow {
	rt := row.SoloRuntime
	if rt == nil {
		return nil
	}
	return tuiText("◇ solo ", tuiHarnessInk(string(rt.Harness)))
}

// previewShown reports whether the thread list shows the selected thread's
// preview beside it. It follows the same ctrl+b choice the thread's sidebar
// does: wide terminals show it until hidden, narrower ones only on request.
func (m *tuiModel) previewShown(cols, rows int) bool {
	return m.sideShown(cols, rows) && cols >= tuiPreviewMinCols
}

// previewRows describes one thread in width cells: what it is doing, how it
// runs, where its work lives, and the prompt it started from.
func (m *tuiModel) previewRows(row psRow, width int) []tuiRow {
	th := row.thread
	title := th.Title
	if title == "" {
		title = "(untitled)"
	}
	status := row.status
	if status == "" {
		status = threadRowStatus(th)
	}
	out := wrapRows(title, width, tuiInk{fg: tuiColText, bold: true})
	if len(out) > 2 {
		out = out[:2]
	}
	out = append(out,
		tuiText("● ", tuiStatusInk(status)).join(
			tuiText(status, tuiStatusInk(status)),
			tuiText("  "+psAgentCount(th.RunningAgents, th.Agents)+" agents", tuiInk{fg: tuiColDim})),
		nil,
		tuiHeading("Mode"))
	if rt := th.SoloRuntime; rt != nil {
		out = append(out, tuiText("◇ solo", tuiHarnessInk(string(rt.Harness))),
			tuiFaint("  "+tuiRuntime(string(rt.Harness), rt.Model, rt.Variant)))
	} else {
		out = append(out, tuiText("orchestrator team", tuiInk{fg: tuiColText}))
	}
	out = append(out, nil, tuiHeading("Work"),
		tuiKV("project", row.project),
		tuiKV("updated", relativeAgo(th.UpdatedAt, m.now)),
		tuiKV("id     ", shortThreadID(th.ID)))
	if wt := th.Worktree; wt != nil && wt.Branch != "" {
		out = append(out, tuiKV("branch ", wt.Branch))
		if wt.Base != "" {
			out = append(out, tuiFaint("        from "+wt.Base))
		}
	}
	if th.Archived {
		out = append(out, tuiFaint("archived"))
	}
	if th.FirstPrompt != "" {
		out = append(out, nil, tuiHeading("Started from"))
		prompt := wrapRows(strings.Join(strings.Fields(th.FirstPrompt), " "), width, tuiInk{fg: tuiColDim})
		if len(prompt) > 8 {
			prompt = append(prompt[:7], tuiFaint(fmt.Sprintf("… %d more lines", len(prompt)-7)))
		}
		out = append(out, prompt...)
	}
	for i := range out {
		out[i] = out[i].fit(width)
	}
	return out
}

// beside puts side to the right of body, separated by a rule. body is padded
// to mainCols and side to sideW; both must be rows tall.
func tuiBeside(body []tuiRow, mainCols int, side []tuiRow, sideW int) []tuiRow {
	rule := tuiText("│", tuiInk{fg: tuiColBorder})
	pad := tuiText(" ", tuiInk{})
	side = fitRows(side, len(body))
	for i := range body {
		body[i] = body[i].padTo(mainCols, tuiInk{}).join(rule, pad, side[i].fit(sideW).padTo(sideW, tuiInk{}))
	}
	return body
}
