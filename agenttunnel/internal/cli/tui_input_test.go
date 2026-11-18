package cli

import (
	"fmt"
	"slices"
	"testing"
)

// tuiInputAt is an editor holding s with the cursor at cur.
func tuiInputAt(s string, cur int) *tuiInput {
	t := &tuiInput{}
	t.set(s)
	t.cur = cur
	return t
}

func TestTuiInputInsert(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		cur   int
		r     rune
		want  string
		wantC int
	}{
		{"middle", "abcd", 2, 'X', "abXcd", 3},
		{"start", "abc", 0, 'X', "Xabc", 1},
		{"end", "abc", 3, 'X', "abcX", 4},
		{"newline splits the line", "ab", 1, '\n', "a\nb", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tuiInputAt(tc.text, tc.cur)
			in.insert(tc.r)
			if got := in.string(); got != tc.want || in.cur != tc.wantC {
				t.Fatalf("got %q cur %d, want %q cur %d", got, in.cur, tc.want, tc.wantC)
			}
		})
	}
}

func TestTuiInputZeroValue(t *testing.T) {
	var in tuiInput
	if !in.empty() || in.historyPrev() || in.historyNext() {
		t.Fatal("zero value should be an empty live draft with no history")
	}
	in.insert('a')
	if got := in.take(); got != "a" {
		t.Fatalf("take = %q, want %q", got, "a")
	}
}

func TestTuiInputBackspaceDel(t *testing.T) {
	tests := []struct {
		name    string
		op      func(*tuiInput)
		text    string
		cur     int
		want    string
		wantCur int
	}{
		{"backspace at start is a no-op", (*tuiInput).backspace, "abc", 0, "abc", 0},
		{"backspace removes before cursor", (*tuiInput).backspace, "abc", 2, "ac", 1},
		{"backspace joins lines", (*tuiInput).backspace, "a\nb", 2, "ab", 1},
		{"del at end is a no-op", (*tuiInput).del, "abc", 3, "abc", 3},
		{"del removes at cursor", (*tuiInput).del, "abc", 1, "ac", 1},
		{"del at start keeps cursor", (*tuiInput).del, "abc", 0, "bc", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tuiInputAt(tc.text, tc.cur)
			tc.op(in)
			if got := in.string(); got != tc.want || in.cur != tc.wantCur {
				t.Fatalf("got %q cur %d, want %q cur %d", got, in.cur, tc.want, tc.wantCur)
			}
		})
	}
}

func TestTuiInputLeftRightClamp(t *testing.T) {
	in := tuiInputAt("ab", 0)
	in.left()
	if in.cur != 0 {
		t.Fatalf("left at start: cur = %d, want 0", in.cur)
	}
	in.right()
	in.right()
	in.right()
	if in.cur != 2 {
		t.Fatalf("right past end: cur = %d, want 2", in.cur)
	}
	in.left()
	if in.cur != 1 {
		t.Fatalf("left: cur = %d, want 1", in.cur)
	}
}

func TestTuiInputWordMove(t *testing.T) {
	// indices: foo 0-2, spaces 3-4, bar 5-7, newlines 8-9, baz 10-12, space 13, qux 14-16
	const text = "foo  bar\n\nbaz qux"
	tests := []struct {
		name  string
		cur   int
		left  int
		right int
	}{
		{"start", 0, 0, 3},
		{"after foo", 3, 0, 8},
		{"inside bar", 6, 5, 8},
		{"after bar, over newlines", 8, 5, 13},
		{"start of baz", 10, 5, 13},
		{"end", 17, 14, 17},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tuiInputAt(text, tc.cur)
			in.wordLeft()
			if in.cur != tc.left {
				t.Fatalf("wordLeft from %d: cur = %d, want %d", tc.cur, in.cur, tc.left)
			}
			in = tuiInputAt(text, tc.cur)
			in.wordRight()
			if in.cur != tc.right {
				t.Fatalf("wordRight from %d: cur = %d, want %d", tc.cur, in.cur, tc.right)
			}
		})
	}
}

func TestTuiInputHomeEnd(t *testing.T) {
	// "two words" spans 4..12 with its newlines at 3 and 13.
	const text = "one\ntwo words\nthree"
	tests := []struct {
		name      string
		cur       int
		home, end int
	}{
		{"middle line, middle", 9, 4, 13},
		{"middle line, at start", 4, 4, 13},
		{"first line", 1, 0, 3},
		{"last line", 15, 14, 19},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tuiInputAt(text, tc.cur)
			in.home()
			if in.cur != tc.home {
				t.Fatalf("home: cur = %d, want %d", in.cur, tc.home)
			}
			in = tuiInputAt(text, tc.cur)
			in.end()
			if in.cur != tc.end {
				t.Fatalf("end: cur = %d, want %d", in.cur, tc.end)
			}
		})
	}
}

