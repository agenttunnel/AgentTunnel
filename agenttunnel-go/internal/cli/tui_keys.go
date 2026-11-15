package cli

import (
	"context"
	"io"
	"sync"
	"unicode/utf8"

	"golang.org/x/term"
)

// tuiKeyKind enumerates the key events the TUI input layer understands.
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

// keyDecoder turns raw terminal bytes into tuiKey events. Escape
// sequences and multi-byte runes split across reads stay buffered until
// their remaining bytes arrive, so feed can be called once per Read.
type keyDecoder struct {
	mu  sync.Mutex
	buf []byte
}

// feed appends newly read bytes and returns every key they complete,
// including keys held back by a previously incomplete sequence.
func (d *keyDecoder) feed(p []byte) []tuiKey {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf = append(d.buf, p...)
	var keys []tuiKey
	for len(d.buf) > 0 {
		k, n, ok := nextKey(d.buf)
		if !ok {
			break
		}
		d.buf = d.buf[n:]
		if k.Kind != keyNone {
			keys = append(keys, k)
		}
	}
	return keys
}

// flush resolves a buffered partial escape as a lone Escape press and drops
// the rest. Terminals deliver whole escape sequences in a single read, so
// bytes still pending once the reader has gone quiet are a bare Esc; the
// event loop calls this on its idle tick. A pending partial rune is left
// for the next feed.
func (d *keyDecoder) flush() []tuiKey {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.buf) == 0 || d.buf[0] != 0x1b {
		return nil
	}
	d.buf = d.buf[:0]
	return []tuiKey{{Kind: keyEsc}}
}

// nextKey decodes one key from the front of buf. It returns the key, how
// many bytes it consumed, and whether buf held a complete event; ok false
// means buf ends mid-sequence and must be fed more bytes.
func nextKey(buf []byte) (k tuiKey, n int, ok bool) {
	b := buf[0]
	switch {
	case b == 0x1b:
		if len(buf) == 1 {
			return tuiKey{}, 0, false
		}
		if buf[1] == '[' || buf[1] == 'O' {
			return csiKey(buf)
		}
		// Not a sequence: a bare Esc pressed before the next key.
		return tuiKey{Kind: keyEsc}, 1, true
	case b == 0x03:
		return tuiKey{Kind: keyCtrlC}, 1, true
	case b == '\r', b == '\n':
		return tuiKey{Kind: keyEnter}, 1, true
	case b == 0x7f, b == 0x08:
		return tuiKey{Kind: keyBackspace}, 1, true
	case b == '\t':
		return tuiKey{Kind: keyTab}, 1, true
	case b == 0x0b:
		return tuiKey{Kind: keyCtrlK}, 1, true
	case b < 0x20:
		return tuiKey{}, 1, true // other control bytes: ignored
	}
	r, size := utf8.DecodeRune(buf)
	if r == utf8.RuneError && size == 1 {
		if !utf8.FullRune(buf) {
			return tuiKey{}, 0, false
		}
		return tuiKey{}, 1, true // invalid UTF-8 byte: skipped
	}
	return tuiKey{Kind: keyRune, Rune: r}, size, true
}

// csiKey decodes an escape sequence with a CSI (`ESC [`) or SS3 (`ESC O`)
// introducer. Parameter and intermediate bytes run 0x20–0x3F, the final
// byte 0x40–0x7E. Sequences the TUI does not act on are consumed silently
// so their bytes never leak into the rune stream.
func csiKey(buf []byte) (k tuiKey, n int, ok bool) {
	i := 2
	for i < len(buf) && buf[i] >= 0x20 && buf[i] <= 0x3f {
		i++
	}
	if i == len(buf) {
		return tuiKey{}, 0, false
	}
	final := buf[i]
	if final < 0x40 || final > 0x7e {
		return tuiKey{}, i + 1, true // malformed: drop what was seen
	}
	switch final {
	case 'A', 'B', 'C', 'D':
		if i > 2 {
			return tuiKey{}, i + 1, true // modified arrow (ctrl/shift): ignored
		}
		var kind tuiKeyKind
		switch final {
		case 'A':
			kind = keyUp
		case 'B':
			kind = keyDown
		case 'C':
			kind = keyRight
		case 'D':
			kind = keyLeft
		}
		return tuiKey{Kind: kind}, i + 1, true
	case '~':
		switch string(buf[2:i]) {
		case "5":
			return tuiKey{Kind: keyPgUp}, i + 1, true
		case "6":
			return tuiKey{Kind: keyPgDn}, i + 1, true
		}
	}
	return tuiKey{}, i + 1, true
}

// termCtl owns raw mode and size queries for one terminal file
// descriptor. The x/term entry points are fields so tests can substitute
// fakes; newTermCtl wires the real ones.
type termCtl struct {
	fd    int
	state *term.State

	makeRaw func(int) (*term.State, error)
	restore func(int, *term.State) error
	getSize func(int) (int, int, error)
}

// newTermCtl returns the x/term-backed control for fd.
func newTermCtl(fd int) *termCtl {
	return &termCtl{
		fd:      fd,
		makeRaw: term.MakeRaw,
		restore: term.Restore,
		getSize: term.GetSize,
	}
}

// MakeRaw switches the terminal into raw mode. A second call while raw is
// a no-op so Restore always returns to the original mode.
func (t *termCtl) MakeRaw() error {
	if t.state != nil {
		return nil
	}
	state, err := t.makeRaw(t.fd)
	if err != nil {
		return err
	}
	t.state = state
	return nil
}

// Restore returns the terminal to its mode before MakeRaw. Calling it when
// not raw is a no-op, so it is safe to defer unconditionally.
func (t *termCtl) Restore() error {
	if t.state == nil {
		return nil
	}
	state := t.state
	t.state = nil
	return t.restore(t.fd, state)
}

// Size reports the terminal dimensions in columns and rows.
func (t *termCtl) Size() (int, int, error) {
	return t.getSize(t.fd)
}

// pumpKeys reads terminal bytes from r, decoding them onto out, until r
// fails or ctx ends; it then closes out. Reads block, so cancellation
// relies on r returning (the caller restores the terminal, which ends any
// pending read) rather than on ctx alone.
func pumpKeys(ctx context.Context, r io.Reader, dec *keyDecoder, out chan<- tuiKey) {
	defer close(out)
	buf := make([]byte, 256)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			for _, k := range dec.feed(buf[:n]) {
				select {
				case out <- k:
				case <-ctx.Done():
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}
