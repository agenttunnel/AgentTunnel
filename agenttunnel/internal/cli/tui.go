package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func init() {
	Register(Command{
		Name:    "tui",
		Summary: "Browse and drive threads from a full-screen terminal UI",
		Usage:   "agenttunnel tui [thread]",
		Run:     runTui,
	})
}

const (
	// tuiPollInterval refreshes screen data in the background: thread.list on
	// the list screen, agents.list on a thread — the followers' own poll, whose
	// job is catching what no event delivered.
	tuiPollInterval = followPollInterval
	// tuiPaintInterval coalesces redraws. Agent output can arrive far faster
	// than a terminal repaints, so state changes mark the screen dirty and the
	// next tick paints once.
	tuiPaintInterval = 50 * time.Millisecond
	// tuiSpinInterval is how often the working indicator repaints on its own:
	// the spinner's frame and the clock's second.
	tuiSpinInterval = 100 * time.Millisecond
	// tuiWheelRows is how far one notch of the mouse wheel scrolls.
	tuiWheelRows = 3
)

// runTui wires the command: connect, own the terminal, then run the session
// loop on a key channel and a drawing surface.
func runTui(ctx context.Context, env *Env, args []string) error {
	ref, err := parseTuiArgs(args)
	if err != nil {
		return err
	}
	if !tuiStdinIsTerminal() {
		return fmt.Errorf("tui needs a terminal")
	}
	// Like `agenttunnel run`, the TUI starts a detached daemon when none
	// answers: opening it in a repository is the whole first step. An explicit
	// --daemon is never autostarted.
	c, info, err := env.Connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	ctx, stop := withInterrupt(ctx)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	m := newTuiModel(time.Now())
	// The palette starts dark; the terminal's own answer about its background
	// arrives as a message once the program runs, and the session applies it.
	m.theme = detectTuiTheme(env.Out, os.Getenv)
	m.savedTheme = tuiSavedTheme(ctx, info)
	applyTuiPalette(false, m.savedTheme)
	m.info = tuiInfo{daemon: tuiDaemonLabel(env.DaemonURL), version: env.Version}
	m.cwd, _ = os.Getwd()

	// Bubble Tea owns the terminal (raw mode, alternate screen, key decoding,
	// resize, frame diffing) and restores it on every way out, panics
	// included. The session loop runs beside it on the key channel. The screen
	// mode (alternate screen, wheel capture, focus reports) is declared by the
	// program's View.
	ui := newTuiProgram(ctx, int(os.Stdin.Fd()))
	prog := tea.NewProgram(ui,
		tea.WithInput(os.Stdin), tea.WithOutput(env.Out),
		tea.WithoutSignalHandler(), tea.WithContext(ctx))
	redial := func(ctx context.Context) (*Client, error) { return tuiRedial(ctx, env) }
	done := make(chan error, 1)
	go func() {
		err := tuiSession(ctx, c, m, ref, ui.keys, bubbleTerminal{ui, prog}, redial)
		prog.Quit()
		done <- err
	}()
	_, runErr := prog.Run()
	cancel()
	sessionErr := <-done
	if sessionErr != nil {
		return sessionErr
	}
	if runErr != nil && !errors.Is(runErr, tea.ErrProgramKilled) {
		return runErr
	}
	return nil
}

// tuiRedial waits for the daemon to answer again after a dropped connection,
// backing off between tries. It never starts a daemon: one that went away on
// purpose stays away, and the header keeps saying so until it is back.
func tuiRedial(ctx context.Context, env *Env) (*Client, error) {
	for delay := 500 * time.Millisecond; ; delay = min(delay*2, 8*time.Second) {
		if c, err := connectReadOnly(ctx, env); err == nil {
			return c, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

// parseTuiArgs accepts at most one thread reference.
func parseTuiArgs(args []string) (string, error) {
	var ref string
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "-"):
			return "", usageError("unknown flag %q (usage: agenttunnel tui [thread])", arg)
		case ref != "":
			return "", usageError("agenttunnel tui takes at most one thread reference")
		default:
			ref = arg
		}
	}
	return ref, nil
}

// tuiTerminal is the display surface the session needs: query the size, paint
// whole frames. bubbleTerminal is the real one; tests substitute a recording
// fake. A terminal may also offer `Resized() <-chan struct{}` to wake the
// session on a resize, `SetTitle(string)` to name the window, and
// `Notify(string)` to tell a user who is looking elsewhere.
type tuiTerminal interface {
	Size() (int, int, error)
	Draw(frame string) error
}

