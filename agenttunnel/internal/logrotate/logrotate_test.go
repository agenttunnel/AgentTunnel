package logrotate

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

func TestWritesPastTheCapKeepActiveFileBoundedAndOneBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atd.log")
	const capBytes = 1024
	w, err := New(path, capBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	line := strings.Repeat("x", 99) + "\n" // 100 bytes
	for i := 0; i < 50; i++ {              // 5000 bytes: several rotations
		if _, err := fmt.Fprintf(w, "%03d %s", i, line); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	if got := fileSize(t, path); got > capBytes {
		t.Fatalf("active file is %d bytes, want <= %d", got, capBytes)
	}
	backup := path + ".1"
	if got := fileSize(t, backup); got == 0 || got > capBytes {
		t.Fatalf("backup is %d bytes, want 0 < size <= %d", got, capBytes)
	}
	// The newest lines are in the active file; the backup holds the lines just
	// before the rotation, never anything newer than the active file.
	active, _ := os.ReadFile(path)
	if !strings.HasSuffix(string(active), "049 "+line) {
		t.Fatalf("active file does not end with the last write:\n%q", active)
	}
	old, _ := os.ReadFile(backup)
	if strings.Contains(string(old), "049 ") {
		t.Fatalf("backup holds the newest line; rotation order is wrong")
	}
}

func TestSingleOversizedWriteIsKeptWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atd.log")
	w, err := New(path, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	big := strings.Repeat("y", 64) + "\n"
	if _, err := w.Write([]byte(big)); err != nil {
		t.Fatal(err)
	}
	// An empty file is never rotated, so the first oversized line lands intact
	// in the active file and no backup appears.
	if got, _ := os.ReadFile(path); string(got) != big {
		t.Fatalf("oversized line was split or dropped: %q", got)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("unexpected backup for a single write: %v", err)
	}
}

// Goroutines writing through one Writer must not interleave bytes within a
// line. Every surviving line in the active file and the backup must be whole.
func TestConcurrentWritesDoNotTearLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atd.log")
	w, err := New(path, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	const writers, perWriter = 8, 300
	var wg sync.WaitGroup
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			body := strings.Repeat(string(rune('a'+g)), 120)
			for i := 0; i < perWriter; i++ {
				fmt.Fprintf(w, "w%d-%04d %s\n", g, i, body)
			}
		}(g)
	}
	wg.Wait()

	line := regexp.MustCompile(`^w(\d)-\d{4} ([a-h])+$`)
	seen := 0
	for _, p := range []string{path, path + ".1"} {
		f, err := os.Open(p)
		if err != nil {
			t.Fatalf("open %s: %v", p, err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			text := sc.Text()
			m := line.FindStringSubmatch(text)
			if m == nil {
				f.Close()
				t.Fatalf("torn line in %s: %q", p, text)
			}
			// The body must be the one letter the writer named in its prefix.
			want := string(rune('a' + int(m[1][0]-'0')))
			if strings.Trim(text[strings.Index(text, " ")+1:], want) != "" {
				f.Close()
				t.Fatalf("line mixes writers in %s: %q", p, text)
			}
			seen++
		}
		f.Close()
	}
	if seen == 0 {
		t.Fatal("no lines survived; the cap may be too small for this test")
	}
	if got := fileSize(t, path); got > 4096 {
		t.Fatalf("active file is %d bytes, want <= 4096", got)
	}
}

// Another process can rotate the same file. The next write must land in the
// file the path now names, not in the renamed backup.
func TestWriteFollowsAnExternalRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atd.log")
	w, err := New(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("before\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("after\n")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "after\n" {
		t.Fatalf("active file = %q, want the post-rotation write", got)
	}
}

func TestNewRejectsNonPositiveCap(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "x.log"), 0); err == nil {
		t.Fatal("New accepted a zero cap")
	}
}
