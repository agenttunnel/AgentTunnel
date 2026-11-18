package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"
)

// tuiProgram is the Bubble Tea model that owns the terminal: raw mode, the
// alternate screen, key decoding, resize, and frame diffing. It holds no UI
// state of its own. The session loop (tuiSession) drives the real model and
// hands over each finished frame; this model turns keys back into tuiKey and
// resizes into a repaint request.
type tuiProgram struct {
	ctx   context.Context
	keys  chan tuiKey
	sized chan struct{}
	// light carries the terminal's answer about its background: true for a
	// light one. It holds the latest answer only.
	light chan bool
	fd    int
	frame string
	title string
	// blurred is set while the terminal reports its window out of focus. A
	// terminal that reports nothing leaves it false, so nothing ever rings.
	blurred atomic.Bool
}

// tuiFrameMsg carries one finished frame from the session loop to View.
type tuiFrameMsg string

// tuiTitleMsg asks the program to name the terminal window.
type tuiTitleMsg string

// tuiClipMsg asks the program to put text on the system clipboard.
type tuiClipMsg string

// tuiRawMsg asks the program to write an escape sequence it has no command
// for, in step with its own output.
type tuiRawMsg string

// tuiExecMsg asks the program to hand the terminal to another process, and to
// call done once that process has exited and the screen is back.
type tuiExecMsg struct {
	cmd  *exec.Cmd
	done func(error)
}

func newTuiProgram(ctx context.Context, fd int) *tuiProgram {
	return &tuiProgram{
		ctx:   ctx,
		keys:  make(chan tuiKey, 64),
		sized: make(chan struct{}, 1),
		light: make(chan bool, 1),
		fd:    fd,
	}
}

// Init asks the terminal for its background color. The answer comes back
// through Bubble Tea's own input parser as a message, so a slow terminal can
// never have it mistaken for typing.
func (p *tuiProgram) Init() tea.Cmd { return tea.RequestBackgroundColor }

func (p *tuiProgram) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tuiFrameMsg:
		p.frame = string(msg)
	case tuiTitleMsg:
		p.title = string(msg)
	case tuiClipMsg:
		return p, tea.SetClipboard(string(msg))
	case tuiRawMsg:
		return p, tea.Raw(string(msg))
	case tuiExecMsg:
		return p, tea.ExecProcess(msg.cmd, func(err error) tea.Msg {
			msg.done(err)
			return nil
		})
	case tea.BackgroundColorMsg:
		select {
		case <-p.light: // an older answer nobody read yet
		default:
		}
		p.light <- !msg.IsDark()
	case tea.FocusMsg:
		p.blurred.Store(false)
	case tea.BlurMsg:
		p.blurred.Store(true)
	case tea.WindowSizeMsg:
		select {
		case p.sized <- struct{}{}:
		default: // a resize is already pending; the paint reads the size fresh
		}
	case tea.KeyPressMsg:
		for _, k := range teaKeys(msg) {
			if !p.forward(k) {
				return p, nil
			}
		}
	case tea.PasteMsg:
		p.forward(tuiKey{Kind: keyPaste, Text: msg.Content})
	case tea.MouseWheelMsg:
		if k, ok := teaWheel(msg); ok {
			p.forward(k)
		}
	}
	return p, nil
}

// forward hands one key to the session loop. It reports false when the
// session ended first, so a full channel never blocks the program on exit.
func (p *tuiProgram) forward(k tuiKey) bool {
	select {
	case p.keys <- k:
		return true
	case <-p.ctx.Done():
		return false
	}
}

// View is the latest frame on the alternate screen. The mouse is captured for
// the wheel alone, and focus is reported so a finished turn only rings when
// the user is looking elsewhere.
func (p *tuiProgram) View() tea.View {
	v := tea.NewView(p.frame)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true
	v.WindowTitle = p.title
	return v
}

// Size, Draw and Resized make tuiProgram the session's tuiTerminal.
func (p *tuiProgram) Size() (int, int, error) { return term.GetSize(p.fd) }

func (p *tuiProgram) Resized() <-chan struct{} { return p.sized }

// Background delivers true when the terminal says its background is light.
func (p *tuiProgram) Background() <-chan bool { return p.light }