// tuiSession is the event loop, shaped like attachSession's: typed keys,
// daemon events, a slow data poll, a fast redraw tick, resizes, and context
// cancellation race in one select. It returns when the user leaves. redial
// brings back a dropped connection; with a nil redial a drop ends the session.
func tuiSession(ctx context.Context, c *Client, m *tuiModel, ref string, keys <-chan tuiKey, term tuiTerminal, redial func(context.Context) (*Client, error)) error {
	if err := tuiRefreshList(ctx, c, m); err != nil {
		return err
	}
	var tracker *turnTracker
	// rosterFetch and listFetch mark a poll read still in flight.
	var rosterFetch, listFetch bool
	if ref != "" {
		row, err := resolveThreadRow(ctx, c, ref)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if tracker, err = tuiOpenThread(ctx, c, m, row, tuiProjectLabel(ctx, c, row)); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	} else if opened, err := tuiOpenHere(ctx, c, m); err != nil {
		// Failing to make a thread is no reason not to start; the list is
		// still there.
	} else if opened != nil {
		// Started inside a project with no thread named: a new thread there
		// is on screen, composer ready.
		tracker = opened
	} else {
		// Not inside any project: offer this folder as one, and ask.
		m.offerHere()
	}

	// events is the live stream; it is nil while the connection is down.
	// redialed delivers the client that replaces a dropped one, which this
	// loop then owns and closes.
	events := c.Events()
	redialed := make(chan *Client, 1)
	// dropped is the client's count of discarded frames as last seen here.
	dropped := c.Dropped()
	var owned *Client
	defer func() {
		if owned != nil {
			owned.Close()
		}
	}()
	// A thread the TUI opened on its own and the user never used is taken
	// back on the way out, whichever way that is. (Deferred after the close
	// above, so it runs before it, on a connection that is still open.)
	defer func() {
		if m.fresh != "" && m.input.empty() {
			tuiDiscardFresh(c, m.fresh)
		}
	}()

	// starting is an outstanding thread.turn.start. That RPC only replies when
	// the whole turn has ended, so it goes out in the background and reports
	// back on starts; a submit while one is in flight steers instead of
	// racing a second start against a thread that has not recorded its agents.
	starts := make(chan error, 1)
	starting := false
	// results carries folds from background jobs (thread commands, panes);
	// the loop applies each one, so the model stays single-threaded.
	results := make(chan func(*tuiModel), 16)
	submit := func(line string) {
		// The paths picked with "@" go with whatever this line turns out to
		// be; a line that is not a message simply drops them.
		attachments := m.takeAttachments(line)
		// A known /command runs its thread operation; anything else is the
		// attach grammar below (steer text, or an answer to an open question).
		if run, arg, ok := tuiSlashCommand(line); ok {
			run(m, arg)
			return
		}
		parsed := parseAttachCommand(line)
		// An open question owns the next line, whatever follows. An empty line
		// answers a choice with the highlighted option.
		if parsed.action == attachSteer && m.pending != nil {
			q := *m.pending
			answer := parsed.text
			if answer == "" {
				answer = m.pendingChoice()
			}
			if answer == "" {
				return
			}
			if err := answerQuestion(ctx, c, m.thread.ID, q, answer); err != nil {
				m.appendLogLines("question.answer: " + err.Error())
				return
			}
			m.closeQuestion()
			return
		}
		if parsed.action == attachSteer && parsed.text == "" {
			return
		}
		switch parsed.action {
		case attachStop:
			var res struct {
				Stopped bool `json:"stopped"`
			}
			if err := c.Call(ctx, "thread.turn.stop", map[string]string{"threadId": m.thread.ID}, &res); err != nil {
				m.appendLogLines("thread.turn.stop: " + err.Error())
			}
		case attachDetach:
			// The TUI's detach is Esc's: leave the view, keep the turn.
			m.closeThread()
			tracker = nil
		case attachHelp:
			m.appendLogLines(tuiHelpLines()...)
		case attachSteer:
			if tracker != nil && (tracker.anyActive() || starting) {
				steer := map[string]any{"threadId": m.thread.ID, "text": parsed.text}
				if len(attachments) > 0 {
					steer["attachments"] = attachments
				}
				if err := c.Call(ctx, "thread.steer", steer, nil); err != nil {
					m.appendLogLines("thread.steer: " + err.Error())
				}
				return
			}
			starting = true
			threadID, text := m.thread.ID, parsed.text
			caller := c
			go func() {
				starts <- caller.Call(ctx, "thread.turn.start", runTurnStartParams{
					ThreadID: threadID, Text: text, Detach: true, Attachments: attachments,
				}, nil)
			}()
		}
	}

	cols, rows := 0, 0
	if w, h, err := term.Size(); err == nil {
		cols, rows = w, h
	}
	dirty := true
	var painted time.Time
	title := ""
	paint := func() error {
		m.now = time.Now()
		if w, h, err := term.Size(); err == nil && w > 0 && h > 0 {
			cols, rows = w, h
		}
		dirty, painted = false, m.now
		if t, ok := term.(interface{ SetTitle(string) }); ok {
			if next := m.windowTitle(); next != title {
				title = next
				t.SetTitle(title)
			}
		}
		if cols < 1 || rows < 1 {
			return nil
		}
		return term.Draw(render(m, cols, rows))
	}
	if err := paint(); err != nil {
		return err
	}
	// notify tells a user who is looking elsewhere that the open thread wants
	// them: its turn ended, or an agent asked a question.
	notify := func(text string) {
		if n, ok := term.(interface{ Notify(string) }); ok {
			n.Notify(text)
		}
	}
	// settled runs a change to the open thread and notifies when it ended the
	// work: agents were active before it and none is after.
	settled := func(change func()) {
		was := m.working()
		change()
		if was && !m.working() {
			notify(m.threadName() + " finished")
		}
	}

	poll := time.NewTicker(tuiPollInterval)
	defer poll.Stop()
	paintTick := time.NewTicker(tuiPaintInterval)
	defer paintTick.Stop()
	var resize <-chan struct{}
	if r, ok := term.(interface{ Resized() <-chan struct{} }); ok {
		resize = r.Resized()
	}
	// background carries the terminal's answer about its own background.
	var background <-chan bool
	if b, ok := term.(interface{ Background() <-chan bool }); ok {
		background = b.Background()
	}

	// onKey applies one key. It reports true when the key ends the session.
	onKey := func(k tuiKey) (bool, error) {
		if k.Kind == keyCtrlC {
			return true, nil
		}
		m.flash = ""
		dirty = true
		if m.dialog != nil {
			tuiDialogKey(m, k)
			return false, nil
		}
		if m.palette != nil {
			tuiPaletteKey(m, k)
			return false, nil
		}
		if v, ok := m.activeView(); ok && v.Key != nil && v.Key(m, tuiWheelAsArrow(k)) {
			return false, nil
		}
		// ':' opens the palette wherever it cannot be text: everywhere but the
		// composer of an open thread.
		onComposer := m.view == tuiThreadsViewID && m.screen == tuiScreenThread && m.pane == nil
		if k.Kind == keyCtrlK || (k.Kind == keyRune && k.Rune == ':' && !onComposer && !m.filtering()) {
			m.openPalette()
			return false, nil
		}
		if m.view != tuiThreadsViewID {
			// A registered tab owns its keys; the global ones are below. Esc
			// goes back to the thread list, or to the thread left open under
			// the tab.
			switch {
			case k.Kind == keyTab:
				m.cycleView(1)
			case k.Kind == keyShiftTab:
				m.cycleView(-1)
			case k.Kind == keyEsc:
				m.switchView(tuiThreadsViewID)
			case k.Kind == keyRune && k.Rune == 'q':
				return true, nil
			}
			return false, nil
		}
		if m.pane != nil {
			tuiPaneKey(m, k, rows)
			return false, nil
		}
		if m.screen == tuiScreenThread {
			tuiThreadKey(m, &tracker, k, rows, submit)
			return false, nil
		}
		if tuiListFilterKey(m, k) {
			return false, nil
		}
		switch {
		case k.Kind == keyTab:
			m.cycleView(1)
		case k.Kind == keyShiftTab:
			m.cycleView(-1)
		case k.Kind == keyUp || k.Kind == keyWheelUp || (k.Kind == keyRune && k.Rune == 'k'):
			m.moveCursor(-1)
		case k.Kind == keyDown || k.Kind == keyWheelDown || (k.Kind == keyRune && k.Rune == 'j'):
			m.moveCursor(1)
		case k.Kind == keyPgUp:
			m.moveCursor(-tuiPage(rows))
		case k.Kind == keyPgDn:
			m.moveCursor(tuiPage(rows))
		case k.Kind == keyEnter:
			row, ok := m.selected()
			if !ok {
				return false, nil
			}
			opened, err := tuiOpenThread(ctx, c, m, row.thread, row.project)
			if err != nil {
				if ctx.Err() != nil {
					return true, nil
				}
				if redial == nil {
					return true, err
				}
				// A daemon that is away fails the open; the list stays up and
				// says why, and the redial brings it back.
				m.flash = "open " + shortThreadID(row.thread.ID) + ": " + err.Error()
				return false, nil
			}
			tracker = opened
		case k.Kind == keyRune && k.Rune == 'n':
			tuiNewThread(m, "")
		case k.Kind == keyRune && k.Rune == 'r':
			_ = tuiRefreshList(ctx, c, m)
		case k.Kind == keyRune && k.Rune == 'q':
			return true, nil
		}
		return false, nil
	}

	lastView := m.view
	for {
		// Requests the palette or a key raised, and tab changes, act here:
		// the loop's one place for daemon work the key handler cannot do.
		if m.quit {
			return nil
		}
		if m.screen != tuiScreenThread {
			tracker = nil
		}
		tuiLoadSide(m)
		if m.view != lastView {
			lastView = m.view
			dirty = true
			if v, ok := m.activeView(); ok && v.Load != nil {
				v.Load(ctx, c, m)
			}
		}
		if m.refresh {
			m.refresh = false
			if err := tuiRefreshList(ctx, c, m); err == nil {
				dirty = true
			}
		}
		if id := m.takeOpenID(); id != "" {
			opened, err := tuiOpenByID(ctx, c, m, id)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				m.notify("open " + shortThreadID(id) + ": " + err.Error())
			} else {
				tracker = opened
			}
			dirty = true
		}
		for _, job := range m.takeJobs() {
			go runTuiJob(ctx, c, job, results)
		}
		if text := m.copyText; text != "" {
			m.copyText = ""
			if clip, ok := term.(interface{ Copy(string) }); ok {
				clip.Copy(text)
				m.notify(fmt.Sprintf("copied the last reply (%d characters)", len([]rune(text))))
			} else {
				m.notify("this terminal cannot take a copy")
			}
			dirty = true
		}
		if m.edit {
			m.edit = false
			tuiEditPrompt(ctx, m, term, results)
			dirty = true
		}

		select {
		case <-ctx.Done():
			return nil

		case k, ok := <-keys:
			if !ok {
				// Stdin closed (a pipe or a file): keep watching, just take
				// no more input — the same rule attach follows.
				keys = nil
				continue
			}
			// The composer takes a paste whole, line breaks and all; every
			// other field types it as one line.
			pressed := []tuiKey{k}
			if k.Kind == keyPaste && !m.composing() {
				pressed = tuiPasteKeys(k.Text)
			}
			for _, k := range pressed {
				quit, err := onKey(k)
				if quit || err != nil {
					return err
				}
			}

		case err := <-starts:
			starting = false
			if err != nil {
				// m.notify writes to the transcript on the thread, or to the
				// flash line once the user has left it.
				m.notify("thread.turn.start: " + err.Error())
				dirty = true
			}

		case apply := <-results:
			apply(m)
			dirty = true

		case ev, ok := <-events:
			if !ok {
				err := c.Err()
				if redial == nil || ctx.Err() != nil {
					if err != nil && ctx.Err() == nil {
						return fmt.Errorf("lost the connection to the daemon: %w", err)
					}
					return nil
				}
				// The daemon went away. Keep the screen, say so in the header,
				// and wait for it in the background.
				events = nil
				m.info.offline = true
				dirty = true
				go func() {
					if next, err := redial(ctx); err == nil {
						redialed <- next
					}
				}()
				continue
			}
			if m.screen == tuiScreenThread && tracker != nil {
				settled(func() {
					if tuiApplyEvent(m, tracker, ev) {
						dirty = true
					}
				})
				if q := tuiQuestionAsked(ev, m.thread.ID); q != "" {
					notify(m.threadName() + " asks: " + q)
				}
			}

		case next := <-redialed:
			if owned != nil {
				owned.Close()
			}
			c, owned, events = next, next, next.Events()
			dropped = c.Dropped()
			m.info.offline = false
			m.refresh = true
			// The thread on screen is subscribed and read again: whatever
			// streamed while the connection was down is in the new backlog.
			if m.screen == tuiScreenThread {
				if reopened, err := tuiReopenThread(ctx, c, m); err == nil {
					tracker = reopened
				} else {
					m.notify("reopen after reconnect: " + err.Error())
				}
			}
			dirty = true

		case <-poll.C:
			// Frames the client had to discard are output this transcript
			// never saw. The daemon still has it: read the thread again.
			if n := c.Dropped(); n != dropped {
				dropped = n
				if m.screen == tuiScreenThread {
					if reopened, err := tuiReopenThread(ctx, c, m); err == nil {
						tracker = reopened
						dirty = true
					} else {
						m.notify("reopen after a dropped frame: " + err.Error())
						dirty = true
					}
				}
			}
			// The roster and the list are read off the loop: a poll tick queues
			// the read and the fold applies it here, only if the reader is still
			// where the read was taken. One read in flight per kind, so a slow
			// daemon never stacks them.
			if tracker != nil && !rosterFetch {
				rosterFetch = true
				threadID := m.thread.ID
				m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
					roster, err := fetchTuiRoster(ctx, c, threadID)
					return func(m *tuiModel) {
						rosterFetch = false
						if err == nil && tuiRosterCurrent(m, tracker, threadID) {
							settled(func() {
								tracker.update(roster.statuses)
								m.setRoster(roster)
							})
						}
					}
				})
			}
			if v, ok := m.activeView(); ok {
				if v.Load != nil {
					v.Load(ctx, c, m)
				}
				dirty = true
				continue
			}
			if m.screen == tuiScreenList && !listFetch {
				listFetch = true
				m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
					rows, err := collectPs(ctx, c, psOptions{})
					return func(m *tuiModel) {
						listFetch = false
						if err == nil && tuiListCurrent(m) {
							m.setList(nestPsRows(rows))
						}
					}
				})
			}

		case <-resize:
			dirty = true

		case light := <-background:
			// Colors are baked into the rows each block remembers, so the
			// remembered rows go with the old palette.
			if m.theme.depth != tuiDepthNone {
				applyTuiPalette(light, m.savedTheme)
				m.repaintAll()
				dirty = true
			}

		case <-paintTick.C:
			// The working indicator repaints on its own beat, so the spinner
			// turns and the clock counts while nothing else changes.
			spinning := m.view == tuiThreadsViewID && m.screen == tuiScreenThread && m.pane == nil && m.working()
			if dirty || (spinning && time.Since(painted) >= tuiSpinInterval) {
				if err := paint(); err != nil {
					return err
				}
			}
		}
	}
}

