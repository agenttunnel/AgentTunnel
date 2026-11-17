package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// The thread screen's right pane: usage, the thread's agents, and the role
// runtimes. It appears only when the terminal is wide enough to leave the chat
// its room, and its data loads as a background job so painting never waits.

const (
	tuiSideWidth   = 36
	tuiSideMinCols = 110
	tuiSideTTL     = 20 * time.Second
)

type tuiSide struct {
	tuiBase
	ledger contracts.ThreadUsage
	limits []contracts.HarnessLimits
	cfg    tuiSettingsWire
	loaded bool
}

// tuiLoadSide queues a refresh of the sidebar's data; the ttl keeps it cheap
// to call on every loop pass.
func tuiLoadSide(m *tuiModel) {
	if m.screen != tuiScreenThread {
		return
	}
	tuiLoadJob(m, &m.side.tuiBase, tuiSideTTL, func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var (
			ledger contracts.ThreadUsage
			limits []contracts.HarnessLimits
			cfg    tuiSettingsWire
		)
		if c.Call(ctx, "usage.get", nil, &ledger) != nil ||
			c.Call(ctx, "settings.get", nil, &cfg) != nil {
			return nil
		}
		_ = c.Call(ctx, "usage.limits", map[string]bool{"refresh": false}, &limits)
		return func(m *tuiModel) {
			m.side.ledger, m.side.limits, m.side.cfg, m.side.loaded = ledger, limits, cfg, true
		}
	})
}

// sideRows is the pane's content, width cells wide.
func (m *tuiModel) sideRows(width int) []tuiRow {
	var out []tuiRow
	heading := func(s string) {
		if len(out) > 0 {
			out = append(out, nil)
		}
		out = append(out, tuiHeading(s))
	}

	heading("Usage")
	if !m.side.loaded {
		out = append(out, tuiFaint("loading…"))
	} else {
		out = append(out, tuiKV("tokens", usageTokenCount(m.side.ledger.Total().Total())),
			tuiKV("cost  ", usageCost(m.side.ledger.Total().CostUSD)))
		for _, h := range m.side.limits {
			if !h.Available || len(h.Windows) == 0 {
				continue
			}
			out = append(out, tuiText(string(h.Harness), tuiHarnessInk(string(h.Harness))))
			for _, w := range h.Windows {
				label := fmt.Sprintf("%.0f%% %s", w.UsedPercent, usageDash(w.Label))
				out = append(out, tuiMeter(width, w.UsedPercent, label))
			}
		}
	}

	heading("Agents")
	if len(m.roster) == 0 {
		out = append(out, tuiFaint("none"))
	}
	out = append(out, m.agentTreeRows(width)...)

	heading("Roles")
	if !m.side.loaded {
		out = append(out, tuiFaint("loading…"))
		return out
	}
	if l := m.side.cfg.Lead; l.Harness != "" || l.Model != "" {
		out = append(out, tuiText("lead", tuiInk{fg: tuiColText}))
		out = append(out, tuiFaint("  "+tuiRuntime(l.Harness, l.Model, l.Variant)))
	}
	for _, r := range m.side.cfg.Roles {
		name := r.Name
		if name == "" {
			name = r.ID
		}
		ink := tuiInk{fg: tuiColText}
		if !r.Enabled {
			ink = tuiInk{fg: tuiColFaint}
		}
		out = append(out, tuiText(name, ink))
		out = append(out, tuiFaint("  "+tuiRuntime(r.Harness, r.Model, r.Variant)))
	}
	return out
}

// agentTreeRows is the thread's agents as a tree: each one under the agent
// that spawned it, with its status and — while it works — how long it has
// been at it. Siblings sort by id so the tree is stable between renders.
func (m *tuiModel) agentTreeRows(width int) []tuiRow {
	children := map[string][]string{}
	for id := range m.roster {
		parent := m.parent[id]
		if _, known := m.roster[parent]; !known || parent == id {
			parent = ""
		}
		children[parent] = append(children[parent], id)
	}
	for _, ids := range children {
		sort.Strings(ids)
	}
	var out []tuiRow
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		// A parent chain that loops back on itself would never end; the
		// roster's size bounds any honest depth.
		if depth > len(m.roster) {
			return
		}
		for _, id := range children[parent] {
			status := m.roster[id]
			lead := strings.Repeat("  ", depth)
			if depth > 0 {
				lead = strings.Repeat("  ", depth-1) + "└ "
			}
			row := tuiText(lead, tuiInk{fg: tuiColBorder}).join(
				tuiText("● ", tuiStatusInk(string(status))),
				tuiText(id, tuiHarnessInk(m.harness[id])))
			detail := string(status)
			if status == contracts.AgentRunning || status == contracts.AgentPending {
				if at := m.started[id]; !at.IsZero() {
					detail += " " + tuiElapsed(m.now.Sub(at))
				}
			}
			row = row.join(tuiText("  "+detail, tuiStatusInk(string(status))))
			out = append(out, row.fit(width))
			if role := m.role[id]; role != "" && role != id {
				out = append(out, tuiFaint(strings.Repeat("  ", depth+1)+role).fit(width))
			}
			walk(id, depth+1)
		}
	}
	walk("", 0)
	return out
}

func tuiRuntime(harness, model, variant string) string {
	s := usageDash(harness) + "/" + usageDash(model)
	if variant != "" {
		s += " · " + variant
	}
	return s
}

// withSidebar puts the pane beside a thread body built for cols-tuiSideWidth-1
// cells. Callers check sideShown first.
func (m *tuiModel) withSidebar(body []tuiRow, mainCols, rows int) []tuiRow {
	side := fitRows(m.sideRows(tuiSideWidth-2), rows)
	rule := tuiText("│", tuiInk{fg: tuiColBorder})
	pad := tuiText(" ", tuiInk{})
	for i := range body {
		body[i] = body[i].padTo(mainCols, tuiInk{}).join(rule, pad, side[i].fit(tuiSideWidth-2))
	}
	return body
}

func (m *tuiModel) sideShown(cols, rows int) bool {
	return cols >= tuiSideMinCols && rows >= 8
}
