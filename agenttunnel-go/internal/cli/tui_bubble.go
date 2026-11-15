package cli

import (
	"context"
	"os"

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
}

// tuiFrameMsg carries one finished frame from the session loop to View.
type tuiFrameMsg string

func newTuiProgram(ctx context.Context, fd int) *tuiProgram {
	return &tuiProgram{
		ctx:   ctx,
		keys:  make(chan tuiKey, 8),
		sized: make(chan struct{}, 1),
		fd:    fd,
	}
}

func (p *tuiProgram) Init() tea.Cmd { return nil }

func (p *tuiProgram) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tuiFrameMsg:
		p.frame = string(msg)
	case tea.WindowSizeMsg:
		select {
		case p.sized <- struct{}{}:
		default: // a resize is already pending; the paint reads the size fresh
		}
	case tea.KeyMsg:
		for _, k := range teaKeys(msg) {
			select {
			case p.keys <- k:
			case <-p.ctx.Done():
				return p, nil
			}
		}
	}
	return p, nil
}

func (p *tuiProgram) View() string { return p.frame }

// Size, Draw and Resized make tuiProgram the session's tuiTerminal.
func (p *tuiProgram) Size() (int, int, error) { return term.GetSize(p.fd) }

func (p *tuiProgram) Resized() <-chan struct{} { return p.sized }

// teaKeys maps one Bubble Tea key to the keys the session understands. A
// paste arrives as one KeyRunes message and types rune by rune; keys the TUI
// does not act on map to nothing.
func teaKeys(msg tea.KeyMsg) []tuiKey {
	one := func(kind tuiKeyKind) []tuiKey { return []tuiKey{{Kind: kind}} }
	switch msg.Type {
	case tea.KeyRunes:
		if msg.Alt {
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
		return one(keyEnter)
	case tea.KeyBackspace:
		return one(keyBackspace)
	case tea.KeyUp:
		return one(keyUp)
	case tea.KeyDown:
		return one(keyDown)
	case tea.KeyLeft:
		return one(keyLeft)
	case tea.KeyRight:
		return one(keyRight)
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
	case tea.KeyCtrlK:
		return one(keyCtrlK)
	}
	return nil
}

// bubbleTerminal is the session's tuiTerminal over a running Bubble Tea
// program: size comes from the tty, frames go out as messages.
type bubbleTerminal struct {
	*tuiProgram
	prog *tea.Program
}

func (t bubbleTerminal) Draw(frame string) error {
	t.prog.Send(tuiFrameMsg(frame))
	return nil
}

// tuiStdinIsTerminal reports whether keys can come from this process's stdin.
func tuiStdinIsTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
