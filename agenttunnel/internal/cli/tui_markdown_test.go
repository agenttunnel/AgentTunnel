package cli

import (
	"reflect"
	"strings"
	"testing"
)

// tuiPlainRows is each row's unstyled text, so assertions read as the screen.
func tuiPlainRows(rows []tuiRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.plain()
	}
	return out
}

func tuiAssertWidths(t *testing.T, rows []tuiRow, width int) {
	t.Helper()
	for i, r := range rows {
		if got := r.width(); got > width {
			t.Fatalf("row %d is %d cells, want <= %d: %q", i, got, width, r.plain())
		}
	}
}

// tuiInkOf is the ink of the span holding text, failing if there is none.
func tuiInkOf(t *testing.T, rows []tuiRow, text string) tuiInk {
	t.Helper()
	for _, r := range rows {
		for _, s := range r {
			if s.text == text {
				return s.ink
			}
		}
	}
	t.Fatalf("no span with text %q in %q", text, tuiPlainRows(rows))
	return tuiInk{}
}

func TestTuiWrapRowFitsUnchanged(t *testing.T) {
	row := tuiText("short line", tuiInk{fg: tuiColDim})
	got := tuiWrapRow(row, 40, 2)
	if len(got) != 1 || !reflect.DeepEqual(got[0], row) {
		t.Fatalf("fitting row changed: %#v", got)
	}
	if got := tuiWrapRow(nil, 10, 0); len(got) != 1 || got[0] != nil {
		t.Fatalf("nil row = %#v, want one nil row", got)
	}
}

func TestTuiWrapRowBreaksAtSpacesAndDropsThem(t *testing.T) {
	got := tuiPlainRows(tuiWrapRow(tuiText("hello world foo", tuiInk{}), 11, 0))
	want := []string{"hello world", "foo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wrap = %q, want %q", got, want)
	}
}

func TestTuiWrapRowHardSplitsLongWord(t *testing.T) {
	got := tuiPlainRows(tuiWrapRow(tuiText("abcdefghij", tuiInk{}), 4, 0))
	want := []string{"abcd", "efgh", "ij"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hard split = %q, want %q", got, want)
	}
}

func TestTuiWrapRowWideRunesNeverExceedWidth(t *testing.T) {
	rows := tuiWrapRow(tuiText("日本語のテキスト", tuiInk{}), 5, 0)
	tuiAssertWidths(t, rows, 5)
	if got := strings.Join(tuiPlainRows(rows), ""); got != "日本語のテキスト" {
		t.Fatalf("wide text lost runes: %q", got)
	}
}

func TestTuiWrapRowHangIndentsContinuations(t *testing.T) {
	got := tuiPlainRows(tuiWrapRow(tuiText("aaa bbb ccc", tuiInk{}), 7, 2))
	want := []string{"aaa bbb", "  ccc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hang wrap = %q, want %q", got, want)
	}
}

func TestTuiWrapRowHangTooLargeIsZero(t *testing.T) {
	got := tuiPlainRows(tuiWrapRow(tuiText("aaa bbb ccc", tuiInk{}), 4, 9))
	want := []string{"aaa", "bbb", "ccc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("oversized hang = %q, want %q", got, want)
	}
}

func TestTuiWrapRowKeepsIndentationOnFirstRow(t *testing.T) {
	got := tuiPlainRows(tuiWrapRow(tuiText("   one two three", tuiInk{}), 9, 0))
	want := []string{"   one", "two three"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("indented wrap = %q, want %q", got, want)
	}
}

func TestTuiWrapRowKeepsInkAcrossSplit(t *testing.T) {
	bold := tuiInk{fg: tuiColAccent, bold: true}
	row := tuiText("aaaa bbbb", bold)
	rows := tuiWrapRow(row, 4, 0)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %q", len(rows), tuiPlainRows(rows))
	}
	for i, r := range rows {
		if len(r) != 1 || r[0].ink != bold {
			t.Fatalf("row %d ink = %#v, want %#v", i, r, bold)
		}
	}
}

func TestTuiWrapRowPropertyWidths(t *testing.T) {
	samples := []string{
		"the quick brown fox jumps over the lazy dog",
		"  indented line with   several   gaps  ",
		"日本語のテキスト and mixed ascii 漢字",
		"supercalifragilisticexpialidocious",
	}
	for _, width := range []int{5, 10, 20, 40} {
		for _, s := range samples {
			for _, hang := range []int{0, 2, width} {
				rows := tuiWrapRow(tuiText(s, tuiInk{}), width, hang)
				tuiAssertWidths(t, rows, width)
			}
		}
	}
}

func TestTuiMarkdownParagraphWraps(t *testing.T) {
	rows := tuiMarkdownRows("one two three four", 9, tuiInk{})
	want := []string{"one two", "three", "four"}
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, want) {
		t.Fatalf("paragraph = %q, want %q", got, want)
	}
}

