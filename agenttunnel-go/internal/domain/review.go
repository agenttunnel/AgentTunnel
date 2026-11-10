package domain

import (
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// ReviewRequest renders review comments as the user turn the team reads: one
// "file:line" (or "file:start-end") bullet per comment, with a multi-line body
// indented under it. Comments on removed lines say so, since their numbers are
// old-file numbers.
func ReviewRequest(comments []contracts.ReviewCommentAdded) string {
	var b strings.Builder
	b.WriteString("Review comments on the current changes. Address each one, then report what you changed.\n\n")
	for _, c := range comments {
		loc := fmt.Sprintf("%d", c.Line)
		if c.EndLine > c.Line {
			loc = fmt.Sprintf("%d-%d", c.Line, c.EndLine)
		}
		removed := ""
		if c.Side == "old" {
			removed = " (removed lines)"
		}
		body := strings.ReplaceAll(strings.TrimSpace(c.Body), "\n", "\n  ")
		fmt.Fprintf(&b, "- %s:%s%s: %s\n", c.File, loc, removed, body)
	}
	return strings.TrimRight(b.String(), "\n")
}
