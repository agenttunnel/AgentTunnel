package domain

import (
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// ReviewRequest renders review comments as the user turn the team reads: one
// "file:line" bullet per comment, with a multi-line body indented under it.
func ReviewRequest(comments []contracts.ReviewCommentAdded) string {
	var b strings.Builder
	b.WriteString("Review comments on the current changes. Address each one, then report what you changed.\n\n")
	for _, c := range comments {
		body := strings.ReplaceAll(strings.TrimSpace(c.Body), "\n", "\n  ")
		fmt.Fprintf(&b, "- %s:%d: %s\n", c.File, c.Line, body)
	}
	return strings.TrimRight(b.String(), "\n")
}
