package scm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeCLI scripts git and the forge CLIs: each key is "name arg0 arg1...", and
// a missing key fails the way an unknown command would.
type fakeCLI struct {
	installed map[string]bool
	replies   map[string]reply
}

type reply struct {
	stdout string
	stderr string
	err    error
}

func (f fakeCLI) client() Client {
	return Client{
		LookPath: func(name string) (string, error) {
			if f.installed[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		Run: func(_ context.Context, _, name string, args ...string) ([]byte, string, error) {
			key := strings.Join(append([]string{name}, args...), " ")
			r, ok := f.replies[key]
			if !ok {
				return nil, "unscripted: " + key, errors.New("exit 1")
			}
			return []byte(r.stdout), r.stderr, r.err
		},
	}
}

const (
	gitBranch = "git rev-parse --abbrev-ref HEAD"
	gitOrigin = "git remote get-url origin"
	ghView    = "gh pr view --json number,title,url,state,isDraft,reviewDecision,statusCheckRollup,comments,reviews"
	glView    = "glab mr view --output json"
)

func githubRepo(extra map[string]reply) fakeCLI {
	replies := map[string]reply{
		gitBranch: {stdout: "feat/x\n"},
		gitOrigin: {stdout: "git@github.com:agenttunnel/AgentTunnel.git\n"},
	}
	for k, v := range extra {
		replies[k] = v
	}
	return fakeCLI{installed: map[string]bool{"gh": true}, replies: replies}
}

func TestFetchGitHubPullRequestSummarizesChecksReviewAndComments(t *testing.T) {
	pr := `{"number":42,"title":"Add PR panel","url":"https://github.com/o/r/pull/42","state":"OPEN",
	"isDraft":false,"reviewDecision":"CHANGES_REQUESTED",
	"statusCheckRollup":[
		{"__typename":"CheckRun","name":"test","status":"COMPLETED","conclusion":"SUCCESS"},
		{"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"FAILURE"},
		{"__typename":"CheckRun","name":"build","status":"IN_PROGRESS","conclusion":""},
		{"__typename":"StatusContext","context":"deploy","state":"SUCCESS"}],
	"comments":[{"author":{"login":"ann"},"body":"nit: rename","url":"https://x/c1","createdAt":"2026-11-09T10:00:00Z"}],
	"reviews":[{"author":{"login":"bob"},"body":"please fix lint","state":"CHANGES_REQUESTED","url":"https://x/r1","submittedAt":"2026-11-09T11:00:00Z"},
		{"author":{"login":"cy"},"body":"","state":"APPROVED","url":"https://x/r2","submittedAt":"2026-11-09T12:00:00Z"}]}`

	snap := githubRepo(map[string]reply{ghView: {stdout: pr}}).client().Fetch(context.Background(), "/repo")

	if snap.Status != StatusReady || snap.Provider != "github" || snap.Branch != "feat/x" {
		t.Fatalf("snapshot = %+v, want ready github request on feat/x", snap)
	}
	req := snap.Request
	if req == nil || req.Number != 42 || req.State != "open" || req.Review != "changes_requested" {
		t.Fatalf("request = %+v", req)
	}
	if req.Checks != "failing" {
		t.Fatalf("checks = %q, want failing (one check failed)", req.Checks)
	}
	wantChecks := []Check{
		{Name: "test", State: "passing"},
		{Name: "lint", State: "failing"},
		{Name: "build", State: "pending"},
		{Name: "deploy", State: "passing"},
	}
	if len(req.CheckRuns) != len(wantChecks) {
		t.Fatalf("check runs = %+v", req.CheckRuns)
	}
	for i, want := range wantChecks {
		if req.CheckRuns[i] != want {
			t.Fatalf("check %d = %+v, want %+v", i, req.CheckRuns[i], want)
		}
	}
	// The approval with no body is dropped; the other two are ordered by time.
	if len(req.Comments) != 2 || req.Comments[0].Author != "ann" || req.Comments[1].Kind != "review" ||
		req.Comments[1].ReviewState != "changes_requested" {
		t.Fatalf("comments = %+v", req.Comments)
	}
}

func TestFetchGitHubPendingChecksWithoutFailureStayPending(t *testing.T) {
	pr := `{"number":1,"state":"OPEN","reviewDecision":"","statusCheckRollup":[
		{"__typename":"CheckRun","name":"test","status":"COMPLETED","conclusion":"SUCCESS"},
		{"__typename":"StatusContext","context":"ci","state":"PENDING"}]}`
	req := githubRepo(map[string]reply{ghView: {stdout: pr}}).client().Fetch(context.Background(), "/r").Request
	if req.Checks != "pending" || req.Review != "none" {
		t.Fatalf("checks=%q review=%q, want pending/none", req.Checks, req.Review)
	}
}

func TestFetchDegradesWhenCLIMissingOrLoggedOut(t *testing.T) {
	cases := []struct {
		name string
		cli  fakeCLI
		want Status
	}{
		{
			name: "gh not installed",
			cli: fakeCLI{installed: map[string]bool{}, replies: map[string]reply{
				gitBranch: {stdout: "feat/x"}, gitOrigin: {stdout: "https://github.com/o/r.git"},
			}},
			want: StatusUnavailable,
		},
		{
			name: "gh not logged in",
			cli: githubRepo(map[string]reply{ghView: {
				stderr: "To get started with GitHub CLI, please run:  gh auth login\n", err: errors.New("exit 4"),
			}}),
			want: StatusUnauthenticated,
		},
		{
			name: "no pull request for the branch",
			cli: githubRepo(map[string]reply{ghView: {
				stderr: `no pull requests found for branch "feat/x"`, err: errors.New("exit 1"),
			}}),
			want: StatusNone,
		},
		{
			name: "unknown forge",
			cli: fakeCLI{installed: map[string]bool{"gh": true}, replies: map[string]reply{
				gitBranch: {stdout: "feat/x"}, gitOrigin: {stdout: "https://example.com/o/r.git"},
			}},
			want: StatusUnsupported,
		},
		{
			name: "detached head",
			cli: fakeCLI{installed: map[string]bool{"gh": true}, replies: map[string]reply{
				gitBranch: {stdout: "HEAD"},
			}},
			want: StatusNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := tc.cli.client().Fetch(context.Background(), "/repo")
			if snap.Status != tc.want {
				t.Fatalf("status = %q (%s), want %q", snap.Status, snap.Message, tc.want)
			}
			if snap.Request != nil {
				t.Fatalf("degraded snapshot carries a request: %+v", snap.Request)
			}
			if snap.Message == "" {
				t.Fatalf("degraded snapshot needs a message the UI can show")
			}
		})
	}
}

func TestFetchReportsUnknownCLIFailureAsError(t *testing.T) {
	cli := githubRepo(map[string]reply{ghView: {stderr: "HTTP 502: bad gateway\nmore", err: errors.New("exit 1")}})
	snap := cli.client().Fetch(context.Background(), "/repo")
	if snap.Status != StatusError || snap.Message != "HTTP 502: bad gateway" {
		t.Fatalf("snapshot = %+v, want error with first stderr line", snap)
	}
}

func TestFetchGitLabMergeRequestMapsPipelineAndDraft(t *testing.T) {
	mr := `{"iid":7,"title":"Wip thing","web_url":"https://gitlab.com/o/r/-/merge_requests/7",
		"state":"opened","draft":true,"head_pipeline":{"status":"failed"}}`
	cli := fakeCLI{
		installed: map[string]bool{"glab": true},
		replies: map[string]reply{
			gitBranch: {stdout: "feat/x"},
			gitOrigin: {stdout: "git@gitlab.com:o/r.git"},
			glView:    {stdout: mr},
		},
	}
	snap := cli.client().Fetch(context.Background(), "/repo")
	if snap.Status != StatusReady || snap.Provider != "gitlab" || snap.Request == nil {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.Request.State != "open" || !snap.Request.Draft || snap.Request.Checks != "failing" {
		t.Fatalf("request = %+v", snap.Request)
	}
	if snap.Request.Comments != nil {
		t.Fatalf("gitlab comments are not wired yet; got %+v", snap.Request.Comments)
	}
}

func TestFetchGitLabNoOpenMergeRequest(t *testing.T) {
	cli := fakeCLI{
		installed: map[string]bool{"glab": true},
		replies: map[string]reply{
			gitBranch: {stdout: "feat/x"},
			gitOrigin: {stdout: "git@gitlab.com:o/r.git"},
			glView:    {stderr: `no open merge request available for "feat/x"`, err: errors.New("exit 1")},
		},
	}
	if snap := cli.client().Fetch(context.Background(), "/repo"); snap.Status != StatusNone {
		t.Fatalf("status = %q, want none", snap.Status)
	}
}

func TestProviderForRemoteShapes(t *testing.T) {
	cases := map[string]string{
		"git@github.com:o/r.git":           "github",
		"https://github.com/o/r.git":       "github",
		"https://user@gitlab.com/o/r.git":  "gitlab",
		"ssh://git@gitlab.example.com/o/r": "gitlab",
		"https://example.com/o/r.git":      "",
	}
	for remote, want := range cases {
		if got := providerFor(remote); got != want {
			t.Errorf("providerFor(%q) = %q, want %q", remote, got, want)
		}
	}
}
