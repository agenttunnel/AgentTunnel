package contextstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// fixedNotebook opens a thread's notebook with a deterministic clock, so
// timestamps can be compared exactly after replay.
func fixedNotebook(t *testing.T, s *NotebookStore, id string) *Notebook {
	t.Helper()
	nb := s.Open(id, nil)
	base := time.Date(2026, 11, 1, 10, 0, 0, 123456789, time.UTC)
	ticks := 0
	nb.clock = func() time.Time {
		ticks++
		return base.Add(time.Duration(ticks) * time.Second)
	}
	return nb
}

func sameEntries(t *testing.T, key string, got, want []Entry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d entries, want %d: %+v", key, len(got), len(want), got)
	}
	for i := range want {
		if got[i].Author != want[i].Author || got[i].Value != want[i].Value || !got[i].At.Equal(want[i].At) {
			t.Fatalf("%s[%d] = %+v, want %+v", key, i, got[i], want[i])
		}
	}
}

func TestNotebookStoreReplaysSetAppendOrderAndTimestamps(t *testing.T) {
	s := NewNotebookStore(t.TempDir())
	nb := fixedNotebook(t, s, "t1")
	nb.Append("result_b", "b1", "agent-b")
	nb.Set("notes", "n1", "orchestrator")
	nb.Append("result_a", "a1", "agent-a")
	nb.Append("result_b", "b2", "agent-b")
	nb.Set(curatedKey, "c1", "curator")
	nb.Set(curatedKey, "c2", "curator")

	re := s.Open("t1", nil)
	if got, want := re.Keys(), []string{"result_b", "notes", "result_a", curatedKey}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for _, key := range nb.Keys() {
		sameEntries(t, key, re.Get(key), nb.Get(key))
	}
	// Set replaced the curated context, and the replayed state agrees.
	sameEntries(t, curatedKey, re.Get(curatedKey), []Entry{{Author: "curator", Value: "c2", At: nb.Get(curatedKey)[0].At}})
}

func TestNotebookStoreReplaysMixedSetAndAppendWithinOneKey(t *testing.T) {
	s := NewNotebookStore(t.TempDir())
	nb := fixedNotebook(t, s, "t1")
	nb.Append("k", "a", "x")
	nb.Append("k", "b", "x")
	nb.Set("k", "c", "y")
	nb.Append("k", "d", "z")

	re := s.Open("t1", nil)
	sameEntries(t, "k", re.Get("k"), nb.Get("k"))
	if got := re.Get("k"); len(got) != 2 || got[0].Value != "c" || got[1].Value != "d" {
		t.Fatalf("k = %+v, want [c d]", got)
	}
}