// teaKeys maps one Bubble Tea key press to the keys the session understands;
// keys the TUI does not act on map to nothing. Shift+Enter starts a line in
// terminals that can tell it from Enter; Alt+Enter and Ctrl+J do everywhere.
func teaKeys(msg tea.KeyPressMsg) []tuiKey {
	one := func(kind tuiKeyKind) []tuiKey { return []tuiKey{{Kind: kind}} }
	ctrl, alt, shift := msg.Mod.Contains(tea.ModCtrl), msg.Mod.Contains(tea.ModAlt), msg.Mod.Contains(tea.ModShift)
	switch msg.Code {
	case tea.KeyEnter, tea.KeyKpEnter:
		if alt || shift {
			return one(keyNewline)
		}
		return one(keyEnter)
	case tea.KeyBackspace:
		if alt || ctrl {
			return one(keyKillWord)
		}
		return one(keyBackspace)
	case tea.KeyDelete:
		return one(keyDelete)
	case tea.KeyUp:
		return one(keyUp)
	case tea.KeyDown:
		return one(keyDown)
	case tea.KeyLeft:
		if alt || ctrl {
			return one(keyWordLeft)
		}
		return one(keyLeft)
	case tea.KeyRight:
		if alt || ctrl {
			return one(keyWordRight)
		}
		return one(keyRight)
	case tea.KeyHome:
		return one(keyHome)
	case tea.KeyEnd:
		return one(keyEnd)
	case tea.KeyPgUp:
		return one(keyPgUp)
	case tea.KeyPgDown:
		return one(keyPgDn)
	case tea.KeyEscape:
		return one(keyEsc)
	case tea.KeyTab:
		if shift {
			return one(keyShiftTab)
		}
		return one(keyTab)
	}
	switch {
	case ctrl && !alt:
		switch msg.Code {
		case 'c':
			return one(keyCtrlC)
		case 'k':
			return one(keyCtrlK)
		case 'g':
			return one(keyEditor)
		case 'j':
			return one(keyNewline)
		case 'a':
			return one(keyHome)
		case 'e':
			return one(keyEnd)
		case 'u':
			return one(keyKillLine)
		case 'w':
			return one(keyKillWord)
		}
		return nil
	case alt:
		// Option+b / Option+f are the word moves terminals send for alt+left
		// and alt+right when they speak readline.
		switch msg.Code {
		case 'b':
			return one(keyWordLeft)
		case 'f':
			return one(keyWordRight)
		}
		return nil
	}
	keys := make([]tuiKey, 0, len(msg.Text))
	for _, r := range msg.Text {
		if r >= 0x20 && r != 0x7f {
			keys = append(keys, tuiKey{Kind: keyRune, Rune: r})
		}
	}
	return keys
}

// teaWheel maps a mouse wheel notch to its key. Clicks and motion are not
// acted on: the mouse is captured only so the wheel scrolls the conversation
// instead of arriving as arrow keys, which would walk the prompt history.
func teaWheel(msg tea.MouseWheelMsg) (tuiKey, bool) {
	switch msg.Button {
	case tea.MouseWheelUp:
		return tuiKey{Kind: keyWheelUp}, true
	case tea.MouseWheelDown:
		return tuiKey{Kind: keyWheelDown}, true
	}
	return tuiKey{}, false
}

// bubbleTerminal is the session's tuiTerminal over a running Bubble Tea
// program: size comes from the tty; frames, the title, and everything else
// written to the terminal go out as messages, in step with its renderer.
type bubbleTerminal struct {
	*tuiProgram
	prog *tea.Program
}

func (t bubbleTerminal) Draw(frame string) error {
	t.prog.Send(tuiFrameMsg(frame))
	return nil
}

func (t bubbleTerminal) SetTitle(title string) {
	t.prog.Send(tuiTitleMsg(title))
}

// Notify tells a user who has switched away that a thread wants them: a
// desktop notification where the terminal takes OSC 9, and the bell, which
// terminals turn into a badge or a bounce. It does nothing while the window
// has focus — the user is already looking.
func (t bubbleTerminal) Notify(text string) {
	if !t.blurred.Load() {
		return
	}
	t.prog.Send(tuiRawMsg("\x1b]9;" + tuiNotifyText(text) + "\x07\a"))
}

// Copy puts text on the system clipboard through the terminal (OSC 52), which
// also works over SSH. With the mouse captured for the wheel, this is the way
// to take a reply out without a modifier-drag.
func (t bubbleTerminal) Copy(text string) {
	t.prog.Send(tuiClipMsg(text))
}

// Edit opens text in the user's editor and reports what they saved. It
// returns an error at once when no editor is configured; otherwise done runs
// after the editor exits, on a goroutine of Bubble Tea's, with the saved text.
func (t bubbleTerminal) Edit(text string, done func(string, error)) error {
	argv := tuiEditorCommand(os.Getenv)
	if len(argv) == 0 {
		return errors.New("set $VISUAL or $EDITOR to write the prompt in an editor")
	}
	file, err := os.CreateTemp("", "agenttunnel-prompt-*.md")
	if err != nil {
		return err
	}
	path := file.Name()
	_, err = file.WriteString(text)
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return err
	}
	t.prog.Send(tuiExecMsg{cmd: exec.Command(argv[0], append(argv[1:], path)...), done: func(runErr error) {
		defer os.Remove(path)
		if runErr != nil {
			done("", runErr)
			return
		}
		saved, err := os.ReadFile(path)
		done(string(saved), err)
	}})
	return nil
}

// tuiEditorCommand is the editor to run, split into its program and
// arguments ("code --wait" is two words): $VISUAL, else $EDITOR, else nothing.
func tuiEditorCommand(getenv func(string) string) []string {
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if argv := strings.Fields(getenv(name)); len(argv) > 0 {
			return argv
		}
	}
	return nil
}

// tuiNotifyText makes text safe inside an OSC string: control bytes would end
// the sequence early or start another, and ";" would split its fields.
func tuiNotifyText(text string) string {
	return strings.ReplaceAll(tuiSanitize(strings.ReplaceAll(text, "\t", " ")), ";", ",")
}

// tuiStdinIsTerminal reports whether keys can come from this process's stdin.
func tuiStdinIsTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
