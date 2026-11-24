package cli

import (
	"context"
	"time"
)

// The Remote tab: how this daemon is reached from elsewhere (the phone pairing,
// the WhatsApp bridge, the relay) and whether it is current. The pairing QR is
// never drawn here: it is a secret for a phone's camera, and the desktop app
// shows it. Inbound WhatsApp control lets a chat drive agents, so turning it on
// waits for a second press; applying an update replaces the binary, so it does too.

const tuiViewRemote = "remote"

// tuiRemoteItem mirrors transport.RemoteItem; QR is deliberately not decoded.
type tuiRemoteItem struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	State            string `json:"state"`
	Detail           string `json:"detail"`
	Control          bool   `json:"control"`
	ControlSupported bool   `json:"controlSupported"`
	Unavailable      bool   `json:"unavailable"`
}

// tuiRelayStatus mirrors transport.RelayStatus.
type tuiRelayStatus struct {
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url"`
	Connected bool   `json:"connected"`
	Instance  string `json:"instance"`
	LANHost   string `json:"lanHost"`
}

// tuiUpdateStatus mirrors update.Status.
type tuiUpdateStatus struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available"`
	Installed string `json:"installed"`
}

type tuiRemoteState struct {
	tuiBase
	items []tuiRemoteItem
	relay tuiRelayStatus
	// update is the last check's result; checked is when update.check last
	// ran, so the status line can say how fresh it is. checkBusy is a check
	// in flight.
	update    tuiUpdateStatus
	checked   time.Time
	checkBusy bool
}

func tuiRemoteOf(m *tuiModel) *tuiRemoteState {
	return tuiStateOf[tuiRemoteState](m, tuiViewRemote)
}

// fetchJob reads the remote and relay status in the background.
func (st *tuiRemoteState) fetchJob() tuiJob {
	base := &st.tuiBase
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var remote struct {
			Items []tuiRemoteItem `json:"items"`
		}
		if err := c.Call(ctx, "remote.status", nil, &remote); err != nil {
			return tuiViewErrJob(base, err)
		}
		var relay tuiRelayStatus
		if err := c.Call(ctx, "relay.status", nil, &relay); err != nil {
			return tuiViewErrJob(base, err)
		}
		return func(m *tuiModel) {
			st := tuiRemoteOf(m)
			st.items, st.relay = remote.Items, relay
		}
	}
}

// startUpdateCheck asks the release feed for the latest build as a background
// job: it is a network call, so it must never block the session loop. It runs
// on first open and on u; a check already in flight is not stacked behind.
func (st *tuiRemoteState) startUpdateCheck(m *tuiModel, force bool) {
	st.mu.Lock()
	if st.checkBusy || (!force && !st.checked.IsZero()) {
		st.mu.Unlock()
		return
	}
	st.checkBusy = true
	st.mu.Unlock()
	base := &st.tuiBase
	m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
		// The release feed can hang; a check that never answers must not leave
		// the view saying it is checking, or refuse every later one.
		ctx, cancel := context.WithTimeout(ctx, tuiCallTimeout)
		defer cancel()
		var up tuiUpdateStatus
		err := c.Call(ctx, "update.check", nil, &up)
		return func(m *tuiModel) {
			st := tuiRemoteOf(m)
			st.mu.Lock()
			st.checkBusy = false
			if err == nil {
				st.update, st.checked = up, time.Now()
			}
			st.mu.Unlock()
			if err != nil {
				base.setErr(err)
				return
			}
			base.say("checked for updates")
		}
	})
}

// updateStatus reads the update status under the lock: an install running in the
// background writes its outcome into it.
func (st *tuiRemoteState) updateStatus() tuiUpdateStatus {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.update
}

func (st *tuiRemoteState) whatsapp() (tuiRemoteItem, bool) {
	for _, it := range st.items {
		if it.ID == "whatsapp" {
			return it, true
		}
	}
	return tuiRemoteItem{}, false
}

func (st *tuiRemoteState) lines() []tuiRow {
	var out []tuiRow
	out = append(out, tuiHeading("Remote"))
	if len(st.items) == 0 {
		out = append(out, tuiFaint("  no remote surfaces reported"))
	}
	for _, it := range st.items {
		out = append(out, remoteItemRow(it))
	}
	out = append(out, nil, tuiHeading("Relay"))
	if !st.relay.Enabled {
		out = append(out, tuiFaint("  off · manage it from the desktop app"))
	} else {
		state := tuiText("  connected", tuiInk{fg: tuiColOK})
		if !st.relay.Connected {
			state = tuiText("  not connected", tuiInk{fg: tuiColWarn})
		}
		out = append(out, state.join(tuiText("  "+usageDash(st.relay.URL), tuiInk{fg: tuiColDim})))
		if st.relay.Instance != "" {
			out = append(out, tuiKV("  instance", st.relay.Instance))
		}
	}
	out = append(out, nil, tuiHeading("Updates"))
	u := st.updateStatus()
	if u.Current == "" {
		out = append(out, tuiFaint("  not checked yet · press u to check"))
	} else {
		out = append(out, tuiKV("  current  ", u.Current))
		switch {
		case u.Installed != "":
			out = append(out, tuiText("  installed "+u.Installed+" · restart to use it", tuiInk{fg: tuiColOK}))
		case !u.Enabled:
			out = append(out, tuiFaint("  self-update is off for this install"))
		case u.Available:
			out = append(out, tuiText("  "+u.Latest+" available · press A to install", tuiInk{fg: tuiColWarn}))
		default:
			out = append(out, tuiFaint("  up to date"))
		}
	}
	return out
}

