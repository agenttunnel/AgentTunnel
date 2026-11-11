// Package scm reads the pull request (GitHub) or merge request (GitLab) for a
// thread's branch through the gh and glab CLIs. The daemon never holds forge
// credentials: whatever the user is logged into in their CLI is what we see,
// and a missing or logged-out CLI degrades to a status instead of an error.
package scm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Status says what the snapshot can tell the client about the branch.
type Status string

const (
	StatusReady           Status = "ready"           // a PR/MR exists for the branch
	StatusNone            Status = "none"            // no PR/MR for this branch yet
	StatusUnavailable     Status = "unavailable"     // the forge CLI is not installed
	StatusUnauthenticated Status = "unauthenticated" // installed but not logged in
	StatusUnsupported     Status = "unsupported"     // origin is not GitHub or GitLab
	StatusError           Status = "error"           // the CLI ran and failed
)

const (
	fetchTimeout = 20 * time.Second
	maxComments  = 20
	maxBodyRunes = 2000
)

// Snapshot is the PR/MR view for one working directory.
type Snapshot struct {
	Status   Status   `json:"status"`
	Message  string   `json:"message,omitempty"`
	Provider string   `json:"provider,omitempty"` // "github" | "gitlab"
	Branch   string   `json:"branch,omitempty"`
	Request  *Request `json:"request,omitempty"`
}

// Request is the forge's pull or merge request for the branch.
type Request struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	State     string    `json:"state"` // open | merged | closed
	Draft     bool      `json:"draft"`
	Review    string    `json:"review"` // approved | changes_requested | review_required | none; "" when the forge does not report it
	Checks    string    `json:"checks"` // passing | failing | pending | none
	CheckRuns []Check   `json:"checkRuns"`
	Comments  []Comment `json:"comments"` // newest last; nil for GitLab until notes are wired
}

// Check is one CI check or commit status on the request's head.
type Check struct {
	Name  string `json:"name"`
	State string `json:"state"` // passing | failing | pending | none
}

