package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/term"
)

// key is a shorthand for building expected keys in the tables below.
func k(kind tuiKeyKind) tuiKey { return tuiKey{Kind: kind} }

func runeKey(r rune) tuiKey { return tuiKey{Kind: keyRune, Rune: r} }

func TestTUIKeysDecode(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []tuiKey
	}{
		{"ascii runes", "aZ3", []tuiKey{runeKey('a'), runeKey('Z'), runeKey('3')}},
		{"multibyte runes", "hél日", []tuiKey{runeKey('h'), runeKey('é'), runeKey('l'), runeKey('日')}},
		{"enter", "a\r\n", []tuiKey{runeKey('a'), k(keyEnter), k(keyEnter)}},
		{"backspace", "a\x7f\x08", []tuiKey{runeKey('a'), k(keyBackspace), k(keyBackspace)}},
		{"ctrl-c", "a\x03b", []tuiKey{runeKey('a'), k(keyCtrlC), runeKey('b')}},
		{"arrows", "\x1b[A\x1b[B\x1b[C\x1b[D", []tuiKey{k(keyUp), k(keyDown), k(keyRight), k(keyLeft)}},
		{"ss3 arrows", "\x1bOA", []tuiKey{k(keyUp)}},
		{"page keys", "\x1b[5~\x1b[6~", []tuiKey{k(keyPgUp), k(keyPgDn)}},
		{"unknown csi is swallowed", "\x1b[3~x", []tuiKey{runeKey('x')}},
		{"modified arrows are swallowed", "\x1b[1;5A\r", []tuiKey{k(keyEnter)}},
		{"malformed sequence dropped", "\x1b[\x80a", []tuiKey{runeKey('a')}},
		{"bare esc before a key", "\x1bx", []tuiKey{k(keyEsc), runeKey('x')}},
		{"esc twice", "\x1b\x1b[A", []tuiKey{k(keyEsc), k(keyUp)}},
		{"tab and ctrl-k", "\t\x0b", []tuiKey{k(keyTab), k(keyCtrlK)}},
		{"other control bytes ignored", "\x00\x01a", []tuiKey{runeKey('a')}},
		{"typed stream", "hi\x1b[D\x7f\n", []tuiKey{runeKey('h'), runeKey('i'), k(keyLeft), k(keyBackspace), k(keyEnter)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var d keyDecoder
			got := d.feed([]byte(tc.in))
			if !keysEqual(got, tc.want) {
				t.Fatalf("feed(%q) = %s, want %s", tc.in, formatKeys(got), formatKeys(tc.want))
			}
			if len(d.buf) != 0 {
				t.Fatalf("feed(%q) left %d pending bytes", tc.in, len(d.buf))
			}
		})
	}
}

func TestTUIKeysPartialSequences(t *testing.T) {
	cases := []struct {
		name  string
		feeds []string
		mid   []int // keys decoded after each feed but the last; len == len(feeds)-1
		want  []tuiKey
	}{
		{
			name:  "arrow split in three",
			feeds: []string{"\x1b", "[", "A"},
			mid:   []int{0, 0},
			want:  []tuiKey{k(keyUp)},
		},
		{
			name:  "pgdn split after params",
			feeds: []string{"\x1b[6", "~"},
			mid:   []int{0},
			want:  []tuiKey{k(keyPgDn)},
		},
		{
			name:  "rune split mid utf8",
			feeds: []string{"\xe6\x97", "\xa5"},
			mid:   []int{0},
			want:  []tuiKey{runeKey('日')},
		},
		{
			name:  "held-back arrow releases later bytes",
			feeds: []string{"\x1b[", "Ax"},
			mid:   []int{0},
			want:  []tuiKey{k(keyUp), runeKey('x')},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var d keyDecoder
			for i, chunk := range tc.feeds {
				got := d.feed([]byte(chunk))
				if i < len(tc.mid) && len(got) != tc.mid[i] {
					t.Fatalf("feed %d (%q) decoded %s, want %d keys", i, chunk, formatKeys(got), tc.mid[i])
				}
				if i == len(tc.feeds)-1 && !keysEqual(got, tc.want) {
					t.Fatalf("final feed decoded %s, want %s", formatKeys(got), formatKeys(tc.want))
				}
			}
		})
	}
}

func TestTUIKeysFlush(t *testing.T) {
	cases := []struct {
		name   string
		pend   string
		want   []tuiKey
		remain bool // bytes still buffered after the flush
	}{
		{"lone esc", "\x1b", []tuiKey{k(keyEsc)}, false},
		{"partial csi", "\x1b[5", []tuiKey{k(keyEsc)}, false},
		{"partial rune kept", "\xe6\x97", nil, true},
		{"nothing pending", "", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var d keyDecoder
			d.feed([]byte(tc.pend))
			got := d.flush()
			if !keysEqual(got, tc.want) {
				t.Fatalf("flush() = %s, want %s", formatKeys(got), formatKeys(tc.want))
			}
			if remain := len(d.buf) != 0; remain != tc.remain {
				t.Fatalf("after flush %d bytes remain, want %v", len(d.buf), tc.remain)
			}
			// The decoder must keep working after a flush.
			if got := d.feed([]byte("a")); !keysEqual(got, []tuiKey{runeKey('a')}) {
				t.Fatalf("feed after flush = %s, want [a]", formatKeys(got))
			}
		})
	}
}

