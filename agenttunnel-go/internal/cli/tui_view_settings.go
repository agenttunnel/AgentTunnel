package cli

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// The Settings tab: the switches that change how the daemon runs threads, each
// role's and harness's enabled state, the lead runtime, and the model catalog.
// Every toggle is one settings.set patch; the view reloads after it lands.

const tuiViewSettings = "settings"

// tuiSettingsWire mirrors transport.SettingsView, the fields this view shows.
type tuiSettingsWire struct {
	Workdir           string        `json:"workdir"`
	AutoMode          bool          `json:"autoMode"`
	Roles             []tuiRoleWire `json:"roles"`
	Lead              tuiLeadWire   `json:"lead"`
	StaleAfterMinutes int           `json:"staleAfterMinutes"`
	ExperimentalRoles bool          `json:"experimentalRoles"`
}

// tuiRoleWire mirrors transport.RoleConfigView.
type tuiRoleWire struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Harness      string `json:"harness"`
	Model        string `json:"model"`
	Variant      string `json:"variant"`
	Enabled      bool   `json:"enabled"`
	Required     bool   `json:"required"`
	Experimental bool   `json:"experimental"`
}

// tuiLeadWire mirrors transport.LeadRuntimeView.
type tuiLeadWire struct {
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Variant string `json:"variant"`
}

// tuiSettingItem is one selectable row: a toggle, or a harness.
type tuiSettingItem struct {
	kind string // auto | experimental | role | harness
	id   string
	on   bool
}

type tuiSettingsState struct {
	tuiBase
	cfg   tuiSettingsWire
	harn  harnessListResult
	addTo string // the harness the open model editor adds to
}

func tuiSettingsOf(m *tuiModel) *tuiSettingsState {
	return tuiStateOf[tuiSettingsState](m, tuiViewSettings)
}

func (st *tuiSettingsState) fetch() error {
	var cfg tuiSettingsWire
	if err := st.call("settings.get", nil, &cfg); err != nil {
		return err
	}
	var harn harnessListResult
	if err := st.call("harness.list", nil, &harn); err != nil {
		return err
	}
	st.cfg, st.harn = cfg, harn
	st.cursor = tuiMoveCursor(st.cursor, 0, len(st.items()))
	return nil
}

