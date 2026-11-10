package scm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// maxFoundRequests bounds how many of a branch's requests discovery asks for.
const maxFoundRequests = 100

// Listing is what the Pull Request view shows for one thread: the forge's answer
// about the thread's branch, and the tracked requests read from the forge.
type Listing struct {
	Status   Status        `json:"status"`
	Message  string        `json:"message,omitempty"`
	Provider string        `json:"provider,omitempty"`
	Branch   string        `json:"branch,omitempty"`
	Requests []PullRequest `json:"requests"`
}

// PullRequest is the read-only summary of one request the thread opened. Only
// the number and URL are stored by the daemon; everything else is read from the
// forge each time the view is shown. Error is set when that read failed.
type PullRequest struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	URL       string   `json:"url"`
	State     string   `json:"state"` // open | merged | closed
	Draft     bool     `json:"draft"`
	Comments  int      `json:"comments"`
	Commits   int      `json:"commits"`
	Assignees []string `json:"assignees"`
	Reviewers []string `json:"reviewers"`
	Error     string   `json:"error,omitempty"`
}

// Discover asks the forge for every pull request whose head is the branch in
// workdir, open or closed. It returns the status for the branch and the
// requests found; refs is empty unless the forge answered.
func (c Client) Discover(ctx context.Context, workdir string) (Listing, []contracts.PullRequestRef) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	provider, branch, blocked, ok := c.resolve(ctx, workdir)
	if !ok {
		return Listing{Status: blocked.Status, Message: blocked.Message, Provider: blocked.Provider, Branch: blocked.Branch}, nil
	}
	list := Listing{Status: StatusReady, Provider: provider, Branch: branch}
	if provider != "github" {
		// Discovery reads GitHub's head-branch search; GitLab requests are not tracked yet.
		return Listing{Status: StatusUnsupported, Provider: provider, Branch: branch,
			Message: "Pull requests are tracked for GitHub only"}, nil
	}

	out, stderr, err := c.Run(ctx, workdir, "gh", "pr", "list", "--head", branch, "--state", "all",
		"--json", "number,url", "--limit", fmt.Sprint(maxFoundRequests))
	if err != nil {
		snap := classify(stderr, err, branch, "no pull requests found", "gh auth login")
		list.Status, list.Message = snap.Status, snap.Message
		return list, nil
	}
	var found []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(out, &found); err != nil {
		list.Status, list.Message = StatusError, "could not read gh output: "+err.Error()
		return list, nil
	}
	refs := make([]contracts.PullRequestRef, 0, len(found))
	for _, f := range found {
		refs = append(refs, contracts.PullRequestRef{Number: f.Number, URL: f.URL})
	}
	return list, refs
}

// Details fills in the forge's view of each tracked request. When the forge could
// not be reached, every request carries that reason instead of a CLI call that
// is known to fail.
func (c Client) Details(ctx context.Context, workdir string, list Listing, refs []contracts.PullRequestRef) Listing {
	list.Requests = make([]PullRequest, 0, len(refs))
	for _, ref := range refs {
		if list.Status != StatusReady {
			list.Requests = append(list.Requests, PullRequest{Number: ref.Number, URL: ref.URL, Error: list.Message})
			continue
		}
		list.Requests = append(list.Requests, c.readRequest(ctx, workdir, ref))
	}
	return list
}

func (c Client) readRequest(ctx context.Context, workdir string, ref contracts.PullRequestRef) PullRequest {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	out, stderr, err := c.Run(ctx, workdir, "gh", "pr", "view", ref.URL, "--json",
		"number,title,url,state,isDraft,comments,commits,assignees,reviewRequests,reviews")
	if err != nil {
		return PullRequest{Number: ref.Number, URL: ref.URL, Error: firstLine(stderr, err.Error())}
	}
	pr, err := parseRequest(out)
	if err != nil {
		return PullRequest{Number: ref.Number, URL: ref.URL, Error: err.Error()}
	}
	return pr
}

type ghRequested struct {
	Login string `json:"login"` // a user
	Name  string `json:"name"`  // a team
}

type ghRequestDetail struct {
	Number         int           `json:"number"`
	Title          string        `json:"title"`
	URL            string        `json:"url"`
	State          string        `json:"state"`
	IsDraft        bool          `json:"isDraft"`
	Comments       []ghComment   `json:"comments"`
	Commits        []struct{}    `json:"commits"` // counted, not read
	Assignees      []ghActor     `json:"assignees"`
	ReviewRequests []ghRequested `json:"reviewRequests"`
	Reviews        []ghReview    `json:"reviews"`
}

func parseRequest(raw []byte) (PullRequest, error) {
	var d ghRequestDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		return PullRequest{}, fmt.Errorf("could not read gh output: %w", err)
	}
	pr := PullRequest{
		Number:   d.Number,
		Title:    d.Title,
		URL:      d.URL,
		State:    strings.ToLower(d.State),
		Draft:    d.IsDraft,
		Comments: len(d.Comments),
		Commits:  len(d.Commits),
	}
	for _, a := range d.Assignees {
		pr.Assignees = append(pr.Assignees, a.Login)
	}
	// Reviewers are the people asked to review plus anyone who already has.
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			pr.Reviewers = append(pr.Reviewers, name)
		}
	}
	for _, r := range d.ReviewRequests {
		if r.Login != "" {
			add(r.Login)
		} else {
			add(r.Name)
		}
	}
	for _, r := range d.Reviews {
		add(r.Author.Login)
	}
	return pr, nil
}
