package cli

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIBubbleKeyMapping(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.KeyMsg
		want []tuiKey
	}{
		{"rune", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}, []tuiKey{{Kind: keyRune, Rune: 'a'}}},
		{"several runes type in order", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi é")}, []tuiKey{
			{Kind: keyRune, Rune: 'h'}, {Kind: keyRune, Rune: 'i'}, {Kind: keyRune, Rune: ' '}, {Kind: keyRune, Rune: 'é'},
		}},
		{"a paste arrives whole, line breaks included", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\nb"), Paste: true}, []tuiKey{
			{Kind: keyPaste, Text: "a\nb"},
		}},
		{"space", tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}, []tuiKey{{Kind: keyRune, Rune: ' '}}},
		{"alt rune is ignored", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x"), Alt: true}, nil},
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}, []tuiKey{{Kind: keyEnter}}},
		{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}, []tuiKey{{Kind: keyBackspace}}},
		{"esc", tea.KeyMsg{Type: tea.KeyEsc}, []tuiKey{{Kind: keyEsc}}},
		{"ctrl+c", tea.KeyMsg{Type: tea.KeyCtrlC}, []tuiKey{{Kind: keyCtrlC}}},
		{"ctrl+k", tea.KeyMsg{Type: tea.KeyCtrlK}, []tuiKey{{Kind: keyCtrlK}}},
		{"tab", tea.KeyMsg{Type: tea.KeyTab}, []tuiKey{{Kind: keyTab}}},
		{"pgup", tea.KeyMsg{Type: tea.KeyPgUp}, []tuiKey{{Kind: keyPgUp}}},
		{"pgdown", tea.KeyMsg{Type: tea.KeyPgDown}, []tuiKey{{Kind: keyPgDn}}},
		{"arrows", tea.KeyMsg{Type: tea.KeyLeft}, []tuiKey{{Kind: keyLeft}}},
		{"alt+enter starts a line", tea.KeyMsg{Type: tea.KeyEnter, Alt: true}, []tuiKey{{Kind: keyNewline}}},
		{"ctrl+j starts a line", tea.KeyMsg{Type: tea.KeyCtrlJ}, []tuiKey{{Kind: keyNewline}}},
		{"delete", tea.KeyMsg{Type: tea.KeyDelete}, []tuiKey{{Kind: keyDelete}}},
		{"home", tea.KeyMsg{Type: tea.KeyHome}, []tuiKey{{Kind: keyHome}}},
		{"ctrl+a is home", tea.KeyMsg{Type: tea.KeyCtrlA}, []tuiKey{{Kind: keyHome}}},
		{"ctrl+e is end", tea.KeyMsg{Type: tea.KeyCtrlE}, []tuiKey{{Kind: keyEnd}}},
		{"alt+left moves a word", tea.KeyMsg{Type: tea.KeyLeft, Alt: true}, []tuiKey{{Kind: keyWordLeft}}},
		{"ctrl+right moves a word", tea.KeyMsg{Type: tea.KeyCtrlRight}, []tuiKey{{Kind: keyWordRight}}},
		{"alt+b moves a word", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b"), Alt: true}, []tuiKey{{Kind: keyWordLeft}}},
		{"ctrl+w kills a word", tea.KeyMsg{Type: tea.KeyCtrlW}, []tuiKey{{Kind: keyKillWord}}},
		{"alt+backspace kills a word", tea.KeyMsg{Type: tea.KeyBackspace, Alt: true}, []tuiKey{{Kind: keyKillWord}}},
		{"ctrl+u kills the line", tea.KeyMsg{Type: tea.KeyCtrlU}, []tuiKey{{Kind: keyKillLine}}},
		{"shift+tab", tea.KeyMsg{Type: tea.KeyShiftTab}, []tuiKey{{Kind: keyShiftTab}}},
		{"unmapped", tea.KeyMsg{Type: tea.KeyCtrlB}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := teaKeys(tt.msg)
			if len(got) != len(tt.want) {
				t.Fatalf("keys = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("keys = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// The program model forwards keys to the session, holds the latest frame for
// View, and turns a resize into one pending wake-up however many arrive.
func TestTUIBubbleProgramFlow(t *testing.T) {
	ui := newTuiProgram(context.Background(), 0)

	ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if k := <-ui.keys; k.Kind != keyEnter {
		t.Fatalf("forwarded key = %v", k)
	}

	ui.Update(tuiFrameMsg("frame one"))
	if got := ui.View(); got != "frame one" {
		t.Fatalf("View = %q", got)
	}

	ui.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	ui.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	select {
	case <-ui.Resized():
	default:
		t.Fatal("resize did not wake the session")
	}
	select {
	case <-ui.Resized():
		t.Fatal("resizes were not coalesced")
	default:
	}
}

// A stalled session must not wedge the program once the context ends.
func TestTUIBubbleKeyForwardStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ui := newTuiProgram(ctx, 0)
	for range cap(ui.keys) {
		ui.Update(tea.KeyMsg{Type: tea.KeyTab})
	}
	cancel()
	ui.Update(tea.KeyMsg{Type: tea.KeyTab}) // channel full: returns instead of blocking
}
