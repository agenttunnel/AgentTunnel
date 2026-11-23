package cli

// tuiKeyKind enumerates the key events the session understands; tuiProgram
// maps Bubble Tea keys onto them.
// keyNone is the zero value so a zero tuiKey means "decoded, ignored".
type tuiKeyKind int

const (
	keyNone tuiKeyKind = iota
	keyRune
	keyEnter
	// keyNewline starts a new line in the composer (alt+enter or ctrl+j);
	// Enter alone sends.
	keyNewline
	keyBackspace
	keyDelete
	keyUp
	keyDown
	keyLeft
	keyRight
	keyWordLeft
	keyWordRight
	keyHome
	keyEnd
	keyPgUp
	keyPgDn
	keyEsc
	keyCtrlC
	keyTab
	keyShiftTab
	keyCtrlK
	// keyCtrlB shows or hides the thread screen's sidebar.
	keyCtrlB
	// keyEditor hands the prompt to $EDITOR (ctrl+g), for text too long to
	// write on a few rows.
	keyEditor
	// keyKillLine and keyKillWord are the readline deletes: ctrl+u and ctrl+w.
	keyKillLine
	keyKillWord
	// keyWheelUp and keyWheelDown are one notch of the mouse wheel.
	keyWheelUp
	keyWheelDown
	// keyPaste carries a whole bracketed paste in Text, newlines included, so
	// the composer inserts it as one edit instead of typing it rune by rune.
	keyPaste
)

// tuiKey is one decoded key press. Rune is set only for keyRune, Text only for
// keyPaste.
type tuiKey struct {
	Kind tuiKeyKind
	Rune rune
	Text string
}

// tuiPasteKeys turns a paste into the rune keys a single-line field types:
// line breaks become spaces and other control runes are dropped. The composer
// takes the paste whole instead; this is for dialogs, the palette, and views.
func tuiPasteKeys(text string) []tuiKey {
	keys := make([]tuiKey, 0, len(text))
	for _, r := range text {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		if tuiControlRune(r) {
			continue
		}
		keys = append(keys, tuiKey{Kind: keyRune, Rune: r})
	}
	return keys
}