// tuiEditPrompt hands the open thread's prompt to the user's editor. What
// they save comes back as a fold, applied only if that thread is still the
// one on screen.
func tuiEditPrompt(ctx context.Context, m *tuiModel, term tuiTerminal, results chan<- func(*tuiModel)) {
	if m.screen != tuiScreenThread {
		return
	}
	editor, ok := term.(interface {
		Edit(text string, done func(string, error)) error
	})
	if !ok {
		m.notify("this terminal cannot open an editor")
		return
	}
	threadID := m.thread.ID
	err := editor.Edit(m.input.string(), func(saved string, err error) {
		fold := func(m *tuiModel) {
			switch {
			case !m.onThread(threadID):
			case err != nil:
				m.appendLogLines("editor: " + err.Error())
			default:
				m.input.set(strings.TrimRight(saved, "\n"))
				m.slashSel = 0
			}
		}
		// A session that ended while the editor was open drops the text.
		select {
		case results <- fold:
		case <-ctx.Done():
		}
	})
	if err != nil {
		m.notify(err.Error())
	}
}

// tuiWheelAsArrow lets a view scroll with the wheel through the arrow keys it
// already handles.
func tuiWheelAsArrow(k tuiKey) tuiKey {
	switch k.Kind {
	case keyWheelUp:
		return tuiKey{Kind: keyUp}
	case keyWheelDown:
		return tuiKey{Kind: keyDown}
	}
	return k
}

