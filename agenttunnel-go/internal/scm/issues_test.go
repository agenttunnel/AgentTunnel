package scm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

var errExit = errors.New("exit 1")

const (
	glIssueList = "glab issue list --assignee=@me --output json --per-page 50"
	glOrigin    = "git@gitlab.example.com:group/app.git"
)

func gitlabRepo(extra map[string]reply) fakeCLI {
	replies := map[string]reply{gitOrigin: {stdout: glOrigin + "\n"}}
	for k, v := range extra {
		replies[k] = v
	}
	return fakeCLI{installed: map[string]bool{"glab": true}, replies: replies}
}

// recording wraps a fake so a test can assert which commands ran.
func recordingCLI(f fakeCLI, calls *[]string) Client {
	c := f.client()
	run := c.Run
	c.Run = func(ctx context.Context, dir, name string, args ...string) ([]byte, string, error) {
		*calls = append(*calls, strings.Join(append([]string{name}, args...), " "))
		return run(ctx, dir, name, args...)
	}
	return c
}

func TestListIssuesReturnsOpenAssignedIssuesWithCappedDescriptions(t *testing.T) {
	long := strings.Repeat("é", maxIssueBodyRunes+500)
	raw := `[
	  {"iid":3,"title":"Login fails","description":"` + long + `","web_url":"https://gitlab.example.com/group/app/-/issues/3","state":"opened","updated_at":"2026-11-10T09:00:00Z"},
	  {"iid":4,"title":"Old bug","description":"done","web_url":"https://gitlab.example.com/group/app/-/issues/4","state":"closed","updated_at":"2026-10-01T09:00:00Z"},
	  {"iid":5,"title":"Slow page","description":"","web_url":"https://gitlab.example.com/group/app/-/issues/5","state":"opened","updated_at":"2026-11-11T09:00:00Z"}
	]`
	var calls []string
	c := recordingCLI(gitlabRepo(map[string]reply{glIssueList: {stdout: raw}}), &calls)

	list := c.ListIssues(context.Background(), "/repo")
	if list.Status != StatusReady || list.Provider != "gitlab" {
		t.Fatalf("status = %s provider = %s, want ready gitlab (%s)", list.Status, list.Provider, list.Message)
	}
	if len(list.Issues) != 2 {
		t.Fatalf("issues = %+v, want the two open ones", list.Issues)
	}
	first := list.Issues[0]
	if first.IID != 3 || first.Title != "Login fails" || first.WebURL == "" || first.UpdatedAt.IsZero() {
		t.Fatalf("first issue = %+v", first)
	}
	if got := utf8.RuneCountInString(first.Description); got != maxIssueBodyRunes+1 {
		t.Fatalf("description runes = %d, want the cap %d plus an ellipsis", got, maxIssueBodyRunes)
	}
	if list.Issues[1].IID != 5 {
		t.Fatalf("second issue = %d, want 5", list.Issues[1].IID)
	}
	if !contains(calls, glIssueList) {
		t.Fatalf("calls = %v, want the assigned-issues listing", calls)
	}
}

func TestListIssuesEmptyListingIsReadyNotNone(t *testing.T) {
	for _, out := range []string{"[]", ""} {
		c := gitlabRepo(map[string]reply{glIssueList: {stdout: out}}).client()
		list := c.ListIssues(context.Background(), "/repo")
		if list.Status != StatusReady || list.Issues == nil || len(list.Issues) != 0 {
			t.Fatalf("output %q: list = %+v, want ready with an empty, non-nil slice", out, list)
		}
	}
}

func TestListIssuesGitHubOriginNeverCallsGH(t *testing.T) {
	var calls []string
	f := fakeCLI{installed: map[string]bool{"gh": true, "glab": true}, replies: map[string]reply{
		gitOrigin: {stdout: "git@github.com:agenttunnel/AgentTunnel.git\n"},
	}}
	list := recordingCLI(f, &calls).ListIssues(context.Background(), "/repo")
	if list.Status != StatusUnsupported {
		t.Fatalf("status = %s, want unsupported", list.Status)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "gh ") || strings.HasPrefix(c, "glab ") {
			t.Fatalf("ran %q for a GitHub origin", c)
		}
	}
}

func TestListIssuesWithoutOriginIsUnsupported(t *testing.T) {
	f := fakeCLI{installed: map[string]bool{"glab": true}, replies: map[string]reply{}}
	if list := f.client().ListIssues(context.Background(), "/not-a-repo"); list.Status != StatusUnsupported {
		t.Fatalf("status = %s, want unsupported", list.Status)
	}
}

func TestListIssuesWithoutGlabIsUnavailable(t *testing.T) {
	f := gitlabRepo(nil)
	f.installed = map[string]bool{}
	list := f.client().ListIssues(context.Background(), "/repo")
	if list.Status != StatusUnavailable || list.Message != "glab is not installed" {
		t.Fatalf("list = %+v, want unavailable", list)
	}
}

func TestListIssuesLoggedOutIsUnauthenticated(t *testing.T) {
	f := gitlabRepo(map[string]reply{
		glIssueList: {stderr: "You are not logged in to any hosts. Run glab auth login to authenticate.", err: errExit},
	})
	if list := f.client().ListIssues(context.Background(), "/repo"); list.Status != StatusUnauthenticated {
		t.Fatalf("status = %s, want unauthenticated", list.Status)
	}
}

func TestListIssuesCLIFailureIsError(t *testing.T) {
	f := gitlabRepo(map[string]reply{glIssueList: {stderr: "boom\nstack trace", err: errExit}})
	list := f.client().ListIssues(context.Background(), "/repo")
	if list.Status != StatusError || list.Message != "boom" {
		t.Fatalf("list = %+v, want an error carrying the first stderr line", list)
	}
}

func TestListIssuesMalformedOutputIsError(t *testing.T) {
	f := gitlabRepo(map[string]reply{glIssueList: {stdout: "not json"}})
	if list := f.client().ListIssues(context.Background(), "/repo"); list.Status != StatusError {
		t.Fatalf("status = %s, want error", list.Status)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// A dropped issue becomes the draft's spec, so a body under the draft limit
// must reach the draft unchanged.
func TestListIssuesKeepsBodyWithinDraftLimitIntact(t *testing.T) {
	body := strings.Repeat("step ", 1000) + "last line"
	raw := `[{"iid":7,"title":"Long","description":"` + body + `","web_url":"https://gitlab.example.com/group/app/-/issues/7","state":"opened","updated_at":"2026-11-10T09:00:00Z"}]`
	var calls []string
	c := recordingCLI(gitlabRepo(map[string]reply{glIssueList: {stdout: raw}}), &calls)

	list := c.ListIssues(context.Background(), "/repo")
	if len(list.Issues) != 1 || list.Issues[0].Description != body {
		t.Fatalf("description was altered: got %d runes", utf8.RuneCountInString(list.Issues[0].Description))
	}
}
