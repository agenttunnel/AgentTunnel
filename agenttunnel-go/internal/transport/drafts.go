package transport

import (
	"context"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// draftPrompt is the first prompt of a draft started without text: its title
// (unless still the placeholder) and its description. It is empty when the
// draft has neither, and the turn is then refused.
func draftPrompt(st *domain.ThreadState) string {
	var parts []string
	if st.Title != contracts.DefaultThreadTitle {
		parts = append(parts, st.Title)
	}
	if desc := strings.TrimSpace(st.Description); desc != "" {
		parts = append(parts, desc)
	}
	return strings.Join(parts, "\n\n")
}

// issueThread names the live thread already started from an issue in a project.
// Deleted threads release their issue. The check and the create are not atomic:
// two clients dropping the same issue at the same instant can both pass it.
func (d *Dispatcher) issueThread(ctx context.Context, projectID, url string) (contracts.ThreadID, bool, error) {
	ids, err := d.engine.Threads(ctx)
	if err != nil {
		return "", false, err
	}
	for _, id := range ids {
		st, err := d.engine.State(ctx, id)
		if err != nil {
			return "", false, err
		}
		if st.Exists && st.ProjectID == projectID && st.Issue != nil && st.Issue.URL == url {
			return id, true, nil
		}
	}
	return "", false, nil
}
