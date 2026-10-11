package gitdiff

import "testing"

const sample = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,4 +1,5 @@
 package main
 
-import "fmt"
+import (
+	"fmt"
+)
 
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
--- a/gone.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-old one
-old two
`

func TestParseDiffFilesAndCounts(t *testing.T) {
	d := ParseDiff(sample)
	if d.Empty {
		t.Fatal("diff should not be empty")
	}
	if len(d.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(d.Files))
	}

	main := d.Files[0]
	if main.Path != "main.go" {
		t.Fatalf("path = %q", main.Path)
	}
	if main.Additions != 3 || main.Deletions != 1 {
		t.Fatalf("adds/dels = %d/%d, want 3/1", main.Additions, main.Deletions)
	}

	gone := d.Files[1]
	if gone.Deletions != 2 {
		t.Fatalf("gone deletions = %d, want 2", gone.Deletions)
	}
}

func TestParseDiffLineNumbers(t *testing.T) {
	d := ParseDiff(sample)
	newNums := map[string]int{}
	for _, l := range d.Files[0].Lines {
		if l.Kind == KindAdd {
			newNums[l.Text] = l.New
		}
	}
	// After hunk start @1,4: context "package main"(1), blank(2), del, then adds
	// begin at new line 3.
	if newNums["import ("] != 3 {
		t.Fatalf("first add new line = %d, want 3", newNums["import ("])
	}
}

func TestParseDiffEmpty(t *testing.T) {
	if !ParseDiff("").Empty {
		t.Fatal("empty input should be an empty diff")
	}
}
