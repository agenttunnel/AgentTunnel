package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"

	"github.com/aymanbagabas/go-osc52/v2"

	tea "github.com/charmbracelet/bubbletea"
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
	fd    int
	frame string
	// blurred is set while the terminal reports its window out of focus. A
	// terminal that reports nothing leaves it false, so nothing ever rings.
	blurred atomic.Bool
}

// tuiFrameMsg carries one finished frame from the session loop to View.
type tuiFrameMsg string

// tuiTitleMsg asks the program to name the terminal window.
type tuiTitleMsg string

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
		fd:    fd,
	}
}

func (p *tuiProgram) Init() tea.Cmd { return nil }

func (p *tuiProgram) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tuiFrameMsg:
		p.frame = string(msg)
	case tuiTitleMsg:
		return p, tea.SetWindowTitle(string(msg))
	case tuiExecMsg:
		return p, tea.ExecProcess(msg.cmd, func(err error) tea.Msg {
			msg.done(err)
			return nil
		})
	case tea.FocusMsg:
		p.blurred.Store(false)
	case tea.BlurMsg:
		p.blurred.Store(true)
	case tea.WindowSizeMsg:
		select {
		case p.sized <- struct{}{}:
		default: // a resize is already pending; the paint reads the size fresh
		}
	case tea.KeyMsg:
		for _, k := range teaKeys(msg) {
			if !p.forward(k) {
				return p, nil
			}
		}
	case tea.MouseMsg:
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

func (p *tuiProgram) View() string { return p.frame }

// Size, Draw and Resized make tuiProgram the session's tuiTerminal.
func (p *tuiProgram) Size() (int, int, error) { return term.GetSize(p.fd) }

func (p *tuiProgram) Resized() <-chan struct{} { return p.sized }

// teaKeys maps one Bubble Tea key to the keys the session understands. A
// bracketed paste arrives as one keyPaste carrying its text; keys the TUI does
// not act on map to nothing.
func teaKeys(msg tea.KeyMsg) []tuiKey {
	one := func(kind tuiKeyKind) []tuiKey { return []tuiKey{{Kind: kind}} }
	if msg.Paste {
		return []tuiKey{{Kind: keyPaste, Text: string(msg.Runes)}}
	}
	switch msg.Type {
	case tea.KeyRunes:
		if msg.Alt {
			// Option+b / Option+f are the word moves terminals send for
			// alt+left and alt+right when they speak readline.
			if len(msg.Runes) == 1 {
				switch msg.Runes[0] {
				case 'b':
					return one(keyWordLeft)
				case 'f':
					return one(keyWordRight)
				}
			}
			return nil
		}
		keys := make([]tuiKey, 0, len(msg.Runes))
		for _, r := range msg.Runes {
			if r >= 0x20 && r != 0x7f {
				keys = append(keys, tuiKey{Kind: keyRune, Rune: r})
			}
		}
		return keys
	case tea.KeySpace:
		return []tuiKey{{Kind: keyRune, Rune: ' '}}
	case tea.KeyEnter:
		if msg.Alt {
			return one(keyNewline)
		}
		return one(keyEnter)
	case tea.KeyCtrlJ:
		return one(keyNewline)
	case tea.KeyBackspace:
		if msg.Alt {
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
		if msg.Alt {
			return one(keyWordLeft)
		}
		return one(keyLeft)
	case tea.KeyRight:
		if msg.Alt {
			return one(keyWordRight)
		}
		return one(keyRight)
	case tea.KeyCtrlLeft:
		return one(keyWordLeft)
	case tea.KeyCtrlRight:
		return one(keyWordRight)
	case tea.KeyHome, tea.KeyCtrlA:
		return one(keyHome)
	case tea.KeyEnd, tea.KeyCtrlE:
		return one(keyEnd)
	case tea.KeyCtrlU:
		return one(keyKillLine)
	case tea.KeyCtrlW:
		return one(keyKillWord)
	case tea.KeyPgUp:
		return one(keyPgUp)
	case tea.KeyPgDown:
		return one(keyPgDn)
	case tea.KeyEsc:
		return one(keyEsc)
	case tea.KeyCtrlC:
		return one(keyCtrlC)
	case tea.KeyTab:
		return one(keyTab)
	case tea.KeyShiftTab:
		return one(keyShiftTab)
	case tea.KeyCtrlK:
		return one(keyCtrlK)
	case tea.KeyCtrlG:
		return one(keyEditor)
	}
	return nil
}

// teaWheel maps a mouse wheel notch to its key. Clicks and motion are not
// acted on: the mouse is captured only so the wheel scrolls the conversation
// instead of arriving as arrow keys, which would walk the prompt history.
func teaWheel(msg tea.MouseMsg) (tuiKey, bool) {
	if msg.Action != tea.MouseActionPress {
		return tuiKey{}, false
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		return tuiKey{Kind: keyWheelUp}, true
	case tea.MouseButtonWheelDown:
		return tuiKey{Kind: keyWheelDown}, true
	}
	return tuiKey{}, false
}

// bubbleTerminal is the session's tuiTerminal over a running Bubble Tea
// program: size comes from the tty, frames and the title go out as messages.
type bubbleTerminal struct {
	*tuiProgram
	prog *tea.Program
	out  io.Writer
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
	fmt.Fprintf(t.out, "\x1b]9;%s\x07\a", tuiNotifyText(text))
}

// Copy puts text on the system clipboard through the terminal (OSC 52), which
// also works over SSH. With the mouse captured for the wheel, this is the way
// to take a reply out without a modifier-drag.
func (t bubbleTerminal) Copy(text string) {
	fmt.Fprint(t.out, osc52.New(text).String())
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