func TestNotebookStoreSkipsTornLastLineAndKeepsLaterWrites(t *testing.T) {
	dir := t.TempDir()
	s := NewNotebookStore(dir)
	nb := fixedNotebook(t, s, "t1")
	nb.Append("notes", "kept", "a")
	nb.Set("result_w", "kept too", "b")

	// A crash mid-write leaves a partial record with no newline.
	f, err := os.OpenFile(filepath.Join(dir, "t1.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"op":"append","key":"notes","author":"x","val`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	re := s.Open("t1", nil)
	if got := re.Get("notes"); len(got) != 1 || got[0].Value != "kept" {
		t.Fatalf("notes = %+v, want the complete record", got)
	}
	// The next write must not merge with the torn fragment.
	re.Append("notes", "after restart", "c")
	again := s.Open("t1", nil)
	if got := again.Get("notes"); len(got) != 2 || got[1].Value != "after restart" {
		t.Fatalf("notes after a write past a torn tail = %+v", got)
	}
	if got := again.Get("result_w"); len(got) != 1 || got[0].Value != "kept too" {
		t.Fatalf("result_w = %+v", got)
	}
}

func TestNotebookStoreSkipsCorruptMiddleLineWithoutLosingGoodRecords(t *testing.T) {
	dir := t.TempDir()
	s := NewNotebookStore(dir)
	nb := fixedNotebook(t, s, "t1")
	nb.Append("notes", "first", "a")
	nb.Append("notes", "third", "a")

	path := filepath.Join(dir, "t1.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	garbled := lines[0] + "not json at all\n" + `{"op":"sideways","key":"x"}` + "\n" + lines[1]
	if err := os.WriteFile(path, []byte(garbled), 0o644); err != nil {
		t.Fatal(err)
	}

	re := s.Open("t1", nil)
	got := re.Get("notes")
	if len(got) != 2 || got[0].Value != "first" || got[1].Value != "third" {
		t.Fatalf("notes = %+v, want both good records", got)
	}
	// The rewrite drops the bad lines, so they are not re-read on every boot.
	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), "not json") || strings.Contains(string(after), "sideways") {
		t.Fatalf("corrupt lines survived compaction:\n%s", after)
	}
}

func TestNotebookStoreCompactsSupersededRecordsOnLoad(t *testing.T) {
	dir := t.TempDir()
	s := NewNotebookStore(dir)
	nb := fixedNotebook(t, s, "t1")
	for i := 0; i < 200; i++ {
		nb.Set(curatedKey, fmt.Sprintf("summary %d", i), "curator")
	}
	nb.Append("notes", "note", "a")
	path := filepath.Join(dir, "t1.jsonl")

	if lines := countLines(t, path); lines != 201 {
		t.Fatalf("before load: %d lines, want 201", lines)
	}
	re := s.Open("t1", nil)
	if lines := countLines(t, path); lines != 2 {
		t.Fatalf("after load: %d lines, want 2 (one live record per entry)", lines)
	}
	if got := re.Get(curatedKey); len(got) != 1 || got[0].Value != "summary 199" {
		t.Fatalf("context = %+v", got)
	}

	// Compacted state replays to the same notebook on the next boot.
	sameEntries(t, "notes", s.Open("t1", nil).Get("notes"), nb.Get("notes"))
}

func TestNotebookStoreCompactionKeepsKeyOrderAndMixedEntries(t *testing.T) {
	dir := t.TempDir()
	s := NewNotebookStore(dir)
	nb := fixedNotebook(t, s, "t1")
	nb.Append("a", "a1", "x")
	nb.Append("b", "b1", "x")
	nb.Append("a", "a2", "x")
	nb.Set("a", "a3", "y")
	nb.Append("a", "a4", "z")

	re := s.Open("t1", nil)
	if got, want := strings.Join(re.Keys(), ","), "a,b"; got != want {
		t.Fatalf("keys = %s, want %s", got, want)
	}
	sameEntries(t, "a", re.Get("a"), nb.Get("a"))
	sameEntries(t, "b", re.Get("b"), nb.Get("b"))
	if lines := countLines(t, filepath.Join(dir, "t1.jsonl")); lines != 3 {
		t.Fatalf("lines after compaction = %d, want 3", lines)
	}
}

func TestNotebookStoreConfinesThreadIDsToItsDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "notebooks")
	s := NewNotebookStore(dir)
	ids := []string{"../../escape", "..", ".", "", "/tmp/abs", "a/../../b", `..\..\win`, "nul\x00byte", "../notebooks/../../x"}
	for _, id := range ids {
		if got := filepath.Dir(s.pathFor(id)); got != dir {
			t.Fatalf("id %q resolves to %q, outside %q", id, got, dir)
		}
		nb := s.Open(id, nil)
		nb.Append("notes", "x", "agent")
	}
	if err := s.Remove("../../escape"); err != nil {
		t.Fatalf("remove traversal id: %v", err)
	}

	top, _ := os.ReadDir(root)
	if len(top) != 1 || top[0].Name() != "notebooks" {
		t.Fatalf("traversal ids wrote outside the notebooks dir: %v", names(top))
	}
	inside, _ := os.ReadDir(dir)
	for _, e := range inside {
		if strings.ContainsAny(e.Name(), `/\`) {
			t.Fatalf("file name %q carries a separator", e.Name())
		}
	}
}

func TestNotebookWriteFailureIsReportedAndTurnKeepsItsNote(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "notebooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewNotebookStore(dir)
	reports := make(chan error, 4)
	nb := s.Open("t1", func(err error) { reports <- err })

	// The directory becomes a regular file, so the next write cannot create its path.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}

	nb.Append("notes", "still in memory", "agent")
	if got := nb.Get("notes"); len(got) != 1 || got[0].Value != "still in memory" {
		t.Fatalf("memory lost the note after a failed write: %+v", got)
	}
	select {
	case err := <-reports:
		if err == nil {
			t.Fatal("reported a nil error")
		}
	default:
		t.Fatal("write failure was not reported")
	}
}

func TestDiscardedNotebookCannotRecreateItsFile(t *testing.T) {
	s := NewNotebookStore(t.TempDir())
	nb := fixedNotebook(t, s, "t1")
	nb.Append("notes", "old", "agent")

	if err := s.Remove("t1"); err != nil {
		t.Fatal(err)
	}
	nb.Discard()
	nb.Append("notes", "late write from a stale holder", "agent")

	if _, err := os.Stat(filepath.Join(s.dir, "t1.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("discarded notebook recreated its file: %v", err)
	}
	if fresh := s.Open("t1", nil); len(fresh.Keys()) != 0 {
		t.Fatalf("recreated id resurrected notes: %v", fresh.Keys())
	}
}

func TestConcurrentWritesReplayInMemoryOrder(t *testing.T) {
	s := NewNotebookStore(t.TempDir())
	nb := s.Open("t1", nil)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				nb.Append(fmt.Sprintf("result_%d", w%3), fmt.Sprintf("w%d-%d", w, i), fmt.Sprintf("agent-%d", w))
			}
		}(w)
	}
	wg.Wait()

	re := s.Open("t1", nil)
	for _, key := range nb.Keys() {
		sameEntries(t, key, re.Get(key), nb.Get(key))
	}
}

// curatedKey stands in for the orchestrator's ContextKey, which this package
// cannot import.
const curatedKey = "context"

// A complete record whose trailing newline is missing (a file cut right after
// the closing brace) must not merge with the next write: both records survive.
func TestNotebookStoreAppendsAfterUnterminatedCompleteRecord(t *testing.T) {
	s := NewNotebookStore(t.TempDir())
	line := `{"op":"set","key":"a","author":"x","value":"v","at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(s.pathFor("t1"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	nb := s.Open("t1", nil)
	nb.Set("b", "w", "y")

	again := s.Open("t1", nil)
	if len(again.Get("a")) != 1 || len(again.Get("b")) != 1 {
		t.Fatalf("after unterminated record + Set: a=%+v b=%+v", again.Get("a"), again.Get("b"))
	}
}

// Invalid UTF-8 is cleaned before the write, so memory and a reload hold the
// same text. Without that, json.Marshal would change the bytes on disk only.
func TestNotebookStoreMemoryMatchesReloadForInvalidUTF8(t *testing.T) {
	s := NewNotebookStore(t.TempDir())
	nb := fixedNotebook(t, s, "t1")
	nb.Set("result_\xff", "bad\xc3(bytes\xfe", "agent")
	nb.Append("notes", "ok", "agent\xfe")

	for _, key := range nb.Keys() {
		if !utf8.ValidString(key) {
			t.Fatalf("key %q is not valid UTF-8", key)
		}
		for _, e := range nb.Get(key) {
			if !utf8.ValidString(e.Value) || !utf8.ValidString(e.Author) {
				t.Fatalf("entry %+v holds invalid UTF-8 in memory", e)
			}
		}
	}
	re := s.Open("t1", nil)
	if strings.Join(re.Keys(), ",") != strings.Join(nb.Keys(), ",") {
		t.Fatalf("keys after reload = %q, want %q", re.Keys(), nb.Keys())
	}
	for _, key := range nb.Keys() {
		sameEntries(t, key, re.Get(key), nb.Get(key))
	}
}

// A compaction temp file left by a crash is swept when the store is created.
// The live notebook file beside it is untouched.
func TestNotebookStoreSweepsCompactionTempFilesFromACrash(t *testing.T) {
	dir := t.TempDir()
	s := NewNotebookStore(dir)
	fixedNotebook(t, s, "t1").Append("notes", "kept", "agent")
	stale := filepath.Join(dir, "t1.jsonl.tmp-424242")
	if err := os.WriteFile(stale, []byte("half a compaction"), 0o644); err != nil {
		t.Fatal(err)
	}

	NewNotebookStore(dir)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("compaction temp file survived the sweep: %v", err)
	}
	if got := s.Open("t1", nil).Get("notes"); len(got) != 1 || got[0].Value != "kept" {
		t.Fatalf("live notebook damaged by the sweep: %+v", got)
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestNotebookEditAndRemoveReplayFromFile(t *testing.T) {
	s := NewNotebookStore(t.TempDir())
	nb := fixedNotebook(t, s, "t1")
	nb.Append("notes", "first", "orchestrator")
	nb.Append("notes", "second", "orchestrator")
	nb.Append("notes", "third", "orchestrator")
	nb.Set("result_a", "only", "agent-a")

	if err := nb.Edit("notes", 1, "second, corrected", UserAuthor); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := nb.Remove("notes", 0); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := nb.Remove("result_a", 0); err != nil {
		t.Fatalf("remove last entry: %v", err)
	}

	re := s.Open("t1", nil)
	sameEntries(t, "notes", re.Get("notes"), nb.Get("notes"))
	if got := re.Get("notes"); len(got) != 2 || got[0].Value != "second, corrected" || got[0].Author != UserAuthor {
		t.Fatalf("notes = %+v, want edited entry attributed to the user", got)
	}
	if got := re.Keys(); strings.Join(got, ",") != "notes" {
		t.Fatalf("keys = %v, want [notes]: an emptied key must leave the notebook", got)
	}
	// The file holds only live entries after the rewrites.
	data, _ := os.ReadFile(filepath.Join(s.dir, "t1.jsonl"))
	if n := strings.Count(string(data), "\n"); n != 2 {
		t.Fatalf("file has %d records, want 2:\n%s", n, data)
	}
}

func TestNotebookEditAndRemoveRejectBadIndex(t *testing.T) {
	nb := NewNotebook()
	nb.Set("k", "v", "a")
	if err := nb.Edit("k", 1, "x", UserAuthor); err != ErrNoEntry {
		t.Fatalf("edit out of range = %v, want ErrNoEntry", err)
	}
	if err := nb.Remove("missing", 0); err != ErrNoEntry {
		t.Fatalf("remove missing key = %v, want ErrNoEntry", err)
	}
	if got := nb.Get("k"); len(got) != 1 || got[0].Value != "v" {
		t.Fatalf("rejected edit changed the notebook: %+v", got)
	}
}

func TestNotebookEditRevertsWhenFileWriteFails(t *testing.T) {
	dir := t.TempDir()
	s := NewNotebookStore(dir)
	nb := fixedNotebook(t, s, "t1")
	nb.Set("k", "before", "a")

	// A directory where the thread file should be makes the rewrite fail.
	if err := os.Remove(filepath.Join(dir, "t1.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "t1.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := nb.Edit("k", 0, "after", UserAuthor); err == nil {
		t.Fatal("edit succeeded although the file could not be rewritten")
	}
	if got := nb.Get("k"); got[0].Value != "before" {
		t.Fatalf("memory = %q, want the value before the failed edit", got[0].Value)
	}
}