// composing reports whether keys reach the open thread's composer: no dialog,
// palette, pane, or other tab is in front of it.
func (m *tuiModel) composing() bool {
	return m.dialog == nil && m.palette == nil && m.pane == nil &&
		m.view == tuiThreadsViewID && m.screen == tuiScreenThread
}

// threadName is how a notification and the window title name the open thread.
func (m *tuiModel) threadName() string {
	if m.thread.Title != "" {
		return m.thread.Title
	}
	return shortThreadID(m.thread.ID)
}

// windowTitle is the terminal window's title: the open thread, marked while
// its agents work, so a background tab shows where things stand.
func (m *tuiModel) windowTitle() string {
	if m.screen != tuiScreenThread {
		return "AgentTunnel"
	}
	mark := ""
	if m.working() {
		mark = "● "
	}
	return mark + tuiSanitize(m.threadName()) + " · AgentTunnel"
}

// tuiThreadKey applies one key on the thread screen. Every printable key is
// text for the composer; Esc leaves the thread, keeping what was typed as its
// draft. Up and Down belong to whatever is nearest the cursor: the slash
// hints, an open question's options, the lines of a multi-line prompt, the
// prompt history, and last the transcript.
func tuiThreadKey(m *tuiModel, tracker **turnTracker, k tuiKey, rows int, submit func(string)) {
	// Whatever the key did to the prompt, the "@" search follows it.
	defer func() {
		if m.screen == tuiScreenThread {
			m.syncMention()
		}
	}()
	switch k.Kind {
	case keyEsc:
		// Esc closes the nearest thing first: an open "@" list, then the thread.
		if m.dismissMention() {
			return
		}
		m.closeThread()
		*tracker = nil
	case keyEnter:
		if m.completeMention() {
			return
		}
		submit(m.takeInput())
	case keyNewline:
		m.typeRune('\n')
	case keyPaste:
		m.input.insertString(k.Text)
		m.slashSel = 0
	case keyBackspace:
		m.backspace()
	case keyDelete:
		m.input.del()
		m.slashSel = 0
	case keyKillWord:
		m.input.deleteWordBack()
		m.slashSel = 0
	case keyKillLine:
		m.input.killToStart()
		m.slashSel = 0
	case keyLeft:
		m.input.left()
	case keyRight:
		m.input.right()
	case keyWordLeft:
		m.input.wordLeft()
	case keyWordRight:
		m.input.wordRight()
	case keyCtrlB:
		m.toggleSide()
	case keyHome:
		m.input.home()
	case keyEnd:
		// On an empty prompt End has nothing to move in: it jumps the
		// transcript back to the newest output.
		if m.input.empty() {
			m.scroll = 0
			return
		}
		m.input.end()
	case keyEditor:
		m.edit = true
	case keyTab:
		if m.completeMention() {
			return
		}
		if len(m.slashMatches()) > 0 {
			m.completeSlash()
			return
		}
		m.cycleView(1)
	case keyShiftTab:
		m.cycleView(-1)
	case keyUp:
		if !m.moveSlash(-1) && !m.moveMention(-1) && !m.moveChoice(-1) && !m.input.up() && !m.input.historyPrev() {
			m.scrollUp(1)
		}
	case keyDown:
		if !m.moveSlash(1) && !m.moveMention(1) && !m.moveChoice(1) && !m.input.down() && !m.input.historyNext() {
			m.scrollDown(1)
		}
	case keyWheelUp:
		m.scrollUp(tuiWheelRows)
	case keyWheelDown:
		m.scrollDown(tuiWheelRows)
	case keyPgUp:
		m.scrollUp(tuiPage(rows))
	case keyPgDn:
		m.scrollDown(tuiPage(rows))
	case keyRune:
		m.typeRune(k.Rune)
	}
}

