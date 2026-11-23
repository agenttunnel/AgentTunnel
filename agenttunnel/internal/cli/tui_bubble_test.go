package cli

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func press(code rune, mod tea.KeyMod, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod, Text: text}
}

func TestTUIBubbleKeyMapping(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.KeyPressMsg
		want []tuiKey
	}{
		{"rune", press('a', 0, "a"), []tuiKey{{Kind: keyRune, Rune: 'a'}}},
		{"several runes type in order", press('h', 0, "hi é"), []tuiKey{
			{Kind: keyRune, Rune: 'h'}, {Kind: keyRune, Rune: 'i'}, {Kind: keyRune, Rune: ' '}, {Kind: keyRune, Rune: 'é'},
		}},
		{"space", press(tea.KeySpace, 0, " "), []tuiKey{{Kind: keyRune, Rune: ' '}}},
		{"alt rune is ignored", press('x', tea.ModAlt, "x"), nil},
		{"enter", press(tea.KeyEnter, 0, ""), []tuiKey{{Kind: keyEnter}}},
		{"backspace", press(tea.KeyBackspace, 0, ""), []tuiKey{{Kind: keyBackspace}}},
		{"esc", press(tea.KeyEscape, 0, ""), []tuiKey{{Kind: keyEsc}}},
		{"ctrl+c", press('c', tea.ModCtrl, ""), []tuiKey{{Kind: keyCtrlC}}},
		{"ctrl+k", press('k', tea.ModCtrl, ""), []tuiKey{{Kind: keyCtrlK}}},
		{"ctrl+g", press('g', tea.ModCtrl, ""), []tuiKey{{Kind: keyEditor}}},
		{"tab", press(tea.KeyTab, 0, ""), []tuiKey{{Kind: keyTab}}},
		{"pgup", press(tea.KeyPgUp, 0, ""), []tuiKey{{Kind: keyPgUp}}},
		{"pgdown", press(tea.KeyPgDown, 0, ""), []tuiKey{{Kind: keyPgDn}}},
		{"arrows", press(tea.KeyLeft, 0, ""), []tuiKey{{Kind: keyLeft}}},
		{"alt+enter starts a line", press(tea.KeyEnter, tea.ModAlt, ""), []tuiKey{{Kind: keyNewline}}},
		{"shift+enter starts a line", press(tea.KeyEnter, tea.ModShift, ""), []tuiKey{{Kind: keyNewline}}},
		{"ctrl+j starts a line", press('j', tea.ModCtrl, ""), []tuiKey{{Kind: keyNewline}}},
		{"delete", press(tea.KeyDelete, 0, ""), []tuiKey{{Kind: keyDelete}}},
		{"home", press(tea.KeyHome, 0, ""), []tuiKey{{Kind: keyHome}}},
		{"ctrl+a is home", press('a', tea.ModCtrl, ""), []tuiKey{{Kind: keyHome}}},
		{"ctrl+e is end", press('e', tea.ModCtrl, ""), []tuiKey{{Kind: keyEnd}}},
		{"alt+left moves a word", press(tea.KeyLeft, tea.ModAlt, ""), []tuiKey{{Kind: keyWordLeft}}},
		{"ctrl+right moves a word", press(tea.KeyRight, tea.ModCtrl, ""), []tuiKey{{Kind: keyWordRight}}},
		{"alt+b moves a word", press('b', tea.ModAlt, "b"), []tuiKey{{Kind: keyWordLeft}}},
		{"ctrl+w kills a word", press('w', tea.ModCtrl, ""), []tuiKey{{Kind: keyKillWord}}},
		{"alt+backspace kills a word", press(tea.KeyBackspace, tea.ModAlt, ""), []tuiKey{{Kind: keyKillWord}}},
		{"ctrl+u kills the line", press('u', tea.ModCtrl, ""), []tuiKey{{Kind: keyKillLine}}},
		{"shift+tab", press(tea.KeyTab, tea.ModShift, ""), []tuiKey{{Kind: keyShiftTab}}},
		{"ctrl+b toggles the sidebar", press('b', tea.ModCtrl, ""), []tuiKey{{Kind: keyCtrlB}}},
		{"unmapped", press('y', tea.ModCtrl, ""), nil},
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

// The program model forwards keys and pastes to the session, holds the latest
// frame for View, declares the screen mode there, turns a resize into one
// pending wake-up however many arrive, and relays the terminal's background.
func TestTUIBubbleProgramFlow(t *testing.T) {
	ui := newTuiProgram(context.Background(), 0)

	ui.Update(press(tea.KeyEnter, 0, ""))
	if k := <-ui.keys; k.Kind != keyEnter {
		t.Fatalf("forwarded key = %v", k)
	}
	ui.Update(tea.PasteMsg{Content: "a\nb"})
	if k := <-ui.keys; k.Kind != keyPaste || k.Text != "a\nb" {
		t.Fatalf("forwarded paste = %+v", k)
	}
	ui.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if k := <-ui.keys; k.Kind != keyWheelUp {
		t.Fatalf("forwarded wheel = %+v", k)
	}

	ui.Update(tuiFrameMsg("frame one"))
	ui.Update(tuiTitleMsg("a title"))
	v := ui.View()
	if v.Content != "frame one" || v.WindowTitle != "a title" || !v.AltScreen || !v.ReportFocus || v.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("View = %+v", v)
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

// A focus report decides whether a notification may ring.
func TestTUIBubbleFocusGatesNotifications(t *testing.T) {
	ui := newTuiProgram(context.Background(), 0)
	ui.Update(tea.BlurMsg{})
	if !ui.blurred.Load() {
		t.Fatal("a blur report did not mark the window out of focus")
	}
	ui.Update(tea.FocusMsg{})
	if ui.blurred.Load() {
		t.Fatal("a focus report did not clear it")
	}
}

// A stalled session must not wedge the program once the context ends.
func TestTUIBubbleKeyForwardStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ui := newTuiProgram(ctx, 0)
	for range cap(ui.keys) {
		ui.Update(press(tea.KeyTab, 0, ""))
	}
	cancel()
	ui.Update(press(tea.KeyTab, 0, "")) // channel full: returns instead of blocking
}