func TestTuiInputDeleteWordBack(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		cur     int
		want    string
		wantCur int
	}{
		{"removes the word before cursor", "foo bar", 7, "foo ", 4},
		{"skips trailing spaces", "foo bar  ", 9, "foo ", 4},
		{"across a newline", "foo\nbar", 7, "foo\n", 4},
		{"at start is a no-op", "foo", 0, "foo", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tuiInputAt(tc.text, tc.cur)
			in.deleteWordBack()
			if got := in.string(); got != tc.want || in.cur != tc.wantCur {
				t.Fatalf("got %q cur %d, want %q cur %d", got, in.cur, tc.want, tc.wantCur)
			}
		})
	}
}

func TestTuiInputKillToStart(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		cur     int
		want    string
		wantCur int
	}{
		{"single line", "abc", 2, "c", 0},
		{"second line keeps the first", "one\ntwo words", 9, "one\nords", 4},
		{"at line start is a no-op", "one\ntwo", 4, "one\ntwo", 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tuiInputAt(tc.text, tc.cur)
			in.killToStart()
			if got := in.string(); got != tc.want || in.cur != tc.wantCur {
				t.Fatalf("got %q cur %d, want %q cur %d", got, in.cur, tc.want, tc.wantCur)
			}
		})
	}
}

func TestTuiInputUpDown(t *testing.T) {
	// Lines: "abcdef" at 0, "xy" at 7, "ghijkl" at 10.
	in := tuiInputAt("abcdef\nxy\nghijkl", 5)
	steps := []struct {
		op  string
		ok  bool
		cur int
	}{
		{"up", false, 5},
		{"down", true, 9},
		{"down", true, 12},
		{"down", false, 12},
		{"up", true, 9},
		{"up", true, 2},
		{"up", false, 2},
	}
	for _, s := range steps {
		var ok bool
		if s.op == "up" {
			ok = in.up()
		} else {
			ok = in.down()
		}
		if ok != s.ok || in.cur != s.cur {
			t.Fatalf("%s: ok %v cur %d, want ok %v cur %d", s.op, ok, in.cur, s.ok, s.cur)
		}
	}

	// A long target line takes the rune column back out of a short one.
	in = tuiInputAt("ab\ncdefgh", 1)
	in.down()
	if in.cur != 4 {
		t.Fatalf("down into a longer line: cur = %d, want 4", in.cur)
	}
}

func TestTuiInputHistory(t *testing.T) {
	in := &tuiInput{}
	for _, s := range []string{"one", "two", "three"} {
		in.set(s)
		in.take()
	}
	in.insertString("dr")

	steps := []struct {
		op   string
		ok   bool
		want string
	}{
		{"prev", true, "three"},
		{"prev", true, "two"},
		{"prev", true, "one"},
		{"prev", false, "one"},
		{"next", true, "two"},
		{"next", true, "three"},
		{"next", true, "dr"},
		{"next", false, "dr"},
	}
	for _, s := range steps {
		var ok bool
		if s.op == "prev" {
			ok = in.historyPrev()
		} else {
			ok = in.historyNext()
		}
		if ok != s.ok || in.string() != s.want {
			t.Fatalf("%s: ok %v text %q, want ok %v text %q", s.op, ok, in.string(), s.ok, s.want)
		}
		if ok && in.cur != len([]rune(s.want)) {
			t.Fatalf("%s: cursor %d, want end %d", s.op, in.cur, len([]rune(s.want)))
		}
	}
}

func TestTuiInputHistoryEditDoesNotChangeHistory(t *testing.T) {
	in := &tuiInput{}
	for _, s := range []string{"one", "two", "three"} {
		in.set(s)
		in.take()
	}
	in.insertString("dr")
	in.historyPrev()
	in.historyPrev()
	in.insert('X')

	if in.historyNext() {
		t.Fatal("editing a recalled prompt should make it the live draft")
	}
	if !in.historyPrev() || in.string() != "three" {
		t.Fatalf("prev after edit: text %q, want %q", in.string(), "three")
	}
	if !in.historyNext() || in.string() != "twoX" {
		t.Fatalf("next restores edited draft: text %q, want %q", in.string(), "twoX")
	}
	if want := []string{"one", "two", "three"}; !slices.Equal(in.hist, want) {
		t.Fatalf("hist = %q, want %q", in.hist, want)
	}
}

func TestTuiInputTake(t *testing.T) {
	tests := []struct {
		name    string
		prompts []string
		wantHis []string
	}{
		{"repeat of newest is not recorded again", []string{"a", "a"}, []string{"a"}},
		{"repeat of an older entry is recorded", []string{"a", "b", "a"}, []string{"a", "b", "a"}},
		{"whitespace only is not recorded", []string{"a", "   \n"}, []string{"a"}},
		{"text is trimmed", []string{"  hi \n"}, []string{"hi"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := &tuiInput{}
			for _, p := range tc.prompts {
				in.set(p)
				in.take()
			}
			if !slices.Equal(in.hist, tc.wantHis) {
				t.Fatalf("hist = %q, want %q", in.hist, tc.wantHis)
			}
		})
	}
}

func TestTuiInputTakeReturnsAndClears(t *testing.T) {
	in := &tuiInput{}
	in.set("  hi \n")
	in.draft = []rune("kept")
	if got := in.take(); got != "hi" {
		t.Fatalf("take = %q, want %q", got, "hi")
	}
	if !in.empty() || in.cur != 0 || in.draft != nil || in.hidx != len(in.hist) {
		t.Fatalf("take should clear the editor: text %q cur %d draft %q hidx %d", in.string(), in.cur, string(in.draft), in.hidx)
	}
}