// tuiPage is the lines one PgUp/PgDn travels: half a screen.
func tuiPage(rows int) int {
	if rows < 2 {
		return 1
	}
	return rows / 2
}

// tuiRosterCurrent reports whether a roster read for threadID still belongs on
// screen: the reader is on that thread and it has a turn tracker. A read that
// lands after the reader moved on is dropped.
func tuiRosterCurrent(m *tuiModel, tracker *turnTracker, threadID string) bool {
	return tracker != nil && m.onThread(threadID)
}

// tuiListCurrent reports whether a list read belongs on screen: the reader is
// still on the list.
func tuiListCurrent(m *tuiModel) bool {
	return m.screen == tuiScreenList
}

// tuiRefreshList reloads the list the ps listing builds: nested rows with
// status and project folded in.
func tuiRefreshList(ctx context.Context, c *Client, m *tuiModel) error {
	rows, err := collectPs(ctx, c, psOptions{})
	if err != nil {
		return err
	}
	m.setList(nestPsRows(rows))
	return nil
}

// tuiOpenThread switches to a thread's live view and marks it read: opening is
// reading, so the daemon's unread state clears with the view.
func tuiOpenThread(ctx context.Context, c *Client, m *tuiModel, row threadListRow, project string) (*turnTracker, error) {
	tracker, err := tuiShowThread(ctx, c, m, row, project)
	if err == nil {
		m.startJob(tuiMarkReadJob(row.ID))
	}
	return tracker, err
}

