package cli

// tuiKeyKind enumerates the key events the session understands; tuiProgram
// maps Bubble Tea keys onto them.
// keyNone is the zero value so a zero tuiKey means "decoded, ignored".
type tuiKeyKind int

const (
	keyNone tuiKeyKind = iota
	keyRune
	keyEnter
	keyBackspace
	keyUp
	keyDown
	keyLeft
	keyRight
	keyPgUp
	keyPgDn
	keyEsc
	keyCtrlC
	keyTab
	keyCtrlK
)

// tuiKey is one decoded key press. Rune is set only for keyRune.
type tuiKey struct {
	Kind tuiKeyKind
	Rune rune
}
