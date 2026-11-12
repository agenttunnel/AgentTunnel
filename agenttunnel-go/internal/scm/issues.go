package scm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// issueListLimit is one glab page. glab lists a single page, so this is the cap
// on how many assigned issues the board shows.
const issueListLimit = 50

// maxIssueBodyRunes caps an issue description. It matches the draft
// description cap in domain: a dropped issue becomes the draft's spec, so the
// body must arrive whole. The board does not render descriptions.
const maxIssueBodyRunes = 20000

// Issue is an open issue assigned to the current user on the forge.
type Issue struct {
	IID         int       `json:"iid"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	WebURL      string    `json:"webUrl"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// IssueList is the issues assigned to the user in one project.
type IssueList struct {
	Status   Status  `json:"status"`
	Message  string  `json:"message,omitempty"`
	Provider string  `json:"provider,omitempty"`
	Issues   []Issue `json:"issues"`
}

// ListIssues lists the open issues assigned to the user in the GitLab project
// behind workdir's origin. Read-only, like Fetch.
func ListIssues(ctx context.Context, workdir string) IssueList {
	return Client{LookPath: exec.LookPath, Run: execRun}.ListIssues(ctx, workdir)
}

// ListIssues reads assigned issues through glab. Only GitLab is supported; the
// GitHub path never reaches gh.
func (c Client) ListIssues(ctx context.Context, workdir string) IssueList {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	remote, ok := c.origin(ctx, workdir)
	if !ok || providerFor(remote) != "gitlab" {
		return IssueList{Status: StatusUnsupported, Message: "Issues are read from GitLab only"}
	}
	if _, err := c.LookPath("glab"); err != nil {
		return IssueList{Status: StatusUnavailable, Provider: "gitlab", Message: "glab is not installed"}
	}
	out, stderr, err := c.Run(ctx, workdir, "glab", "issue", "list",
		"--assignee=@me", "--output", "json", "--per-page", strconv.Itoa(issueListLimit))
	if err != nil {
		return classifyIssues(stderr, err)
	}
	issues, err := parseIssues(out)
	if err != nil {
		return IssueList{Status: StatusError, Provider: "gitlab", Message: err.Error()}
	}
	return IssueList{Status: StatusReady, Provider: "gitlab", Issues: issues}
}

// classifyIssues maps a failed glab call. Unlike classify, there is no "no
// request" case: an empty listing is a success.
func classifyIssues(stderr string, err error) IssueList {
	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "not logged in") || strings.Contains(lower, "glab auth login") {
		return IssueList{Status: StatusUnauthenticated, Provider: "gitlab", Message: "Not logged in. Run glab auth login."}
	}
	return IssueList{Status: StatusError, Provider: "gitlab", Message: firstLine(stderr, err.Error())}
}

type glIssue struct {
	IID         int       `json:"iid"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	WebURL      string    `json:"web_url"`
	State       string    `json:"state"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// parseIssues keeps open issues only and caps each description at the draft limit. Empty output
// is an empty listing, so the result is never nil and always encodes as [].
func parseIssues(raw []byte) ([]Issue, error) {
	issues := []Issue{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return issues, nil
	}
	var rows []glIssue
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("could not read glab output: %w", err)
	}
	for _, r := range rows {
		if r.State != "opened" {
			continue
		}
		issues = append(issues, Issue{
			IID: r.IID, Title: r.Title, Description: capRunes(r.Description, maxIssueBodyRunes),
			WebURL: r.WebURL, UpdatedAt: r.UpdatedAt,
		})
	}
	return issues, nil
}
