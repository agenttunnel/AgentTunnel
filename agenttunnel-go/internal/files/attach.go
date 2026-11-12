package files

import (
	"fmt"
	"os"
	"strings"
)

// Attachment caps keep a turn's prompt bounded: a single file over
// MaxAttachFileBytes is listed by path only, and once the inlined total reaches
// MaxAttachTotalBytes the remaining files are listed too.
const (
	MaxAttachments      = 20
	MaxAttachFileBytes  = 64 * 1024
	MaxAttachTotalBytes = 256 * 1024
)

// AttachmentBlock renders composer attachments as a prompt section. Each path
// is re-resolved inside workdir (it must exist and must not escape it). Text
// files under the caps are inlined; folders, binaries and oversized files are
// listed by path so the agent can read them itself. Returns "" for none.
func AttachmentBlock(workdir string, rels []string) (string, error) {
	if len(rels) == 0 {
		return "", nil
	}
	if len(rels) > MaxAttachments {
		return "", fmt.Errorf("too many attachments: %d (max %d)", len(rels), MaxAttachments)
	}
	var b strings.Builder
	b.WriteString("Attached context from the composer:\n")
	seen := map[string]bool{}
	inlined := 0
	for _, rel := range rels {
		full, err := Resolve(workdir, rel)
		if err != nil {
			return "", fmt.Errorf("attachment %q: %w", rel, err)
		}
		if seen[full] {
			continue
		}
		seen[full] = true
		info, err := os.Stat(full)
		if err != nil {
			return "", fmt.Errorf("attachment %q: %w", rel, err)
		}
		clean := strings.TrimPrefix(rel, "/")
		if info.IsDir() {
			fmt.Fprintf(&b, "\n- %s/ (folder)\n", clean)
			continue
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("attachment %q is not a file or folder", rel)
		}
		if info.Size() > MaxAttachFileBytes || inlined+int(info.Size()) > MaxAttachTotalBytes {
			fmt.Fprintf(&b, "\n- %s (%d bytes, not inlined; read it if needed)\n", clean, info.Size())
			continue
		}
		res, err := Read(workdir, clean)
		if err != nil {
			return "", fmt.Errorf("attachment %q: %w", rel, err)
		}
		if res.Binary {
			fmt.Fprintf(&b, "\n- %s (binary, not inlined)\n", clean)
			continue
		}
		inlined += len(res.Text)
		fence := fenceFor(res.Text)
		fmt.Fprintf(&b, "\n### %s\n%s\n%s", clean, fence, res.Text)
		if !strings.HasSuffix(res.Text, "\n") {
			b.WriteString("\n")
		}
		b.WriteString(fence + "\n")
	}
	return b.String(), nil
}

// fenceFor returns a code fence longer than any backtick run in text, so file
// contents cannot close the fence early.
func fenceFor(text string) string {
	longest, run := 0, 0
	for i := 0; i < len(text); i++ {
		if text[i] == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}
