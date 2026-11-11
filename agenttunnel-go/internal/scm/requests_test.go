package scm

import (
	"context"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

const (
	ghList = "gh pr list --head feat/x --state all --json number,url --limit 100"
	ghRead = "gh pr view https://github.com/o/r/pull/42 --json number,title,url,state,isDraft,comments,commits,assignees,reviewRequests,reviews"
)

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