// remoteItemRow is one surface: its state dot, label, and detail.
func remoteItemRow(it tuiRemoteItem) tuiRow {
	dot := tuiInk{fg: tuiColFaint}
	switch it.State {
	case "connected":
		dot = tuiInk{fg: tuiColOK}
	case "connecting", "pairing":
		dot = tuiInk{fg: tuiColWarn}
	case "error":
		dot = tuiInk{fg: tuiColErr}
	}
	row := tuiText("● ", dot).join(tuiText(it.Label, tuiInk{fg: tuiColText, bold: true}),
		tuiText("  "+it.State, dot))
	if it.Detail != "" {
		row = row.join(tuiText("  "+it.Detail, tuiInk{fg: tuiColDim}))
	}
	if it.ControlSupported {
		ctl := tuiText("  control off", tuiInk{fg: tuiColFaint})
		if it.Control {
			ctl = tuiText("  control on", tuiInk{fg: tuiColWarn})
		}
		row = row.join(ctl)
	}
	return row
}

func remoteOnOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func (st *tuiRemoteState) render(cols, rows int) []tuiRow {
	status := st.status()
	return tuiViewFrame(cols, rows, "Remote & updates", st.lines(), -1, status)
}

func (st *tuiRemoteState) key(m *tuiModel, k tuiKey) bool {
	st.touch(k)
	switch {
	case k.Kind == keyRune && k.Rune == 'w':
		it, ok := st.whatsapp()
		if !ok || it.Unavailable {
			st.say("WhatsApp is not available in this build")
			return true
		}
		connect := it.State != "connected"
		st.run("WhatsApp", func(ctx context.Context, c cliCaller) (string, error) {
			method, done := "remote.whatsapp.disconnect", "WhatsApp disconnected"
			if connect {
				method, done = "remote.whatsapp.connect", "WhatsApp connecting · pair from the desktop app if it asks for a code"
			}
			return done, c.Call(ctx, method, nil, nil)
		})
	case k.Kind == keyRune && k.Rune == 'o':
		it, ok := st.whatsapp()
		if !ok || !it.ControlSupported {
			st.say("inbound control is not available here")
			return true
		}
		enable := !it.Control
		if !st.confirm("control", "press o again to turn WhatsApp control "+remoteOnOff(enable), k) {
			return true
		}
		st.run("WhatsApp control", func(ctx context.Context, c cliCaller) (string, error) {
			err := c.Call(ctx, "remote.whatsapp.control", map[string]bool{"enabled": enable}, nil)
			return "WhatsApp control " + remoteOnOff(enable), err
		})
	case k.Kind == keyRune && k.Rune == 'u':
		st.startUpdateCheck(m, true)
		st.say("checking for updates")
	case k.Kind == keyRune && k.Rune == 'A':
		if u := st.updateStatus(); u.Available && u.Installed == "" {
			if !st.confirm("apply", "press A again to install "+u.Latest+" (replaces this binary)", k) {
				return true
			}
			st.run("installing update", func(ctx context.Context, c cliCaller) (string, error) {
				var applied tuiUpdateStatus
				if err := c.Call(ctx, "update.apply", nil, &applied); err != nil {
					return "", err
				}
				st.mu.Lock()
				st.update.Installed = applied.Installed
				st.mu.Unlock()
				return "installed " + applied.Installed + " · restart to use it", nil
			})
		} else {
			st.say("nothing to install · press u to check")
		}
	case k.Kind == keyRune && k.Rune == 'r':
		tuiReloadJob(m, &st.tuiBase, st.fetchJob())
	default:
		return false
	}
	return true
}

func init() {
	registerTuiView(tuiView{
		ID:    tuiViewRemote,
		Title: "Remote",
		Order: 70,
		Load: func(ctx context.Context, c cliCaller, m *tuiModel) {
			st := tuiRemoteOf(m)
			st.bind(ctx, c)
			tuiLoadJob(m, &st.tuiBase, 10*time.Second, st.fetchJob())
			// The update check is a network call to the release feed, so it
			// runs in the background, once on first open.
			st.startUpdateCheck(m, false)
		},
		Render: func(m *tuiModel, cols, rows int) []tuiRow {
			return tuiRemoteOf(m).render(cols, rows)
		},
		Key: func(m *tuiModel, k tuiKey) bool {
			return tuiRemoteOf(m).key(m, k)
		},
		Hints: []tuiHint{
			{"w", "WhatsApp"}, {"o", "control"}, {"u", "check updates"}, {"A", "install update"}, {"r", "reload"}, {"←/→", "tabs"}, {"esc", "threads"},
		},
	})
}