// tuiShowThread switches to a thread's live view: subscribe first so nothing
// between the backlog read and the live stream is missed, then seed the
// transcript, the roster, and the turn tracker.
func tuiShowThread(ctx context.Context, c *Client, m *tuiModel, row threadListRow, project string) (*turnTracker, error) {
	feed, err := fetchTuiFeed(ctx, c, row.ID)
	if err != nil {
		return nil, err
	}
	m.openThread(row, project, feed.roster.statuses)
	return m.seedThread(feed), nil
}

// tuiOpenHere opens a new thread in the project the TUI was started inside,
// the way an editor opens on the folder it was launched in. It returns nil
// when the directory belongs to no registered project: the TUI never
// registers one unasked. The thread exists only in the daemon's memory until
// something is done in it, and it is not marked read, because a read mark is
// itself something done.
func tuiOpenHere(ctx context.Context, c *Client, m *tuiModel) (*turnTracker, error) {
	if m.cwd == "" {
		return nil, nil
	}
	var projects []projectsRow
	if err := c.Call(ctx, "project.list", nil, &projects); err != nil {
		return nil, err
	}
	// Projects can nest; the deepest one that holds the directory is it.
	var here *projectsRow
	for i, p := range projects {
		if tuiUnderDir(m.cwd, p.Path) && (here == nil || len(p.Path) > len(here.Path)) {
			here = &projects[i]
		}
	}
	if here == nil {
		return nil, nil
	}
	var created struct {
		ThreadID string `json:"threadId"`
	}
	if err := c.Call(ctx, "thread.create", map[string]string{"projectId": here.ID}, &created); err != nil {
		return nil, err
	}
	row, err := resolveThreadRow(ctx, c, created.ThreadID)
	if err != nil {
		return nil, err
	}
	tracker, err := tuiShowThread(ctx, c, m, row, projectLabel(*here))
	if err != nil {
		return nil, err
	}
	m.fresh = created.ThreadID
	_ = tuiRefreshList(ctx, c, m) // the list behind the thread now has it too
	return tracker, nil
}

// offerHere asks whether to add the folder the TUI was started in as a
// project, and on a yes opens a new thread in it. The TUI never registers a
// project unasked, so this is a question, not a default. It asks only inside a
// git repository, and never for the home directory, so starting the TUI
// anywhere else is not met with a prompt.
func (m *tuiModel) offerHere() {
	cwd := m.cwd
	if cwd == "" || !tuiInRepo(cwd) || tuiIsHome(cwd) {
		return
	}
	name := filepath.Base(cwd)
	m.openPicker("add "+name+" as a project?", []tuiPickItem{
		{"Yes, work in " + cwd, "yes"},
		{"Not now", ""},
	}, func(m *tuiModel, answer string) {
		if answer != "yes" {
			return
		}
		m.startJob(tuiCreateProjectJob(name, cwd, func(m *tuiModel, projectID string) {
			m.startJob(tuiHereJob(projectID))
		}))
	})
}

// tuiInRepo reports whether dir is inside a git repository.
func tuiInRepo(dir string) bool {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}

// tuiIsHome reports whether dir is the user's home directory.
func tuiIsHome(dir string) bool {
	home, err := os.UserHomeDir()
	return err == nil && tuiRealPath(home) == tuiRealPath(dir)
}

