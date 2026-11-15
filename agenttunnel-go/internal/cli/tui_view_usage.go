package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// The Usage tab: what the environment has spent, the subscription windows each
// harness reports as meters, and the per-harness and per-model rollups. It is
// read-only; r asks the providers for fresh limits instead of their cache.

const tuiViewUsage = "usage"

// tuiUsageHarness mirrors domain.HarnessStat.
type tuiUsageHarness struct {
	Harness   string               `json:"harness"`
	Threads   int                  `json:"threads"`
	Agents    int                  `json:"agents"`
	Subagents int                  `json:"subagents"`
	Usage     contracts.TokenUsage `json:"usage"`
}

// tuiUsageModel mirrors domain.ModelStat, the fields the rollup shows.
type tuiUsageModel struct {
	Harness   string               `json:"harness"`
	Model     string               `json:"model"`
	Subagents int                  `json:"subagents"`
	Usage     contracts.TokenUsage `json:"usage"`
}

type tuiUsageState struct {
	tuiBase
	ledger    contracts.ThreadUsage
	limits    []contracts.HarnessLimits
	harness   []tuiUsageHarness
	models    []tuiUsageModel
	fetchedAt time.Time
}

func tuiUsageOf(m *tuiModel) *tuiUsageState {
	return tuiStateOf[tuiUsageState](m, tuiViewUsage)
}

// fetch reads the ledger and the rollups; limits come from the provider cache
// unless refresh asks for a live read.
func (st *tuiUsageState) fetch(refresh bool) error {
	var ledger contracts.ThreadUsage
	if err := st.call("usage.get", nil, &ledger); err != nil {
		return err
	}
	var limits []contracts.HarnessLimits
	if err := st.call("usage.limits", map[string]bool{"refresh": refresh}, &limits); err != nil {
		return err
	}
	var harness []tuiUsageHarness
	if err := st.call("stats.harnesses", nil, &harness); err != nil {
		return err
	}
	var models []tuiUsageModel
	if err := st.call("stats.models", nil, &models); err != nil {
		return err
	}
	st.ledger, st.limits, st.harness, st.models = ledger, limits, harness, models
	st.fetchedAt = time.Now()
	return nil
}

// lines is the whole view as rows, in reading order.
func (st *tuiUsageState) lines(width int) []tuiRow {
	var out []tuiRow
	heading := func(s string) {
		if len(out) > 0 {
			out = append(out, nil)
		}
		out = append(out, tuiHeading(s))
	}

	total := st.ledger.Total()
	heading("Spend")
	out = append(out, tuiKV("  orchestrator", tuiUsageLine(st.ledger.Orchestrator)))
	out = append(out, tuiKV("  subagents   ", tuiUsageLine(st.ledger.Subagents)))
	out = append(out, tuiKV("  total       ", tuiUsageLine(total)))

	heading("Limits")
	if len(st.limits) == 0 {
		out = append(out, tuiFaint("  no harness reported subscription limits"))
	}
	for _, h := range st.limits {
		name := tuiText("  "+string(h.Harness)+"  ", tuiHarnessInk(string(h.Harness)))
		if !h.Available || len(h.Windows) == 0 {
			reason := h.Reason
			if reason == "" {
				reason = "no windows reported"
			}
			out = append(out, name.join(tuiFaint("unmetered · "+reason)))
			continue
		}
		out = append(out, name)
		for _, w := range h.Windows {
			label := fmt.Sprintf("%.0f%%  %s", w.UsedPercent, usageResetAt(w.ResetsAt))
			out = append(out, tuiText("    "+usageDash(w.Label)+"  ", tuiInk{fg: tuiColDim}).
				join(tuiMeter(min(width-24, 40), w.UsedPercent, label)))
		}
	}

	heading("By harness")
	if len(st.harness) == 0 {
		out = append(out, tuiFaint("  no runs recorded"))
	}
	for _, h := range st.harness {
		out = append(out, tuiText("  "+h.Harness, tuiHarnessInk(h.Harness)).
			join(tuiText(fmt.Sprintf("  %d threads  %d agents  %d subagents  ", h.Threads, h.Agents, h.Subagents), tuiInk{fg: tuiColDim}),
				tuiText(tuiUsageLine(h.Usage), tuiInk{fg: tuiColText})))
	}

	heading("By model")
	for _, mdl := range st.models {
		label := mdl.Harness + "/" + usageDash(mdl.Model)
		out = append(out, tuiText("  "+label, tuiInk{fg: tuiColText}).
			join(tuiText(fmt.Sprintf("  %d subagents  ", mdl.Subagents), tuiInk{fg: tuiColDim}),
				tuiText(tuiUsageLine(mdl.Usage), tuiInk{fg: tuiColText})))
	}

	if len(st.ledger.ByAgent) > 0 {
		heading("By agent")
		for _, a := range st.ledger.ByAgent {
			out = append(out, tuiText("  "+string(a.AgentID), tuiInk{fg: tuiColText}).
				join(tuiText("  "+usageDash(string(a.Role))+"  ", tuiInk{fg: tuiColDim}),
					tuiText(tuiUsageLine(a.Usage), tuiInk{fg: tuiColText})))
		}
	}
	return out
}

// tuiUsageLine is one rollup's tokens and cost on a line.
func tuiUsageLine(u contracts.TokenUsage) string {
	return fmt.Sprintf("%s tokens  %s", usageTokenCount(u.Total()), usageCost(u.CostUSD))
}

func (st *tuiUsageState) render(cols, rows int) []tuiRow {
	width := cols
	if rows >= 3 && cols >= 6 {
		width = cols - 4
	}
	lines := st.lines(width)
	off := min(max(st.cursor, 0), max(len(lines)-1, 0))
	return tuiViewFrame(cols, rows, "Usage", lines[off:], -1, st.status())
}

func (st *tuiUsageState) key(k tuiKey) bool {
	st.touch(k)
	if step := tuiNav(k, 8); step != 0 {
		st.cursor = max(st.cursor+step, 0)
		return true
	}
	if k.Kind == keyRune && k.Rune == 'r' {
		st.reload(func() error {
			err := st.fetch(true)
			if err == nil {
				st.say("limits refreshed")
			}
			return err
		})
		return true
	}
	return false
}

func init() {
	registerTuiView(tuiView{
		ID:    tuiViewUsage,
		Title: "Usage",
		Order: 50,
		Load: func(ctx context.Context, c cliCaller, m *tuiModel) {
			st := tuiUsageOf(m)
			tuiLoadView(ctx, c, &st.tuiBase, 5*time.Second, func() error { return st.fetch(false) })
		},
		Render: func(m *tuiModel, cols, rows int) []tuiRow {
			return tuiUsageOf(m).render(cols, rows)
		},
		Key: func(m *tuiModel, k tuiKey) bool {
			return tuiUsageOf(m).key(k)
		},
		Hints: []tuiHint{
			{"↑/↓", "scroll"}, {"r", "refresh limits"}, {"tab", "views"}, {"esc", "threads"}, {"q", "quit"},
		},
	})
}