func TestTuiInputHistoryCap(t *testing.T) {
	in := &tuiInput{}
	for i := range tuiInputHistory + 5 {
		in.set(fmt.Sprintf("p%d", i))
		in.take()
	}
	if len(in.hist) != tuiInputHistory {
		t.Fatalf("len(hist) = %d, want %d", len(in.hist), tuiInputHistory)
	}
	if in.hist[0] != "p5" || in.hist[len(in.hist)-1] != "p104" {
		t.Fatalf("hist keeps the newest entries: first %q last %q", in.hist[0], in.hist[len(in.hist)-1])
	}
}

func TestTuiInputInsertStringNormalizes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"line endings and controls", "a\r\nb\tc\x1bd", "a\nb cd"},
		{"lone CR is a newline", "x\ry", "x\ny"},
		{"C1 controls and NUL are dropped", "\x7f\u0085ok\x00", "ok"},
		{"tab becomes one space", "tab\there", "tab here"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var in tuiInput
			in.insertString(tc.in)
			if got := in.string(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}

	in := tuiInputAt("AB", 1)
	in.insertString("xy")
	if in.string() != "AxyB" || in.cur != 3 {
		t.Fatalf("insert in middle: got %q cur %d, want %q cur 3", in.string(), in.cur, "AxyB")
	}
}

func TestTuiInputSetAndClear(t *testing.T) {
	in := &tuiInput{}
	in.set("one")
	in.take()
	in.set("hello")
	if in.cur != 5 || in.hidx != len(in.hist) {
		t.Fatalf("set: cur %d hidx %d, want cur 5 hidx %d", in.cur, in.hidx, len(in.hist))
	}
	in.clear()
	if !in.empty() || in.cur != 0 || !slices.Equal(in.hist, []string{"one"}) {
		t.Fatalf("clear should empty the text and keep history: text %q hist %q", in.string(), in.hist)
	}
}

func TestTuiInputView(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		cur       int
		width     int
		maxRows   int
		wantLines []string
		wantRow   int
		wantCol   int
	}{
		{"wide runes wrap by cells", "日本語", 3, 4, 5, []string{"日本", "語"}, 1, 2},
		{"wide rune straddling the edge starts next row", "abc日", 4, 4, 5, []string{"abc", "日"}, 1, 2},
		{"cursor at end of full row starts a new row", "abcd", 4, 4, 5, []string{"abcd", ""}, 1, 0},
		{"cursor at a wrap point belongs to the next row", "abcdefgh", 6, 3, 5, []string{"abc", "def", "gh"}, 2, 0},
		{"cursor at end of full row before newline", "abcd\nef", 4, 4, 5, []string{"abcd", "", "ef"}, 1, 0},
		{"empty logical line is one row", "a\n\nb", 2, 5, 5, []string{"a", "", "b"}, 1, 0},
		{"cursor mid-line", "abcdef", 2, 10, 5, []string{"abcdef"}, 0, 2},
		{"empty text", "", 0, 10, 5, []string{""}, 0, 0},
		{"width below 1 is 1", "ab", 1, 0, 5, []string{"a", "b"}, 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tuiInputAt(tc.text, tc.cur)
			lines, row, col := in.view(tc.width, tc.maxRows)
			if !slices.Equal(lines, tc.wantLines) || row != tc.wantRow || col != tc.wantCol {
				t.Fatalf("view = %q row %d col %d, want %q row %d col %d",
					lines, row, col, tc.wantLines, tc.wantRow, tc.wantCol)
			}
		})
	}
}

func TestTuiInputViewWindow(t *testing.T) {
	// Ten lines "0".."9"; line k starts at index 2k.
	const text = "0\n1\n2\n3\n4\n5\n6\n7\n8\n9"
	tests := []struct {
		name      string
		cur       int
		wantLines []string
		wantRow   int
	}{
		{"top of the text", 0, []string{"0", "1", "2"}, 0},
		{"cursor near the top keeps the window at the start", 6, []string{"1", "2", "3"}, 2},
		{"cursor in the middle ends the window at the cursor", 8, []string{"2", "3", "4"}, 2},
		{"cursor at the first row of the last window", 14, []string{"7", "8", "9"}, 0},
		{"cursor at the end shows the last rows", 19, []string{"7", "8", "9"}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tuiInputAt(text, tc.cur)
			lines, row, _ := in.view(10, 3)
			if !slices.Equal(lines, tc.wantLines) || row != tc.wantRow {
				t.Fatalf("view = %q row %d, want %q row %d", lines, row, tc.wantLines, tc.wantRow)
			}
		})
	}

	in := tuiInputAt("a\nb\nc", 5)
	lines, row, col := in.view(10, 0)
	if !slices.Equal(lines, []string{"c"}) || row != 0 || col != 1 {
		t.Fatalf("maxRows below 1: view = %q row %d col %d, want [c] row 0 col 1", lines, row, col)
	}
}