func TestTuiMarkdownHeadingStripsMarkersAndStylesInk(t *testing.T) {
	rows := tuiMarkdownRows("## Plan **now**", 40, tuiInk{fg: tuiColText})
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, []string{"Plan now"}) {
		t.Fatalf("heading = %q", got)
	}
	want := tuiInk{fg: tuiColAccent, bold: true}
	if got := tuiInkOf(t, rows, "Plan "); got != want {
		t.Fatalf("heading ink = %#v, want %#v", got, want)
	}
}

func TestTuiMarkdownHashWithoutSpaceIsNotHeading(t *testing.T) {
	rows := tuiMarkdownRows("#tag stays", 40, tuiInk{})
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, []string{"#tag stays"}) {
		t.Fatalf("hashtag = %q", got)
	}
}

func TestTuiMarkdownBulletHangingIndent(t *testing.T) {
	rows := tuiMarkdownRows("- alpha beta gamma", 12, tuiInk{})
	want := []string{"• alpha beta", "  gamma"}
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, want) {
		t.Fatalf("bullet = %q, want %q", got, want)
	}
}

func TestTuiMarkdownNestedBulletIndentation(t *testing.T) {
	rows := tuiMarkdownRows("- top\n    * nested item text", 40, tuiInk{})
	want := []string{"• top", "    • nested item text"}
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, want) {
		t.Fatalf("nested = %q, want %q", got, want)
	}
}

func TestTuiMarkdownNumberedItemKeepsMarker(t *testing.T) {
	rows := tuiMarkdownRows("3) first step here", 12, tuiInk{})
	want := []string{"3) first", "   step here"}
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, want) {
		t.Fatalf("numbered = %q, want %q", got, want)
	}
}

func TestTuiMarkdownFencedCodeIsPanelAndNotReflowed(t *testing.T) {
	src := "```go\nfunc main() {  fmt.Println(\"a very long line of code\")\n}\n```"
	rows := tuiMarkdownRows(src, 20, tuiInk{})
	tuiAssertWidths(t, rows, 20)

	if got := rows[0].plain(); got != "go" {
		t.Fatalf("language row = %q, want %q", got, "go")
	}
	if ink := rows[0][0].ink; ink != (tuiInk{fg: tuiColFaint}) {
		t.Fatalf("language ink = %#v", ink)
	}
	code := rows[1:]
	for i, r := range code {
		if r.width() != 20 {
			t.Fatalf("code row %d width = %d, want 20: %q", i, r.width(), r.plain())
		}
		if !strings.HasPrefix(r.plain(), "  ") {
			t.Fatalf("code row %d lacks prefix: %q", i, r.plain())
		}
		for _, s := range r {
			if s.ink.bg != tuiColBar {
				t.Fatalf("code row %d span %q bg = %#v, want panel bar", i, s.text, s.ink.bg)
			}
		}
	}
	// The code text is hard-wrapped at width-2 cells, so the long line is cut
	// at 18 cells and the rest continues on the next panel row.
	joined := ""
	for _, r := range code {
		joined += strings.TrimRight(strings.TrimPrefix(r.plain(), "  "), " ")
	}
	if !strings.Contains(joined, "func main() {  fmt.Println(\"a very long line of code\")") {
		t.Fatalf("code was reflowed or altered: %q", joined)
	}
	if got := strings.TrimRight(code[0].plain(), " "); got != "  func main() {  fmt" {
		t.Fatalf("first code row = %q", got)
	}
}

func TestTuiMarkdownFenceWithoutLanguageHasNoLabelRow(t *testing.T) {
	rows := tuiMarkdownRows("```\nx\n```", 10, tuiInk{})
	if len(rows) != 1 || !strings.HasPrefix(rows[0].plain(), "  x") {
		t.Fatalf("rows = %q", tuiPlainRows(rows))
	}
}

func TestTuiMarkdownUnclosedFenceRunsToEnd(t *testing.T) {
	rows := tuiMarkdownRows("intro\n```go\nstill typing", 20, tuiInk{})
	tuiAssertWidths(t, rows, 20)
	last := rows[len(rows)-1]
	if got := strings.TrimRight(last.plain(), " "); got != "  still typing" {
		t.Fatalf("unclosed code row = %q", got)
	}
	if ink := tuiInkOf(t, rows, "still typing"); ink != (tuiInk{fg: tuiColText, bg: tuiColBar}) {
		t.Fatalf("code ink = %#v", ink)
	}
}

func TestTuiMarkdownInlineCodeInk(t *testing.T) {
	rows := tuiMarkdownRows("run `go test` now", 40, tuiInk{fg: tuiColText})
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, []string{"run go test now"}) {
		t.Fatalf("inline code = %q", got)
	}
	want := tuiInk{fg: tuiColAccent2}
	if got := tuiInkOf(t, rows, "go test"); got != want {
		t.Fatalf("code ink = %#v, want %#v", got, want)
	}
}

func TestTuiMarkdownBoldInk(t *testing.T) {
	base := tuiInk{fg: tuiColText}
	rows := tuiMarkdownRows("a **strong** word", 40, base)
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, []string{"a strong word"}) {
		t.Fatalf("bold = %q", got)
	}
	if got := tuiInkOf(t, rows, "strong"); got != (tuiInk{fg: tuiColText, bold: true}) {
		t.Fatalf("bold ink = %#v", got)
	}
}

