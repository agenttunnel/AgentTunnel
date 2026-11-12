package scm

import (
	"context"
	"errors"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

const (
	ghList = "gh pr list --head feat/x --state all --json number,url --limit 100"
	ghRead = "gh pr view https://github.com/o/r/pull/42 --json number,title,url,state,isDraft,comments,commits,assignees,reviewRequests,reviews"
	glList = "glab mr list --source-branch feat/x --all --per-page 100 --output json"
	glRead = "glab mr view 7 --output json"
)

func gitlabRepo(extra map[string]reply) fakeCLI {
	replies := map[string]reply{
		gitBranch: {stdout: "feat/x\n"},
		gitOrigin: {stdout: "git@gitlab.com:o/r.git\n"},
	}
	for k, v := range extra {
		replies[k] = v
	}
	return fakeCLI{installed: map[string]bool{"glab": true}, replies: replies}
}

func TestDiscoverFindsEveryRequestOnTheBranch(t *testing.T) {
	list := `[{"number":43,"url":"https://github.com/o/r/pull/43"},{"number":42,"url":"https://github.com/o/r/pull/42"}]`
	fake := githubRepo(map[string]reply{ghList: {stdout: list}})

	got, refs := fake.client().Discover(context.Background(), "/repo")
	if got.Status != StatusReady || got.Branch != "feat/x" {
		t.Fatalf("listing = %+v, want ready on feat/x", got)
	}
	want := []contracts.PullRequestRef{
		{Number: 43, URL: "https://github.com/o/r/pull/43"},
		{Number: 42, URL: "https://github.com/o/r/pull/42"},
	}
	if len(refs) != len(want) || refs[0] != want[0] || refs[1] != want[1] {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}
}

func TestDetailsCountsCommitsCommentsAndPeople(t *testing.T) {
	detail := `{"number":42,"title":"Add PR view","url":"https://github.com/o/r/pull/42","state":"OPEN","isDraft":true,
	"comments":[{"author":{"login":"ann"},"body":"nit","url":"u","createdAt":"2026-11-09T10:00:00Z"}],
	"commits":[{"oid":"a"},{"oid":"b"},{"oid":"c"}],
	"assignees":[{"login":"theo"}],
	"reviewRequests":[{"login":"bob"},{"name":"core-team"}],
	"reviews":[{"author":{"login":"bob"},"state":"COMMENTED","body":"","url":"r","submittedAt":"2026-11-09T11:00:00Z"},
		{"author":{"login":"cy"},"state":"APPROVED","body":"","url":"r2","submittedAt":"2026-11-09T12:00:00Z"}]}`
	fake := githubRepo(map[string]reply{ghRead: {stdout: detail}})
	ref := contracts.PullRequestRef{Number: 42, URL: "https://github.com/o/r/pull/42"}

	list := fake.client().Details(context.Background(), "/repo", Listing{Status: StatusReady}, []contracts.PullRequestRef{ref})
	if len(list.Requests) != 1 {
		t.Fatalf("requests = %+v, want one", list.Requests)
	}
	pr := list.Requests[0]
	if pr.Error != "" || pr.State != "open" || !pr.Draft || pr.Title != "Add PR view" {
		t.Fatalf("pr = %+v", pr)
	}
	if pr.Comments != 1 || pr.Commits != 3 {
		t.Fatalf("comments/commits = %d/%d, want 1/3", pr.Comments, pr.Commits)
	}
	if len(pr.Assignees) != 1 || pr.Assignees[0] != "theo" {
		t.Fatalf("assignees = %v", pr.Assignees)
	}
	if want := []string{"bob", "core-team", "cy"}; len(pr.Reviewers) != len(want) ||
		pr.Reviewers[0] != want[0] || pr.Reviewers[1] != want[1] || pr.Reviewers[2] != want[2] {
		t.Fatalf("reviewers = %v, want %v (requested first, then reviewers, no repeats)", pr.Reviewers, want)
	}
}

// When the forge cannot be read, tracked requests keep their identity and show
// the reason, and no forge CLI is asked anything.
func TestDetailsKeepsTrackedRequestsWhenForgeIsMissing(t *testing.T) {
	fake := githubRepo(nil)
	fake.installed = map[string]bool{}
	c := fake.client()
	ref := contracts.PullRequestRef{Number: 42, URL: "https://github.com/o/r/pull/42"}

	list, refs := c.Discover(context.Background(), "/repo")
	if list.Status != StatusUnavailable || refs != nil {
		t.Fatalf("listing = %+v refs = %v, want unavailable with no refs", list, refs)
	}
	got := c.Details(context.Background(), "/repo", list, []contracts.PullRequestRef{ref})
	if len(got.Requests) != 1 || got.Requests[0].Number != 42 || got.Requests[0].URL != ref.URL ||
		got.Requests[0].Error != "gh is not installed" {
		t.Fatalf("requests = %+v, want the tracked ref with the reason", got.Requests)
	}
}

func TestDiscoverFindsEveryMergeRequestOnTheBranch(t *testing.T) {
	list := `[{"iid":9,"web_url":"https://gitlab.com/o/r/-/merge_requests/9"},
		{"iid":7,"web_url":"https://gitlab.com/o/r/-/merge_requests/7"}]`
	fake := gitlabRepo(map[string]reply{glList: {stdout: list}})

	got, refs := fake.client().Discover(context.Background(), "/repo")
	if got.Status != StatusReady || got.Provider != "gitlab" || got.Branch != "feat/x" {
		t.Fatalf("listing = %+v, want ready gitlab on feat/x", got)
	}
	want := []contracts.PullRequestRef{
		{Number: 9, URL: "https://gitlab.com/o/r/-/merge_requests/9"},
		{Number: 7, URL: "https://gitlab.com/o/r/-/merge_requests/7"},
	}
	if len(refs) != len(want) || refs[0] != want[0] || refs[1] != want[1] {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}
}

func TestDetailsMapsGitLabPipelineAndApprovals(t *testing.T) {
	// The real `glab mr view --output json` shape: no commits array, no
	// approved flag. Approval and changes-requested come from
	// detailed_merge_status alone.
	mr := `{"iid":7,"title":"Add MR view","web_url":"https://gitlab.com/o/r/-/merge_requests/7",
		"state":"opened","draft":false,"user_notes_count":2,
		"assignees":[{"username":"theo","name":"Theo"}],
		"reviewers":[{"username":"bob","name":"Bob"}],
		"head_pipeline":{"status":"failed"},"detailed_merge_status":"not_approved"}`
	fake := gitlabRepo(map[string]reply{glRead: {stdout: mr}})
	ref := contracts.PullRequestRef{Number: 7, URL: "https://gitlab.com/o/r/-/merge_requests/7"}

	list := fake.client().Details(context.Background(), "/repo",
		Listing{Status: StatusReady, Provider: "gitlab"}, []contracts.PullRequestRef{ref})
	if len(list.Requests) != 1 {
		t.Fatalf("requests = %+v, want one", list.Requests)
	}
	got := list.Requests[0]
	if got.Error != "" || got.State != "open" || got.Draft || got.Title != "Add MR view" {
		t.Fatalf("mr = %+v", got)
	}
	if got.Checks != "failing" || got.Review != "review_required" {
		t.Fatalf("checks/review = %q/%q, want failing/review_required", got.Checks, got.Review)
	}
	if got.Comments != 2 || got.Commits != 0 {
		t.Fatalf("comments/commits = %d/%d, want 2/0 (glab MR JSON carries no commits)", got.Comments, got.Commits)
	}
	if len(got.Assignees) != 1 || got.Assignees[0] != "theo" ||
		len(got.Reviewers) != 1 || got.Reviewers[0] != "bob" {
		t.Fatalf("people = %v / %v", got.Assignees, got.Reviewers)
	}
}

// An apparent approval flag is not part of the MR JSON; only merge status
// counts. This guards against the dead field creeping back in.
func TestDetailsIgnoresInventedApprovedFlag(t *testing.T) {
	mr := `{"iid":7,"web_url":"https://gitlab.com/o/r/-/merge_requests/7","state":"opened","approved":true}`
	fake := gitlabRepo(map[string]reply{glRead: {stdout: mr}})
	ref := contracts.PullRequestRef{Number: 7, URL: "https://gitlab.com/o/r/-/merge_requests/7"}

	list := fake.client().Details(context.Background(), "/repo",
		Listing{Status: StatusReady, Provider: "gitlab"}, []contracts.PullRequestRef{ref})
	if len(list.Requests) != 1 || list.Requests[0].Review != "" {
		t.Fatalf("requests = %+v, want one with no review", list.Requests)
	}
	if list.Requests[0].Commits != 0 {
		t.Fatalf("commits = %d, want 0", list.Requests[0].Commits)
	}
}

// A trimmed-down payload falls back to the tracked ref's identity.
func TestDetailsGitLabFallsBackToTrackedIdentity(t *testing.T) {
	mr := `{"state":"opened","detailed_merge_status":"mergeable"}`
	fake := gitlabRepo(map[string]reply{glRead: {stdout: mr}})
	ref := contracts.PullRequestRef{Number: 7, URL: "https://gitlab.com/o/r/-/merge_requests/7"}

	list := fake.client().Details(context.Background(), "/repo",
		Listing{Status: StatusReady, Provider: "gitlab"}, []contracts.PullRequestRef{ref})
	got := list.Requests[0]
	if got.Number != 7 || got.URL != ref.URL {
		t.Fatalf("identity = %d %q, want the tracked ref", got.Number, got.URL)
	}
	if got.Review != "approved" {
		t.Fatalf("review = %q, want approved", got.Review)
	}
}

func TestGitLabReviewBuckets(t *testing.T) {
	cases := map[string]struct {
		status string
		want   string
	}{
		"mergeable":                 {status: "mergeable", want: "approved"},
		"not approved":              {status: "not_approved", want: "review_required"},
		"changes requested":         {status: "requested_changes", want: "changes_requested"},
		"unresolved discussions":    {status: "discussions_not_resolved", want: "changes_requested"},
		"draft":                     {status: "draft_status", want: ""},
		"ci must pass":              {status: "ci_must_pass", want: ""},
		"mixed case and whitespace": {status: "  MergeAble ", want: "approved"},
		"unreported":                {status: "", want: ""},
	}
	for name, tc := range cases {
		if got := gitlabReview(tc.status); got != tc.want {
			t.Errorf("%s: gitlabReview(%q) = %q, want %q", name, tc.status, got, tc.want)
		}
	}
}

func TestDiscoverKeepsTrackedMergeRequestsWhenGLabIsMissing(t *testing.T) {
	fake := gitlabRepo(nil)
	fake.installed = map[string]bool{}
	c := fake.client()
	ref := contracts.PullRequestRef{Number: 7, URL: "https://gitlab.com/o/r/-/merge_requests/7"}

	list, refs := c.Discover(context.Background(), "/repo")
	if list.Status != StatusUnavailable || list.Provider != "gitlab" || refs != nil {
		t.Fatalf("listing = %+v refs = %v, want unavailable gitlab with no refs", list, refs)
	}
	got := c.Details(context.Background(), "/repo", list, []contracts.PullRequestRef{ref})
	if len(got.Requests) != 1 || got.Requests[0].Error != "glab is not installed" {
		t.Fatalf("requests = %+v, want the tracked ref with the reason", got.Requests)
	}
}

func TestDiscoverGitLabUnauthenticated(t *testing.T) {
	fake := gitlabRepo(map[string]reply{glList: {
		stderr: "To get started with GitLab CLI, please run:  glab auth login\n", err: errors.New("exit 4"),
	}})
	list, refs := fake.client().Discover(context.Background(), "/repo")
	if list.Status != StatusUnauthenticated || refs != nil || list.Message == "" {
		t.Fatalf("listing = %+v refs = %v, want unauthenticated with a message", list, refs)
	}
}