// Comment is a conversation comment or a review with a body.
type Comment struct {
	Author      string    `json:"author"`
	Body        string    `json:"body"`
	URL         string    `json:"url,omitempty"`
	Kind        string    `json:"kind"` // comment | review
	ReviewState string    `json:"reviewState,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Runner runs a command in dir and returns its stdout. stderr is returned
// separately so callers can tell "no PR" or "not logged in" apart from a crash.
type Runner func(ctx context.Context, dir, name string, args ...string) (stdout []byte, stderr string, err error)

// Client holds the two side-effecting seams so tests can script the CLIs.
type Client struct {
	LookPath func(name string) (string, error)
	Run      Runner
}

// Fetch reads the PR/MR for the branch checked out in workdir using the real
// CLIs. Read-only: it never writes to the forge or to daemon state.
func Fetch(ctx context.Context, workdir string) Snapshot {
	return Client{LookPath: exec.LookPath, Run: execRun}.Fetch(ctx, workdir)
}

// Fetch resolves the forge from origin, then asks its CLI for the branch's request.
func (c Client) Fetch(ctx context.Context, workdir string) Snapshot {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	provider, branch, blocked, ok := c.resolve(ctx, workdir)
	if !ok {
		return blocked
	}

	var snap Snapshot
	if provider == "github" {
		snap = c.fetchGitHub(ctx, workdir, branch)
	} else {
		snap = c.fetchGitLab(ctx, workdir, branch)
	}
	snap.Provider = provider
	snap.Branch = branch
	return snap
}

// resolve finds the branch checked out in workdir and the forge behind origin,
// and checks that the forge CLI is installed. When any of those fails it
// returns ok=false with the snapshot that says why.
func (c Client) resolve(ctx context.Context, workdir string) (provider, branch string, blocked Snapshot, ok bool) {
	out, stderr, err := c.Run(ctx, workdir, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", "", Snapshot{Status: StatusNone, Message: firstLine(stderr, "Not a git repository")}, false
	}
	branch = strings.TrimSpace(string(out))
	if branch == "" || branch == "HEAD" {
		return "", "", Snapshot{Status: StatusNone, Message: "Detached HEAD: no branch to look up"}, false
	}

	out, _, err = c.Run(ctx, workdir, "git", "remote", "get-url", "origin")
	if err != nil {
		return "", "", Snapshot{Status: StatusUnsupported, Branch: branch, Message: "No origin remote"}, false
	}
	provider = providerFor(strings.TrimSpace(string(out)))
	if provider == "" {
		return "", "", Snapshot{Status: StatusUnsupported, Branch: branch, Message: "origin is not GitHub or GitLab"}, false
	}

	cli := map[string]string{"github": "gh", "gitlab": "glab"}[provider]
	if _, err := c.LookPath(cli); err != nil {
		return "", "", Snapshot{Status: StatusUnavailable, Provider: provider, Branch: branch,
			Message: cli + " is not installed"}, false
	}
	return provider, branch, Snapshot{}, true
}

func (c Client) fetchGitHub(ctx context.Context, workdir, branch string) Snapshot {
	out, stderr, err := c.Run(ctx, workdir, "gh", "pr", "view", "--json",
		"number,title,url,state,isDraft,reviewDecision,statusCheckRollup,comments,reviews")
	if err != nil {
		return classify(stderr, err, branch, "no pull requests found", "gh auth login")
	}
	req, err := parseGitHub(out)
	if err != nil {
		return Snapshot{Status: StatusError, Message: err.Error()}
	}
	return Snapshot{Status: StatusReady, Request: req}
}

func (c Client) fetchGitLab(ctx context.Context, workdir, branch string) Snapshot {
	out, stderr, err := c.Run(ctx, workdir, "glab", "mr", "view", "--output", "json")
	if err != nil {
		return classify(stderr, err, branch, "no open merge request", "glab auth login")
	}
	req, err := parseGitLab(out)
	if err != nil {
		return Snapshot{Status: StatusError, Message: err.Error()}
	}
	return Snapshot{Status: StatusReady, Request: req}
}

// classify maps a failed CLI call onto a status. The CLIs have no stable exit
// codes for these cases, so the match is on their stderr wording.
func classify(stderr string, err error, branch, noRequestText, loginHint string) Snapshot {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, noRequestText):
		return Snapshot{Status: StatusNone, Message: "No request open for " + branch}
	case strings.Contains(lower, loginHint) || strings.Contains(lower, "not logged in"):
		return Snapshot{Status: StatusUnauthenticated, Message: "Not logged in. Run " + loginHint + "."}
	}
	return Snapshot{Status: StatusError, Message: firstLine(stderr, err.Error())}
}

// providerFor reads the host out of an https or scp-style git remote.
func providerFor(remote string) string {
	host := remote
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
		if j := strings.Index(host, "/"); j >= 0 {
			host = host[:j]
		}
	} else if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	host = strings.ToLower(host)
	switch {
	case strings.Contains(host, "github"):
		return "github"
	case strings.Contains(host, "gitlab"):
		return "gitlab"
	}
	return ""
}

type ghActor struct {
	Login string `json:"login"`
}

type ghCheck struct {
	TypeName   string `json:"__typename"`
	Name       string `json:"name"`
	Context    string `json:"context"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

type ghComment struct {
	Author    ghActor   `json:"author"`
	Body      string    `json:"body"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"createdAt"`
}

type ghReview struct {
	Author      ghActor   `json:"author"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	URL         string    `json:"url"`
	SubmittedAt time.Time `json:"submittedAt"`
}

type ghPR struct {
	Number            int         `json:"number"`
	Title             string      `json:"title"`
	URL               string      `json:"url"`
	State             string      `json:"state"`
	IsDraft           bool        `json:"isDraft"`
	ReviewDecision    string      `json:"reviewDecision"`
	StatusCheckRollup []ghCheck   `json:"statusCheckRollup"`
	Comments          []ghComment `json:"comments"`
	Reviews           []ghReview  `json:"reviews"`
}

func parseGitHub(raw []byte) (*Request, error) {
	var pr ghPR
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, fmt.Errorf("could not read gh output: %w", err)
	}
	req := &Request{
		Number: pr.Number,
		Title:  pr.Title,
		URL:    pr.URL,
		State:  strings.ToLower(pr.State),
		Draft:  pr.IsDraft,
		Review: map[string]string{
			"APPROVED":          "approved",
			"CHANGES_REQUESTED": "changes_requested",
			"REVIEW_REQUIRED":   "review_required",
		}[pr.ReviewDecision],
	}
	if req.Review == "" {
		req.Review = "none"
	}

	states := make([]string, 0, len(pr.StatusCheckRollup))
	for _, c := range pr.StatusCheckRollup {
		name, state := c.Name, checkRunState(c.Status, c.Conclusion)
		if c.TypeName == "StatusContext" || c.Context != "" {
			name, state = c.Context, statusContextState(c.State)
		}
		req.CheckRuns = append(req.CheckRuns, Check{Name: name, State: state})
		states = append(states, state)
	}
	req.Checks = aggregate(states)

	var comments []Comment
	for _, c := range pr.Comments {
		comments = append(comments, Comment{
			Author: c.Author.Login, Body: c.Body, URL: c.URL, Kind: "comment", CreatedAt: c.CreatedAt,
		})
	}
	for _, r := range pr.Reviews {
		if strings.TrimSpace(r.Body) == "" {
			continue // approvals without text are already in Review
		}
		comments = append(comments, Comment{
			Author: r.Author.Login, Body: r.Body, URL: r.URL, Kind: "review",
			ReviewState: strings.ToLower(r.State), CreatedAt: r.SubmittedAt,
		})
	}
	req.Comments = recentComments(comments)
	return req, nil
}

type glPipeline struct {
	Status string `json:"status"`
}

type glMR struct {
	IID                 int         `json:"iid"`
	Title               string      `json:"title"`
	WebURL              string      `json:"web_url"`
	State               string      `json:"state"`
	Draft               bool        `json:"draft"`
	WorkInProgress      bool        `json:"work_in_progress"`
	HeadPipeline        *glPipeline `json:"head_pipeline"`
	DetailedMergeStatus string      `json:"detailed_merge_status"`
}

func parseGitLab(raw []byte) (*Request, error) {
	var mr glMR
	if err := json.Unmarshal(raw, &mr); err != nil {
		return nil, fmt.Errorf("could not read glab output: %w", err)
	}
	req := &Request{
		Number: mr.IID,
		Title:  mr.Title,
		URL:    mr.WebURL,
		State:  gitlabRequestState(mr.State),
		Draft:  mr.Draft || mr.WorkInProgress,
		Checks: "none",
		Review: gitlabReview(mr.DetailedMergeStatus),
	}
	if mr.HeadPipeline != nil {
		req.Checks = pipelineState(mr.HeadPipeline.Status)
		req.CheckRuns = []Check{{Name: "pipeline", State: req.Checks}}
	}
	return req, nil
}

// gitlabRequestState maps GitLab's request states onto the shared buckets.
func gitlabRequestState(state string) string {
	return map[string]string{"opened": "open", "merged": "merged", "closed": "closed", "locked": "closed"}[strings.ToLower(state)]
}

// gitlabReview folds GitLab's detailed_merge_status into the shared review
// buckets. The MR JSON glab prints carries no approval flag of its own, and
// "mergeable" is the only status that means the MR is approved and ready, so
// that is what counts as approved. An unreported status stays empty so the UI
// omits the row.
func gitlabReview(mergeStatus string) string {
	switch strings.ToLower(strings.TrimSpace(mergeStatus)) {
	case "mergeable":
		return "approved"
	case "not_approved":
		return "review_required"
	case "requested_changes", "discussions_not_resolved":
		return "changes_requested"
	}
	return ""
}

func checkRunState(status, conclusion string) string {
	if strings.ToUpper(status) != "COMPLETED" {
		return "pending"
	}
	switch strings.ToUpper(conclusion) {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return "passing"
	case "":
		return "pending"
	}
	return "failing"
}

func statusContextState(state string) string {
	switch strings.ToUpper(state) {
	case "SUCCESS":
		return "passing"
	case "PENDING", "EXPECTED", "":
		return "pending"
	}
	return "failing"
}

// pipelineState maps GitLab pipeline statuses onto the same three buckets.
func pipelineState(status string) string {
	switch status {
	case "success":
		return "passing"
	case "failed", "canceled":
		return "failing"
	case "", "skipped":
		return "none"
	}
	return "pending"
}

// aggregate folds per-check states into one: any failure wins, then pending.
func aggregate(states []string) string {
	if len(states) == 0 {
		return "none"
	}
	pending := false
	for _, s := range states {
		switch s {
		case "failing":
			return "failing"
		case "pending":
			pending = true
		}
	}
	if pending {
		return "pending"
	}
	return "passing"
}

// recentComments keeps the newest comments, oldest first, with bodies capped so
// one long review cannot bloat every poll.
func recentComments(in []Comment) []Comment {
	sort.SliceStable(in, func(i, j int) bool { return in[i].CreatedAt.Before(in[j].CreatedAt) })
	if len(in) > maxComments {
		in = in[len(in)-maxComments:]
	}
	for i := range in {
		in[i].Body = capRunes(in[i].Body, maxBodyRunes)
	}
	return in
}

func capRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func firstLine(text, fallback string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback
	}
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return text
}

func execRun(ctx context.Context, dir, name string, args ...string) ([]byte, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// Never let a CLI open a prompt: with no TTY it fails fast instead of hanging.
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, "timed out waiting for " + name, err
	}
	return out, stderr.String(), err
}