func TestTuiMarkdownUnmatchedMarkersStayLiteral(t *testing.T) {
	rows := tuiMarkdownRows("half **open and `tick", 40, tuiInk{})
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, []string{"half **open and `tick"}) {
		t.Fatalf("unmatched = %q", got)
	}
}

func TestTuiMarkdownStarInsideCodeIsLiteral(t *testing.T) {
	rows := tuiMarkdownRows("use `a**b` and **c**", 40, tuiInk{})
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, []string{"use a**b and c"}) {
		t.Fatalf("code with ** = %q", got)
	}
}

func TestTuiMarkdownSnakeCaseUntouched(t *testing.T) {
	rows := tuiMarkdownRows("set my_var_name and *not emphasis*", 60, tuiInk{})
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, []string{"set my_var_name and *not emphasis*"}) {
		t.Fatalf("snake_case = %q", got)
	}
}

func TestTuiMarkdownHorizontalRule(t *testing.T) {
	for _, src := range []string{"---", "***", "___"} {
		rows := tuiMarkdownRows(src, 6, tuiInk{})
		if got := tuiPlainRows(rows); !reflect.DeepEqual(got, []string{"──────"}) {
			t.Fatalf("rule %q = %q", src, got)
		}
		if ink := rows[0][0].ink; ink != (tuiInk{fg: tuiColBorder}) {
			t.Fatalf("rule ink = %#v", ink)
		}
	}
}

func TestTuiMarkdownBlockquote(t *testing.T) {
	rows := tuiMarkdownRows("> quoted words that wrap", 12, tuiInk{})
	want := []string{"│ quoted", "  words that", "  wrap"}
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, want) {
		t.Fatalf("quote = %q, want %q", got, want)
	}
	if ink := tuiInkOf(t, rows, "quoted"); ink.fg != tuiColDim {
		t.Fatalf("quote text ink = %#v", ink)
	}
}

func TestTuiMarkdownTableLineHardWrapsWithoutInline(t *testing.T) {
	rows := tuiMarkdownRows("| `a` | b c |", 8, tuiInk{})
	tuiAssertWidths(t, rows, 8)
	if got := strings.Join(tuiPlainRows(rows), ""); got != "| `a` | b c |" {
		t.Fatalf("table text = %q", got)
	}
}

func TestTuiMarkdownBlankLinesCollapse(t *testing.T) {
	rows := tuiMarkdownRows("\n\nfirst\n\n\n\nsecond\n\n", 20, tuiInk{})
	want := []string{"first", "", "second"}
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, want) {
		t.Fatalf("blank collapse = %q, want %q", got, want)
	}
	if rows[1] != nil {
		t.Fatalf("blank row should be nil, got %#v", rows[1])
	}
}

func TestTuiMarkdownTabsExpand(t *testing.T) {
	rows := tuiMarkdownRows("a\tb", 20, tuiInk{})
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, []string{"a    b"}) {
		t.Fatalf("tab = %q", got)
	}
}

func TestTuiMarkdownControlCharactersStripped(t *testing.T) {
	rows := tuiMarkdownRows("a\x1b[31mb\x07c", 20, tuiInk{})
	for i, r := range rows {
		if strings.ContainsRune(r.plain(), '\x1b') || strings.ContainsRune(r.plain(), '\x07') {
			t.Fatalf("row %d kept a control rune: %q", i, r.plain())
		}
	}
	if got := tuiPlainRows(rows); !reflect.DeepEqual(got, []string{"a[31mbc"}) {
		t.Fatalf("stripped = %q", got)
	}
}

func TestTuiMarkdownEmptyAndWidthFloor(t *testing.T) {
	if got := tuiMarkdownRows("", 10, tuiInk{}); got != nil {
		t.Fatalf("empty = %#v, want nil", got)
	}
	rows := tuiMarkdownRows("ab", 0, tuiInk{})
	tuiAssertWidths(t, rows, 1)
}

func TestTuiMarkdownRowsStayWithinWidth(t *testing.T) {
	doc := strings.Join([]string{
		"# Heading with a long title that must wrap",
		"Paragraph with `inline code`, **bold words**, and 日本語 mixed in.",
		"- a bullet whose text is long enough to wrap onto more rows",
		"  1. a numbered child with a longer description",
		"> a quote that keeps going past the edge of the panel",
		"| table | row | with | many | cells | here |",
		"---",
		"```go",
		"func tooLongToFitAnywhere() { return nothingWillSplitThisWord }",
		"日本語のコードブロック日本語のコードブロック",
		"```",
		"",
		"",
		"Trailing text after the fence.",
	}, "\n")
	for _, width := range []int{8, 20, 60} {
		rows := tuiMarkdownRows(doc, width, tuiInk{fg: tuiColText})
		tuiAssertWidths(t, rows, width)
		if len(rows) > 0 && (rows[0] == nil || rows[len(rows)-1] == nil) {
			t.Fatalf("width %d: leading or trailing blank row", width)
		}
	}
}