func TestTUIKeysTermControl(t *testing.T) {
	var rawFD, restoreFD, sizeFD atomic.Int32
	ctl := &termCtl{
		fd: 7,
		makeRaw: func(fd int) (*term.State, error) {
			rawFD.Store(int32(fd))
			return &term.State{}, nil
		},
		restore: func(fd int, state *term.State) error {
			restoreFD.Store(int32(fd))
			if state == nil {
				return errors.New("restore: nil state")
			}
			return nil
		},
		getSize: func(fd int) (int, int, error) {
			sizeFD.Store(int32(fd))
			return 80, 24, nil
		},
	}

	if err := ctl.MakeRaw(); err != nil {
		t.Fatalf("MakeRaw: %v", err)
	}
	if rawFD.Load() != 7 {
		t.Fatalf("MakeRaw used fd %d, want 7", rawFD.Load())
	}
	if err := ctl.MakeRaw(); err != nil {
		t.Fatalf("second MakeRaw: %v", err)
	}
	cols, rows, err := ctl.Size()
	if err != nil || cols != 80 || rows != 24 {
		t.Fatalf("Size() = %d, %d, %v; want 80, 24, nil", cols, rows, err)
	}
	if sizeFD.Load() != 7 {
		t.Fatalf("Size used fd %d, want 7", sizeFD.Load())
	}
	if err := ctl.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restoreFD.Load() != 7 {
		t.Fatalf("Restore used fd %d, want 7", restoreFD.Load())
	}
	if err := ctl.Restore(); err != nil {
		t.Fatalf("Restore before MakeRaw must be a no-op, got %v", err)
	}
}

func TestTUIKeysTermControlErrors(t *testing.T) {
	boom := errors.New("no tty")
	ctl := &termCtl{
		fd:      3,
		makeRaw: func(int) (*term.State, error) { return nil, boom },
		restore: func(int, *term.State) error { return errors.New("restore failed") },
		getSize: func(int) (int, int, error) { return 0, 0, boom },
	}
	if err := ctl.MakeRaw(); !errors.Is(err, boom) {
		t.Fatalf("MakeRaw error = %v, want %v", err, boom)
	}
	if _, _, err := ctl.Size(); !errors.Is(err, boom) {
		t.Fatalf("Size error = %v, want %v", err, boom)
	}
	// A failed MakeRaw left no state, so Restore must stay a no-op.
	if err := ctl.Restore(); err != nil {
		t.Fatalf("Restore after failed MakeRaw = %v, want nil", err)
	}
}

func TestTUIKeysTermControlWiredToterm(t *testing.T) {
	ctl := newTermCtl(0)
	if ctl.makeRaw == nil || ctl.restore == nil || ctl.getSize == nil {
		t.Fatal("newTermCtl must wire the real x/term entry points")
	}
}

func TestTUIKeysPumpDeliversAndClosesOnEOF(t *testing.T) {
	r := strings.NewReader("a\x1b[D")
	out := make(chan tuiKey)
	done := make(chan struct{})
	var dec keyDecoder
	go func() {
		pumpKeys(context.Background(), r, &dec, out)
		close(done)
	}()

	var got []tuiKey
	for key := range out {
		got = append(got, key)
	}
	<-done
	if want := []tuiKey{runeKey('a'), k(keyLeft)}; !keysEqual(got, want) {
		t.Fatalf("pumped keys = %s, want %s", formatKeys(got), formatKeys(want))
	}
}

func TestTUIKeysPumpClosesOnCancel(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte("ab"))
		pw.Close()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan tuiKey)
	var dec keyDecoder
	go pumpKeys(ctx, pr, &dec, out)

	first := <-out
	if first.Kind != keyRune || first.Rune != 'a' {
		t.Fatalf("first key = %s, want a", formatKeys([]tuiKey{first}))
	}
	// The pump is now blocked sending 'b'; cancelling must close out
	// without the pending key being delivered.
	cancel()
	if key, ok := <-out; ok {
		t.Fatalf("channel delivered %s after cancel", formatKeys([]tuiKey{key}))
	}
}

func TestTUIKeysConcurrentFeedFlush(t *testing.T) {
	var d keyDecoder
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			d.flush()
		}
	}()
	for i := 0; i < 500; i++ {
		d.feed([]byte("x\x1b[A"))
	}
	<-done
	if got := d.flush(); len(d.buf) != 0 && got != nil {
		t.Fatal("decoder unusable after concurrent feed/flush")
	}
}

func keysEqual(got, want []tuiKey) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func formatKeys(keys []tuiKey) string {
	if keys == nil {
		return "[]"
	}
	vals := make([]string, len(keys))
	for i, key := range keys {
		if key.Kind == keyRune {
			vals[i] = "rune(" + string(key.Rune) + ")"
			continue
		}
		vals[i] = [...]string{
			"none", "rune", "enter", "backspace", "up", "down", "left", "right",
			"pgup", "pgdn", "esc", "ctrl-c",
		}[int(key.Kind)]
	}
	return "[" + strings.Join(vals, " ") + "]"
}
