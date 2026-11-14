package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

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
)

// runTui wires the command: attach read-only, own the terminal, then run the
// session loop on a key channel and a drawing surface.
func runTui(ctx context.Context, env *Env, args []string) error {
	ref, err := parseTuiArgs(args)
	if err != nil {
		return err
	}
	// Viewer and controller only: the TUI attaches to a daemon that already
	// runs and never silently starts or owns one.
	c, err := connectReadOnly(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()

	term := newRawTerminal(env.Out, os.Stdin)
	if err := term.Enter(); err != nil {
		return err
	}
	// Exit restores the alternate screen and cooked mode on every path out of
	// the session — normal return, Ctrl-C, error, and panic unwinding alike.
	defer term.Exit()

	ctx, stop := withInterrupt(ctx)
	defer stop()

	keys := make(chan tuiKey, 8)
	go pumpKeys(ctx, os.Stdin, &keyDecoder{}, keys)

	return tuiSession(ctx, c, newTuiModel(time.Now()), ref, keys, term)
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
// whole frames. Tests substitute a recording fake.
type tuiTerminal interface {
	Size() (int, int, error)
	Draw(frame string) error
}

// rawTerminal is tuiTerminal over a real tty: alternate screen, hidden cursor,
// raw mode, and cursor-home whole-frame paints.
type rawTerminal struct {
	w   io.Writer
	ctl *termCtl
}

// newRawTerminal draws on w and reads keys from the same file (os.Stdin).
func newRawTerminal(w io.Writer, in *os.File) *rawTerminal {
	return &rawTerminal{w: w, ctl: newTermCtl(int(in.Fd()))}
}

// Enter switches to the alternate screen with raw input. A stdin that is not
// a terminal fails here: there are no keys without one.
func (t *rawTerminal) Enter() error {
	if err := t.ctl.MakeRaw(); err != nil {
		return fmt.Errorf("tui needs a terminal: %w", err)
	}
	if _, err := fmt.Fprint(t.w, "\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J"); err != nil {
		_ = t.ctl.Restore()
		return err
	}
	return nil
}

// Exit returns the shell to the state it had before Enter. It is idempotent,
// so it is safe to defer unconditionally.
func (t *rawTerminal) Exit() {
	_ = t.ctl.Restore()
	_, _ = fmt.Fprint(t.w, "\x1b[?25h\x1b[?1049l")
}

// Size reports the terminal dimensions.
func (t *rawTerminal) Size() (int, int, error) { return t.ctl.Size() }

// Draw paints one frame: home the cursor, rewrite each line erasing to the end
// (a shorter line must not smear the previous frame), then clear below.
// Raw mode does not translate \n, so line breaks leave as \r\n.
func (t *rawTerminal) Draw(frame string) error {
	var b strings.Builder
	b.WriteString("\x1b[H")
	lines := strings.Split(frame, "\n")
	for i, line := range lines {
		b.WriteString(line)
		b.WriteString("\x1b[K")
		if i < len(lines)-1 {
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\x1b[J")
	_, err := io.WriteString(t.w, b.String())
	return err
}

// tuiSession is the event loop, shaped like attachSession's: typed keys,
// daemon events, a slow data poll, a fast redraw tick, resizes, and context
// cancellation race in one select. It returns when the user leaves.
func tuiSession(ctx context.Context, c *Client, m *tuiModel, ref string, keys <-chan tuiKey, term tuiTerminal) error {
	if err := tuiRefreshList(ctx, c, m); err != nil {
		return err
	}
	var tracker *turnTracker
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
	}

	// starting is an outstanding thread.turn.start. That RPC only replies when
	// the whole turn has ended, so it goes out in the background and reports
	// back on starts; a submit while one is in flight steers instead of
	// racing a second start against a thread that has not recorded its agents.
	starts := make(chan error, 1)
	starting := false
	submit := func(line string) {
		parsed := parseAttachCommand(line)
		if parsed.action == attachSteer && parsed.text == "" {
			return
		}
		// An open question owns the next line, whatever follows.
		if parsed.action == attachSteer && m.pending != nil {
			q := *m.pending
			if err := answerQuestion(ctx, c, m.thread.ID, q, parsed.text); err != nil {
				m.appendLogLines("question.answer: " + err.Error())
				return
			}
			m.closeQuestion()
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
			m.appendLogLines(strings.Split(attachHelpText, "\n")...)
		case attachSteer:
			if tracker != nil && (tracker.anyActive() || starting) {
				if err := c.Call(ctx, "thread.steer", map[string]string{"threadId": m.thread.ID, "text": parsed.text}, nil); err != nil {
					m.appendLogLines("thread.steer: " + err.Error())
				}
				return
			}
			starting = true
			threadID, text := m.thread.ID, parsed.text
			go func() {
				starts <- c.Call(ctx, "thread.turn.start", runTurnStartParams{
					ThreadID: threadID, Text: text, Detach: true,
				}, nil)
			}()
		}
	}

	cols, rows := 0, 0
	if w, h, err := term.Size(); err == nil {
		cols, rows = w, h
	}
	dirty := true
	paint := func() error {
		m.now = time.Now()
		if w, h, err := term.Size(); err == nil && w > 0 && h > 0 {
			cols, rows = w, h
		}
		dirty = false
		if cols < 1 || rows < 1 {
			return nil
		}
		return term.Draw(render(m, cols, rows))
	}
	if err := paint(); err != nil {
		return err
	}

	poll := time.NewTicker(tuiPollInterval)
	defer poll.Stop()
	paintTick := time.NewTicker(tuiPaintInterval)
	defer paintTick.Stop()
	resize, stopResize := tuiResizeSignals()
	defer stopResize()

	for {
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
			if k.Kind == keyCtrlC {
				return nil
			}
			if m.screen == tuiScreenThread {
				tuiThreadKey(m, &tracker, k, rows, submit)
				dirty = true
				continue
			}
			switch {
			case k.Kind == keyUp || (k.Kind == keyRune && k.Rune == 'k'):
				m.moveCursor(-1)
				dirty = true
			case k.Kind == keyDown || (k.Kind == keyRune && k.Rune == 'j'):
				m.moveCursor(1)
				dirty = true
			case k.Kind == keyEnter:
				row, ok := m.selected()
				if !ok {
					continue
				}
				var err error
				if tracker, err = tuiOpenThread(ctx, c, m, row.thread, row.project); err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return err
				}
				dirty = true
			case k.Kind == keyRune && k.Rune == 'r':
				if err := tuiRefreshList(ctx, c, m); err == nil {
					dirty = true
				}
			case k.Kind == keyRune && k.Rune == 'q':
				return nil
			}

		case err := <-starts:
			starting = false
			if err != nil && m.screen == tuiScreenThread {
				m.appendLogLines("thread.turn.start: " + err.Error())
				dirty = true
			}

		case ev, ok := <-c.Events():
			if !ok {
				if err := c.Err(); err != nil && ctx.Err() == nil {
					return fmt.Errorf("lost the connection to the daemon: %w", err)
				}
				return nil
			}
			if m.screen == tuiScreenThread && tracker != nil && tuiApplyEvent(m, tracker, ev) {
				dirty = true
			}

		case <-poll.C:
			if m.screen == tuiScreenList {
				if err := tuiRefreshList(ctx, c, m); err == nil {
					dirty = true
				}
				continue
			}
			if tracker != nil {
				if fresh, err := fetchAgentStatuses(ctx, c, m.thread.ID); err == nil {
					tracker.update(fresh)
					m.setRoster(fresh)
					dirty = true
				}
			}

		case <-resize:
			dirty = true

		case <-paintTick.C:
			if dirty {
				if err := paint(); err != nil {
					return err
				}
			}
		}
	}
}

// tuiThreadKey applies one key on the thread screen. A bare q (nothing typed)
// leaves the view, the pager convention; with text in the input every rune
// types. Esc leaves regardless.
func tuiThreadKey(m *tuiModel, tracker **turnTracker, k tuiKey, rows int, submit func(string)) {
	switch k.Kind {
	case keyEsc:
		m.closeThread()
		*tracker = nil
	case keyEnter:
		submit(m.takeInput())
	case keyBackspace:
		m.backspace()
	case keyUp:
		m.scrollUp(1)
	case keyDown:
		m.scrollDown(1)
	case keyPgUp:
		m.scrollUp(tuiPage(rows))
	case keyPgDn:
		m.scrollDown(tuiPage(rows))
	case keyRune:
		if k.Rune == 'q' && len(m.input) == 0 {
			m.closeThread()
			*tracker = nil
			return
		}
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

// tuiOpenThread switches to a thread's live view: subscribe first so nothing
// between the backlog read and the live stream is missed, then seed the log
// pane, the roster, and the turn tracker.
func tuiOpenThread(ctx context.Context, c *Client, m *tuiModel, row threadListRow, project string) (*turnTracker, error) {
	if err := subscribeThread(ctx, c, row.ID); err != nil {
		return nil, err
	}
	backlog, err := fetchEntries(ctx, c, row.ID, "")
	if err != nil {
		return nil, err
	}
	early := bufferedFrames(c, row.ID, backlog)
	statuses, err := fetchAgentStatuses(ctx, c, row.ID)
	if err != nil {
		// A missing roster only blinds the any-active check, not the view.
		statuses = map[string]contracts.AgentStatus{}
	}
	m.openThread(row, project, backlog, statuses)
	tracker := newTurnTracker(statuses, false)
	for _, ev := range early {
		tuiApplyEvent(m, tracker, ev)
	}
	return tracker, nil
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

// tuiApplyEvent folds one live frame into the model — the same cases
// handleAttachEvent prints, rendered into the log pane instead of stdout.
func tuiApplyEvent(m *tuiModel, tracker *turnTracker, ev Event) bool {
	frame, ok := decodeEventFrame(ev)
	if !ok || frame.ThreadID != m.thread.ID {
		return false
	}
	switch contracts.EventType(frame.Type) {
	case contracts.EvtAgentOutput:
		var out contracts.AgentOutput
		if json.Unmarshal(frame.Payload, &out) != nil {
			return false
		}
		tracker.activity = true
		m.appendLog(contracts.LogEntry{AgentID: out.AgentID, Kind: out.Kind, Text: out.Text, At: time.Now()})
	case contracts.EvtAgentStatusChanged:
		var status contracts.AgentStatusChanged
		if json.Unmarshal(frame.Payload, &status) != nil {
			return false
		}
		tracker.observe(string(status.AgentID), status.Status)
		m.observeAgent(string(status.AgentID), status.Status)
		var b strings.Builder
		writeStatusLine(&b, status)
		m.appendLogLines(strings.TrimRight(b.String(), "\n"))
	case contracts.EvtTaskDispatched:
		var dispatch contracts.TaskDispatched
		if json.Unmarshal(frame.Payload, &dispatch) != nil {
			return false
		}
		tracker.note(string(dispatch.AgentID), contracts.AgentPending)
		m.observeAgent(string(dispatch.AgentID), contracts.AgentPending)
		m.appendLogLines(fmt.Sprintf("%s dispatched %s (%s)",
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