// harnesses is every harness the list knows, in daemon order, falling back to
// the union of the sets when the daemon sent no full list.
func (st *tuiSettingsState) harnesses() []string {
	if len(st.harn.All) > 0 {
		return st.harn.All
	}
	seen := map[string]bool{}
	var out []string
	for _, group := range [][]string{st.harn.Available, st.harn.Enabled} {
		for _, h := range group {
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	for h := range st.harn.Models {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	slices.Sort(out)
	return out
}

// items is the selectable rows in display order: the two switches, the roles,
// then the harnesses.
func (st *tuiSettingsState) items() []tuiSettingItem {
	items := []tuiSettingItem{
		{kind: "auto", on: st.cfg.AutoMode},
		{kind: "experimental", on: st.cfg.ExperimentalRoles},
	}
	for _, r := range st.cfg.Roles {
		items = append(items, tuiSettingItem{kind: "role", id: r.ID, on: r.Enabled})
	}
	for _, h := range st.harnesses() {
		items = append(items, tuiSettingItem{kind: "harness", id: h, on: slices.Contains(st.harn.Enabled, h)})
	}
	return items
}

// toggle flips the selected switch with one settings.set patch.
func (st *tuiSettingsState) toggle(it tuiSettingItem) error {
	var patch map[string]any
	switch it.kind {
	case "auto":
		patch = map[string]any{"autoMode": !it.on}
	case "experimental":
		patch = map[string]any{"experimentalRoles": !it.on}
	case "role":
		patch = map[string]any{"roleEnabled": map[string]any{"id": it.id, "enabled": !it.on}}
	case "harness":
		patch = map[string]any{"harness": map[string]any{"id": it.id, "enabled": !it.on}}
	default:
		return nil
	}
	return st.call("settings.set", patch, nil)
}

func (st *tuiSettingsState) selected() (tuiSettingItem, bool) {
	items := st.items()
	if st.cursor < 0 || st.cursor >= len(items) {
		return tuiSettingItem{}, false
	}
	return items[st.cursor], true
}

// key handles the Settings keys. Every key it does not use returns false, so
// Esc, Tab, ctrl+k and q reach the global layer.
func (st *tuiSettingsState) key(k tuiKey) bool {
	st.touch(k)
	if st.ed.on {
		if st.ed.key(k) {
			model := st.ed.value()
			harness := st.addTo
			st.ed.text = nil
			if model != "" {
				st.run("adding "+model, func(ctx context.Context, c cliCaller) (string, error) {
					err := c.Call(ctx, "models.add", map[string]string{"harness": harness, "model": model}, nil)
					return "added " + model + " to " + harness, err
				})
			}
		}
		return true
	}
	if step := tuiNav(k, 8); step != 0 {
		st.cursor = tuiMoveCursor(st.cursor, step, len(st.items()))
		return true
	}
	switch {
	case k.Kind == keyEnter || (k.Kind == keyRune && k.Rune == ' '):
		if it, ok := st.selected(); ok {
			if err := st.toggle(it); err != nil {
				st.setErr(err)
			} else {
				st.say(fmt.Sprintf("%s %s", it.kind, map[bool]string{true: "off", false: "on"}[it.on]))
				st.reload(st.fetch)
			}
		}
	case k.Kind == keyRune && k.Rune == 'm':
		if it, ok := st.selected(); ok && it.kind == "harness" {
			st.addTo = it.id
			st.ed.open("model for "+it.id, "")
		}
	case k.Kind == keyRune && k.Rune == 'R':
		if it, ok := st.selected(); ok && it.kind == "harness" {
			harness := it.id
			st.run("refreshing models for "+harness, func(ctx context.Context, c cliCaller) (string, error) {
				err := c.Call(ctx, "models.refresh", map[string]string{"harness": harness}, nil)
				return "refreshed " + harness + " models", err
			})
		}
	case k.Kind == keyRune && k.Rune == 'u':
		if it, ok := st.selected(); ok && it.kind == "harness" {
			harness := it.id
			st.run("updating "+harness, func(ctx context.Context, c cliCaller) (string, error) {
				err := c.Call(ctx, "harness.update", map[string][]string{"harnesses": {harness}}, nil)
				return "updated " + harness, err
			})
		}
	case k.Kind == keyRune && k.Rune == 'r':
		st.reload(st.fetch)
	default:
		return false
	}
	return true
}

// render lays out the sections; the cursor's row is highlighted.
func (st *tuiSettingsState) render(cols, rows int) []tuiRow {
	items := st.items()
	var lines []tuiRow
	sel := -1
	idx := 0
	add := func(r tuiRow) { lines = append(lines, r) }
	blank := func() { add(nil) }

	add(tuiHeading("Runtime"))
	add(tuiKV("  lead        ", tuiLeadLabel(st.cfg.Lead)))
	add(tuiKV("  workdir     ", usageDash(st.cfg.Workdir)))
	add(tuiKV("  stale after ", fmt.Sprintf("%d min", st.cfg.StaleAfterMinutes)))
	blank()
	add(tuiHeading("Modes"))
	for range 2 {
		if idx == st.cursor {
			sel = len(lines)
		}
		add(tuiSwitchRow(items[idx]))
		idx++
	}
	blank()
	add(tuiHeading("Roles"))
	for _, r := range st.cfg.Roles {
		if idx == st.cursor {
			sel = len(lines)
		}
		add(tuiRoleRow(r, items[idx].on))
		idx++
	}
	blank()
	add(tuiHeading("Harnesses"))
	for _, h := range st.harnesses() {
		if idx == st.cursor {
			sel = len(lines)
		}
		add(st.harnessRow(h, items[idx].on))
		idx++
	}
	status := st.status()
	if st.ed.on {
		status = st.ed.row()
	}
	return tuiViewFrame(cols, rows, "Settings", lines, sel, status)
}

func tuiLeadLabel(l tuiLeadWire) string {
	if l.Harness == "" && l.Model == "" {
		return "inherits the orchestrator role"
	}
	label := usageDash(l.Harness) + " · " + usageDash(l.Model)
	if l.Variant != "" {
		label += " · " + l.Variant
	}
	return label
}

func tuiSwitchRow(it tuiSettingItem) tuiRow {
	label := "Auto mode"
	if it.kind == "experimental" {
		label = "Experimental roles"
	}
	return tuiText("  "+label, tuiInk{fg: tuiColText}).join(tuiText("  ", tuiInk{}), settingOnOff(it.on))
}

func tuiRoleRow(r tuiRoleWire, on bool) tuiRow {
	runtime := usageDash(r.Harness) + "/" + usageDash(r.Model)
	if r.Variant != "" {
		runtime += " · " + r.Variant
	}
	name := r.Name
	if name == "" {
		name = r.ID
	}
	row := tuiText("  "+name, tuiInk{fg: tuiColText}).join(tuiText("  "+runtime, tuiInk{fg: tuiColDim}), tuiText("  ", tuiInk{}), settingOnOff(on))
	if r.Required {
		row = row.join(tuiText("  required", tuiInk{fg: tuiColFaint}))
	}
	return row
}

func (st *tuiSettingsState) harnessRow(h string, on bool) tuiRow {
	installed := tuiText("missing", tuiInk{fg: tuiColFaint})
	if slices.Contains(st.harn.Available, h) {
		installed = tuiText("installed", tuiInk{fg: tuiColOK})
	}
	models := fmt.Sprintf("%d models", len(st.harn.Models[h]))
	return tuiText("  "+h, tuiHarnessInk(h)).join(tuiText("  ", tuiInk{}), installed,
		tuiText("  "+models, tuiInk{fg: tuiColDim}), tuiText("  ", tuiInk{}), settingOnOff(on))
}

func settingOnOff(on bool) tuiRow {
	if on {
		return tuiText("on", tuiInk{fg: tuiColOK, bold: true})
	}
	return tuiText("off", tuiInk{fg: tuiColFaint})
}

func init() {
	registerTuiView(tuiView{
		ID:    tuiViewSettings,
		Title: "Settings",
		Order: 60,
		Load: func(ctx context.Context, c cliCaller, m *tuiModel) {
			st := tuiSettingsOf(m)
			tuiLoadView(ctx, c, &st.tuiBase, 10*time.Second, st.fetch)
		},
		Render: func(m *tuiModel, cols, rows int) []tuiRow {
			return tuiSettingsOf(m).render(cols, rows)
		},
		Key: func(m *tuiModel, k tuiKey) bool {
			return tuiSettingsOf(m).key(k)
		},
		Hints: []tuiHint{
			{"enter", "toggle"}, {"m", "add model"}, {"R", "refresh models"},
			{"u", "update harness"}, {"r", "reload"}, {"tab", "views"}, {"esc", "threads"},
		},
		Actions: []tuiAction{
			{Title: "Settings: toggle auto mode", Run: func(m *tuiModel) {
				st := tuiSettingsOf(m)
				if err := st.toggle(tuiSettingItem{kind: "auto", on: st.cfg.AutoMode}); err != nil {
					st.setErr(err)
					return
				}
				st.reload(st.fetch)
			}},
		},
	})
}
