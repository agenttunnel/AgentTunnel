package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/scm"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// draftFixture is a dispatcher over a non-git project, so draft tests run
// without git init.
type draftFixture struct {
	t    *testing.T
	ctx  context.Context
	eng  *domain.Engine
	disp *Dispatcher
}

func newDraftFixture(t *testing.T) *draftFixture {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)
	st := settings.New(t.TempDir())
	st.AddProject(settings.Project{ID: "p1", Name: "plain", Path: t.TempDir()})
	disp := NewDispatcher(DispatcherDeps{Engine: eng, Logs: logstore.New(t.TempDir()), Settings: st})
	return &draftFixture{t: t, ctx: ctx, eng: eng, disp: disp}
}

func (f *draftFixture) call(method string, params any) (any, error) {
	f.t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.disp.Handle(f.ctx, Request{Method: method, Params: raw})
}

func (f *draftFixture) mustCall(method string, params any) any {
	f.t.Helper()
	out, err := f.call(method, params)
	if err != nil {
		f.t.Fatalf("%s: %v", method, err)
	}
	return out
}

func (f *draftFixture) createDraft(title, description string, issue *contracts.IssueRef) contracts.ThreadID {
	f.t.Helper()
	out := f.mustCall("thread.create", map[string]any{
		"title": title, "projectId": "p1", "draft": true, "description": description, "issue": issue,
	})
	return out.(map[string]any)["threadId"].(contracts.ThreadID)
}

func TestDraftCreateStoresDescriptionAndIssue(t *testing.T) {
	f := newDraftFixture(t)
	issue := &contracts.IssueRef{Provider: "gitlab", IID: 4, Title: "Login fails", URL: "https://gitlab.example/g/p/-/issues/4"}
	id := f.createDraft("Login", "the form resets", issue)

	st, err := f.eng.State(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Draft || st.Description != "the form resets" || st.Issue == nil || st.Issue.IID != 4 {
		t.Fatalf("draft state = %+v", st)
	}
}

// Describe edits only drafts; a started thread keeps its description fixed.
func TestThreadDescribeRPCRefusesStartedThreads(t *testing.T) {
	f := newDraftFixture(t)
	id := f.createDraft("Login", "old", nil)
	f.mustCall("thread.describe", map[string]any{"threadId": id, "description": "new"})
	if st, _ := f.eng.State(f.ctx, id); st.Description != "new" {
		t.Fatalf("description = %q, want %q", st.Description, "new")
	}

	// A thread created without draft is not a draft: describing it is refused.
	out := f.mustCall("thread.create", map[string]any{"title": "run", "projectId": "p1"})
	runID := out.(map[string]any)["threadId"].(contracts.ThreadID)
	if _, err := f.call("thread.describe", map[string]any{"threadId": runID, "description": "x"}); err == nil {
		t.Fatal("describe on a non-draft thread succeeded")
	}
}

func TestThreadListCarriesDraftAndIssue(t *testing.T) {
	f := newDraftFixture(t)
	issue := &contracts.IssueRef{Provider: "gitlab", IID: 5, Title: "Crash", URL: "https://gitlab.example/g/p/-/issues/5"}
	f.createDraft("Crash", "steps", issue)

	raw, err := json.Marshal(f.mustCall("thread.list", nil))
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Draft bool                `json:"draft"`
		Issue *contracts.IssueRef `json:"issue"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Draft || rows[0].Issue == nil || rows[0].Issue.URL != issue.URL {
		t.Fatalf("thread.list = %s, want the draft with its issue", raw)
	}
}

// One issue starts at most one live thread. Deleting that thread releases it.
func TestDuplicateIssueIsRefusedUntilThreadDeleted(t *testing.T) {
	f := newDraftFixture(t)
	issue := &contracts.IssueRef{Provider: "gitlab", IID: 6, Title: "Dup", URL: "https://gitlab.example/g/p/-/issues/6"}
	first := f.createDraft("Dup", "", issue)

	if _, err := f.call("thread.create", map[string]any{"title": "Dup again", "projectId": "p1", "draft": true, "issue": issue}); err == nil {
		t.Fatal("second thread for the same issue was created")
	} else if !strings.Contains(err.Error(), "already has thread") {
		t.Fatalf("duplicate error = %v", err)
	}

	if err := f.eng.Dispatch(f.ctx, contracts.Command{ID: "del", Type: contracts.CmdThreadDelete, ThreadID: first}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	f.createDraft("Dup retry", "", issue)
}

func TestIssuesListNonGitProjectReportsStatus(t *testing.T) {
	f := newDraftFixture(t)
	out, err := f.call("issues.list", map[string]any{"projectId": "p1"})
	if err != nil {
		t.Fatalf("issues.list: %v", err)
	}
	list, ok := out.(scm.IssueList)
	if !ok {
		t.Fatalf("issues.list = %T, want scm.IssueList", out)
	}
	if list.Status != scm.StatusUnsupported {
		t.Fatalf("status = %s, want unsupported", list.Status)
	}
	assertIssuesIsArray(t, out)

	out, err = f.call("issues.list", map[string]any{"projectId": "missing"})
	if err != nil {
		t.Fatalf("issues.list on a missing project: %v", err)
	}
	if list := out.(scm.IssueList); list.Status != scm.StatusNone {
		t.Fatalf("missing project status = %s, want none", list.Status)
	}
	assertIssuesIsArray(t, out)
}

// The board calls issues.filter on the reply, so "issues" must be a JSON array
// on the wire, not null. The Go value alone cannot show that.
func assertIssuesIsArray(t *testing.T, out any) {
	t.Helper()
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		Issues []json.RawMessage `json:"issues"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if wire.Issues == nil {
		t.Fatalf("issues encoded as null: %s", raw)
	}
}

// Needs git init, so it runs under the lead's full suite. A draft's worktree is
// cut by its first turn, never at creation.
func TestDraftCreateCutsNoWorktree(t *testing.T) {
	f := newWorktreeFixture(t)
	out := f.mustCall("thread.create", map[string]any{
		"title": "draft", "projectId": f.projectID, "worktree": true, "draft": true, "description": "later",
	})
	id := out.(map[string]any)["threadId"].(contracts.ThreadID)
	st := f.state(id)
	if !st.Draft || st.Worktree != nil {
		t.Fatalf("draft state = draft:%v worktree:%+v, want a draft with no worktree", st.Draft, st.Worktree)
	}
}

func TestDraftPromptUsesTitleAndDescription(t *testing.T) {
	cases := []struct {
		name        string
		title, desc string
		want        string
	}{
		{"title and description", "Login", "the form resets", "Login\n\nthe form resets"},
		{"description only", contracts.DefaultThreadTitle, "the form resets", "the form resets"},
		{"title only", "Login", "  ", "Login"},
		{"neither", contracts.DefaultThreadTitle, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := domain.NewThreadState("t1")
			st.Title, st.Description = c.title, c.desc
			if got := draftPrompt(st); got != c.want {
				t.Fatalf("draftPrompt = %q, want %q", got, c.want)
			}
		})
	}
}

// A long issue body must reach the draft whole: the first turn runs it as the
// agent's spec, so a truncated description would start work on a cut spec.
func TestDraftKeepsLongDescriptionIntact(t *testing.T) {
	f := newDraftFixture(t)
	body := strings.Repeat("requirement line\n", 1000)
	id := f.createDraft("Long spec", body, nil)

	st, err := f.eng.State(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.Description != body {
		t.Fatalf("draft description was altered: %d bytes, want %d", len(st.Description), len(body))
	}
}