// tuiHereJob makes a new thread in a project the TUI just added and asks the
// loop to open it as the fresh thread: nothing is saved until the user types.
func tuiHereJob(projectID string) tuiJob {
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var created struct {
			ThreadID string `json:"threadId"`
		}
		if err := c.Call(ctx, "thread.create", map[string]string{"projectId": projectID}, &created); err != nil {
			return tuiNote("", "thread.create: "+err.Error())
		}
		return func(m *tuiModel) {
			m.fresh, m.openID, m.refresh = created.ThreadID, created.ThreadID, true
		}
	}
}

// tuiDiscardCall deletes a thread only if nothing was ever recorded in it: a
// version of zero is a thread that lives in the daemon's memory alone, which
// deleting simply forgets. Anything the user did keeps the thread.
func tuiDiscardCall(ctx context.Context, c cliCaller, threadID string) error {
	var st struct {
		Version int64 `json:"version"`
	}
	if err := c.Call(ctx, "thread.get", map[string]string{"threadId": threadID}, &st); err != nil {
		return err
	}
	if st.Version != 0 {
		return nil
	}
	return c.Call(ctx, "thread.delete", map[string]string{"threadId": threadID}, nil)
}

// tuiDiscardJob takes back an unused thread the TUI opened on its own, then
// refreshes the list it was in.
func tuiDiscardJob(threadID string) tuiJob {
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		if tuiDiscardCall(ctx, c, threadID) != nil {
			return nil
		}
		return func(m *tuiModel) { m.refresh = true }
	}
}

// tuiDiscardFresh is tuiDiscardJob for the way out, when the session's own
// context may already be done: it gets a moment of its own.
func tuiDiscardFresh(c cliCaller, threadID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tuiDiscardCall(ctx, c, threadID)
}

// tuiReopenThread reads the open thread again over a new connection. The
// transcript is rebuilt from the daemon's backlog; the composer, with
// whatever is typed in it, is left alone.
func tuiReopenThread(ctx context.Context, c *Client, m *tuiModel) (*turnTracker, error) {
	feed, err := fetchTuiFeed(ctx, c, m.thread.ID)
	if err != nil {
		return nil, err
	}
	m.resetTranscript()
	m.pending = nil
	return m.seedThread(feed), nil
}

// tuiFeed is everything a thread view starts from.
type tuiFeed struct {
	backlog  []contracts.LogEntry
	messages []tuiMessageRow
	roster   tuiRoster
	// early holds live frames that arrived before the backlog reply.
	early []Event
}

// fetchTuiFeed subscribes to a thread and reads its backlog, the messages the
// user sent, and its roster.
func fetchTuiFeed(ctx context.Context, c *Client, threadID string) (tuiFeed, error) {
	if err := subscribeThread(ctx, c, threadID); err != nil {
		return tuiFeed{}, err
	}
	backlog, err := fetchEntries(ctx, c, threadID, "")
	if err != nil {
		return tuiFeed{}, err
	}
	feed := tuiFeed{backlog: backlog, early: bufferedFrames(c, threadID, backlog)}
	// A missing roster only blinds the any-active check, and missing messages
	// only the user's side of the backlog: neither stops the view.
	if feed.roster, err = fetchTuiRoster(ctx, c, threadID); err != nil {
		feed.roster = tuiRoster{statuses: map[string]contracts.AgentStatus{}}
	}
	if st, err := fetchTuiThreadState(ctx, c, threadID); err == nil {
		feed.messages = st.Messages
	}
	return feed, nil
}

// seedThread fills the open thread's view from a feed and returns the tracker
// that follows its turn.
func (m *tuiModel) seedThread(feed tuiFeed) *turnTracker {
	m.setRoster(feed.roster)
	m.appendBacklog(feed.backlog, feed.messages)
	tracker := newTurnTracker(feed.roster.statuses, false)
	for _, ev := range feed.early {
		tuiApplyEvent(m, tracker, ev)
	}
	return tracker
}

// tuiOpenByID opens a thread the loop was asked to open, by its full id.
func tuiOpenByID(ctx context.Context, c *Client, m *tuiModel, id string) (*turnTracker, error) {
	row, err := resolveThreadRow(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if id == m.fresh {
		// A thread the TUI made for the folder it started in is not marked
		// read: that mark is itself something done, which would save it.
		return tuiShowThread(ctx, c, m, row, tuiProjectLabel(ctx, c, row))
	}
	return tuiOpenThread(ctx, c, m, row, tuiProjectLabel(ctx, c, row))
}

// tuiProjectLabel resolves the project label for a thread opened by reference
// (list rows already carry theirs).
func tuiProjectLabel(ctx context.Context, c cliCaller, row threadListRow) string {
	var projects []projectsRow
	if err := c.Call(ctx, "project.list", nil, &projects); err == nil {
		if p, ok := projectByID(projects, row.ProjectID); ok {
			return projectLabel(p)
		}
	}
	return usageDash(row.ProjectID)
}

// tuiApplyEvent folds one live frame into the model: agent output and the
// user's own messages into the transcript, lifecycle into the roster.
func tuiApplyEvent(m *tuiModel, tracker *turnTracker, ev Event) bool {
	frame, ok := decodeEventFrame(ev)
	if !ok || frame.ThreadID != m.thread.ID {
		return false
	}
	at := frame.At
	if at.IsZero() {
		at = time.Now()
	}
	switch contracts.EventType(frame.Type) {
	case contracts.EvtAgentOutput:
		var out contracts.AgentOutput
		if json.Unmarshal(frame.Payload, &out) != nil {
			return false
		}
		tracker.activity = true
		m.appendLog(contracts.LogEntry{AgentID: out.AgentID, Kind: out.Kind, Text: out.Text, At: at})
	case contracts.EvtMessageSent:
		var msg contracts.MessageSent
		if json.Unmarshal(frame.Payload, &msg) != nil {
			return false
		}
		m.appendMessage(msg.MessageID, msg.Author, msg.Text, at)
	case contracts.EvtAgentStatusChanged:
		var status contracts.AgentStatusChanged
		if json.Unmarshal(frame.Payload, &status) != nil {
			return false
		}
		tracker.observe(string(status.AgentID), status.Status)
		m.observeAgent(string(status.AgentID), status.Status)
		line := fmt.Sprintf("%s %s", status.AgentID, status.Status)
		if reason := strings.TrimSpace(status.Reason); reason != "" {
			line += " (" + reason + ")"
		}
		m.appendStatusLines(line)
	case contracts.EvtTaskDispatched:
		var dispatch contracts.TaskDispatched
		if json.Unmarshal(frame.Payload, &dispatch) != nil {
			return false
		}
		tracker.note(string(dispatch.AgentID), contracts.AgentPending)
		m.observeAgent(string(dispatch.AgentID), contracts.AgentPending)
		m.observeHarness(string(dispatch.AgentID), string(dispatch.Harness))
		m.observeStart(string(dispatch.AgentID), at)
		m.appendStatusLines(fmt.Sprintf("%s dispatched %s (%s)",
			dispatch.AgentID, dispatch.Role, truncateText(dispatch.Task, 80)))
	case contracts.EvtQuestionAsked:
		var q contracts.QuestionAsked
		if json.Unmarshal(frame.Payload, &q) != nil {
			return false
		}
		m.askQuestion(questionPromptEntry{id: q.QuestionID, kind: q.Kind, question: q.Question, options: q.Options})
	case contracts.EvtQuestionClosed:
		var closed contracts.QuestionClosed
		if json.Unmarshal(frame.Payload, &closed) != nil {
			return false
		}
		m.closeQuestion()
	default:
		return false
	}
	return true
}

// tuiQuestionAsked is the question a frame asks on threadID, or "" when the
// frame is anything else.
func tuiQuestionAsked(ev Event, threadID string) string {
	frame, ok := decodeEventFrame(ev)
	if !ok || frame.ThreadID != threadID || contracts.EventType(frame.Type) != contracts.EvtQuestionAsked {
		return ""
	}
	var q contracts.QuestionAsked
	if json.Unmarshal(frame.Payload, &q) != nil {
		return ""
	}
	return q.Question
}

// tuiRosterEntry is one agents.list row: the status, the harness the tint
// needs, and the start time the clock counts from.
type tuiRosterEntry struct {
	Agent struct {
		ID        string    `json:"id"`
		Parent    string    `json:"parentAgentId"`
		Role      string    `json:"role"`
		Status    string    `json:"status"`
		Harness   string    `json:"harness"`
		StartedAt time.Time `json:"startedAt"`
	} `json:"agent"`
}

// tuiRoster is a thread's agents as the view keeps them, keyed by agent id.
type tuiRoster struct {
	statuses map[string]contracts.AgentStatus
	harness  map[string]string
	started  map[string]time.Time
	parent   map[string]string
	role     map[string]string
}

// fetchTuiRoster reads a thread's agents. It is the TUI's one agents.list
// read, so the tint, the clock and the tree cost no extra call.
func fetchTuiRoster(ctx context.Context, c cliCaller, threadID string) (tuiRoster, error) {
	var entries []tuiRosterEntry
	if err := c.Call(ctx, "agents.list", map[string]string{"threadId": threadID}, &entries); err != nil {
		return tuiRoster{}, err
	}
	r := tuiRoster{
		statuses: make(map[string]contracts.AgentStatus, len(entries)),
		harness:  map[string]string{},
		started:  map[string]time.Time{},
		parent:   map[string]string{},
		role:     map[string]string{},
	}
	for _, entry := range entries {
		a := entry.Agent
		r.statuses[a.ID] = contracts.AgentStatus(a.Status)
		if a.Harness != "" {
			r.harness[a.ID] = a.Harness
		}
		if !a.StartedAt.IsZero() {
			r.started[a.ID] = a.StartedAt
		}
		if a.Parent != "" {
			r.parent[a.ID] = a.Parent
		}
		if a.Role != "" {
			r.role[a.ID] = a.Role
		}
	}
	return r, nil
}
