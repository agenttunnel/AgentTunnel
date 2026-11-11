package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// reviewState is a thread with a reviewer, an executor, and three review threads:
// one open and awaiting an answer, one open with no answer yet, one already
// answered by the reviewer, and one resolved.
func reviewState() *domain.ThreadState {
	return &domain.ThreadState{
		ID: "t1",
		Agents: map[contracts.AgentID]contracts.Agent{
			"agent-rev":  {ID: "agent-rev", ParentAgentID: "agent-orchestrator", Name: "rev", Role: contracts.RoleReviewer},
			"agent-exec": {ID: "agent-exec", ParentAgentID: "agent-orchestrator", Name: "exec", Role: contracts.RoleExecutor},
		},
		ReviewThreads: []domain.ReviewThread{
			{ID: "rt-answered", File: "a.go", Line: 3, Body: "guard nil", AuthorID: "agent-rev",
				Status: contracts.ReviewStatusOpen, Replies: []domain.ReviewReply{{AuthorID: "agent-exec", Body: "added the guard"}}},
			{ID: "rt-silent", File: "b.go", Line: 9, Body: "name this", AuthorID: "agent-rev",
				Status: contracts.ReviewStatusOpen},
			{ID: "rt-closed", File: "c.go", Line: 1, Body: "old note", AuthorID: "agent-rev",
				Status: contracts.ReviewStatusOpen, Replies: []domain.ReviewReply{
					{AuthorID: "agent-exec", Body: "done"}, {AuthorID: "agent-rev", Body: "thanks"}}},
			{ID: "rt-resolved", File: "d.go", Line: 2, Body: "fixed earlier", AuthorID: "agent-rev",
				Status: contracts.ReviewStatusResolved, Replies: []domain.ReviewReply{{AuthorID: "agent-exec", Body: "ok"}}},
		},
	}
}

func TestOpenReviewBlockListsOnlyOpenThreads(t *testing.T) {
	block := renderOpenReviews(reviewState())
	for _, want := range []string{"## Open review threads", "rt-answered", "rt-silent", "rt-closed", "review-reply --thread <id>"} {
		if !strings.Contains(block, want) {
			t.Fatalf("open review block lacks %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "rt-resolved") {
		t.Fatalf("a resolved thread is listed as open:\n%s", block)
	}
	if got := renderOpenReviews(&domain.ThreadState{}); got != "" {
		t.Fatalf("no threads should render no block, got %q", got)
	}
}

func TestSubagentPromptCarriesReviewBlock(t *testing.T) {
	block := renderOpenReviews(reviewState())
	prompt := BuildSubagentPrompt(contracts.Role{SystemPrompt: "role"}, "fix it", "", "", block, false, nil)
	if !strings.Contains(prompt, "## Open review threads") || !strings.Contains(prompt, "rt-silent") {
		t.Fatalf("subagent prompt lacks the review block:\n%s", prompt)
	}
	if plain := BuildSubagentPrompt(contracts.Role{SystemPrompt: "role"}, "fix it", "", "", "", false, nil); strings.Contains(plain, "Open review threads") {
		t.Fatalf("prompt without threads shows the block:\n%s", plain)
	}
}

func TestReviewerRoleIsToldToPostComments(t *testing.T) {
	for _, role := range contracts.BuiltinRoles() {
		if role.ID != contracts.RoleReviewer {
			continue
		}
		if !strings.Contains(role.SystemPrompt, "tool review-comment") {
			t.Fatalf("reviewer prompt does not name review-comment: %q", role.SystemPrompt)
		}
		return
	}
	t.Fatal("no reviewer role")
}

func TestReviewRecheckWaitsForAnAnswer(t *testing.T) {
	task := reviewRecheckTask(reviewState(), "agent-rev")
	if !strings.Contains(task, "This is a review re-check.") {
		t.Fatalf("re-check task lacks its header:\n%s", task)
	}
	// Only the thread an executor answered and the reviewer has not weighed.
	if !strings.Contains(task, "Thread rt-answered") || !strings.Contains(task, "added the guard") {
		t.Fatalf("re-check task misses the answered thread:\n%s", task)
	}
	for _, skip := range []string{"rt-silent", "rt-closed", "rt-resolved"} {
		if strings.Contains(task, skip) {
			t.Fatalf("re-check task includes %s:\n%s", skip, task)
		}
	}
	if got := reviewRecheckTask(reviewState(), "agent-exec"); got != "" {
		t.Fatalf("a non-reviewer has nothing to re-check, got %q", got)
	}
}

// reviewRoundScript plays the team for the review tests. The lead dispatches a
// reviewer and an executor that depends on it. The reviewer opens one thread on
// a.go, and on a re-check resolves it. The executor answers the thread it is
// shown. Each reviewer and executor prompt is appended to $REVIEW_LOG, named by
// thread and role, so a test can read what each agent was told.
const reviewRoundScript = `case "$AT_ROLE" in
orchestrator)
  if [ "$AT_PASS" = 0 ]; then
    printf '{"type":"text","text":"tool dispatch --role reviewer --name rev --task \\"review the change\\"\\n"}\n'
    printf '{"type":"text","text":"tool dispatch --role executor --name exec --depends-on rev --task \\"fix what the review found\\"\\n"}\n'
  fi ;;
reviewer)
  printf '%s\n' "$1" >> "$REVIEW_LOG/$AT_THREAD_ID-reviewer.log"
  case "$1" in
  *"This is a review re-check."*)
    id=$(printf '%s\n' "$1" | sed -n 's/^Thread \(review-[^ ]*\) .*/\1/p' | head -1)
    printf '{"type":"text","text":"tool review-resolve --thread %s --text \\"resolved after the fix\\"\\n"}\n' "$id" ;;
  *)
    printf '{"type":"text","text":"tool review-comment --file a.go --line 3 --text finding-%s\\n"}\n' "$AT_THREAD_ID" ;;
  esac ;;
executor)
  printf '%s\n' "$1" >> "$REVIEW_LOG/$AT_THREAD_ID-executor.log"
  case "$1" in *"## Open review threads"*)
    id=$(printf '%s\n' "$1" | sed -n 's/^- \(review-[^ ]*\) .*/\1/p' | head -1)
    printf '{"type":"text","text":"tool review-reply --thread %s --text \\"fixed the finding\\"\\n"}\n' "$id" ;;
  esac ;;
esac
`

// dispatchesNamed returns the agent ids of every dispatch of a named agent, in
// event order, read from the thread's durable log.
func dispatchesNamed(t *testing.T, eng *domain.Engine, threadID contracts.ThreadID, name string) []contracts.TaskDispatched {
	t.Helper()
	events, err := eng.Events(context.Background(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	var out []contracts.TaskDispatched
	for _, ev := range events {
		if d, ok := ev.Payload.(contracts.TaskDispatched); ok && d.Name == name {
			out = append(out, d)
		}
	}
	return out
}

func TestReviewerIsRedispatchedToWeighAnswers(t *testing.T) {
	r, eng, ctx := newPromptHarness(t, reviewRoundScript)
	logDir := t.TempDir()
	t.Setenv("REVIEW_LOG", logDir)

	if err := New(r).RunTurn(ctx, "t1", "review and fix"); err != nil {
		t.Fatalf("turn: %v", err)
	}

	state, _ := eng.State(ctx, "t1")
	if len(state.ReviewThreads) != 1 {
		t.Fatalf("threads = %+v", state.ReviewThreads)
	}
	th := state.ReviewThreads[0]
	// The executor's answer, then the reviewer's resolve note, which is recorded
	// as a reply by the same author.
	if th.Status != contracts.ReviewStatusResolved || len(th.Replies) != 2 || th.Replies[0].Body != "fixed the finding" {
		t.Fatalf("thread after the turn = %+v", th)
	}

	// The reviewer was dispatched twice, both times under the same agent id and
	// parent: the second dispatch is the re-check.
	revs := dispatchesNamed(t, eng, "t1", "rev")
	if len(revs) != 2 {
		t.Fatalf("reviewer dispatches = %d, want 2", len(revs))
	}
	if revs[0].AgentID != revs[1].AgentID || revs[0].ParentAgentID != revs[1].ParentAgentID {
		t.Fatalf("re-dispatch changed identity: %+v then %+v", revs[0], revs[1])
	}
	if !strings.Contains(revs[1].Task, "fixed the finding") {
		t.Fatalf("re-check task lacks the executor's reply:\n%s", revs[1].Task)
	}
	if st := state.Agents[revs[0].AgentID]; st.Status != contracts.AgentSucceeded {
		t.Fatalf("reviewer status = %q, want succeeded", st.Status)
	}

	// The executor was shown the thread the reviewer opened.
	execPrompt := readLog(t, filepath.Join(logDir, "t1-executor.log"))
	if !strings.Contains(execPrompt, "## Open review threads") || !strings.Contains(execPrompt, "finding-t1") {
		t.Fatalf("executor prompt lacks the open thread:\n%s", execPrompt)
	}
}

func TestConcurrentTurnsKeepReviewThreadsApart(t *testing.T) {
	r, eng, ctx := newPromptHarness(t, reviewRoundScript)
	logDir := t.TempDir()
	t.Setenv("REVIEW_LOG", logDir)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2"}); err != nil {
		t.Fatal(err)
	}

	o := New(r)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []contracts.ThreadID{"t1", "t2"} {
		wg.Add(1)
		go func(i int, id contracts.ThreadID) {
			defer wg.Done()
			errs[i] = o.RunTurn(ctx, id, "review and fix "+string(id))
		}(i, id)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("turn %d: %v", i+1, err)
		}
	}

	for _, id := range []string{"t1", "t2"} {
		other := "t2"
		if id == "t2" {
			other = "t1"
		}
		prompt := readLog(t, filepath.Join(logDir, id+"-executor.log"))
		if !strings.Contains(prompt, "finding-"+id) {
			t.Fatalf("%s executor was not shown its own thread:\n%s", id, prompt)
		}
		if strings.Contains(prompt, "finding-"+other) {
			t.Fatalf("%s executor was shown %s's review thread:\n%s", id, other, prompt)
		}
		state, _ := eng.State(ctx, contracts.ThreadID(id))
		if len(state.ReviewThreads) != 1 || state.ReviewThreads[0].Body != "finding-"+id {
			t.Fatalf("%s review threads = %+v", id, state.ReviewThreads)
		}
	}
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// A harness that runs a review command and also prints it must leave one
// thread, one reply: the printed echo is skipped, a fresh call still lands.
func TestPrintedReviewEchoIsNotAppliedTwice(t *testing.T) {
	r, eng, ctx := newPromptHarness(t, "true")
	if err := r.Dispatch(ctx, "t1", []contracts.DispatchTask{
		{AgentID: "agent-rev", Name: "rev", Role: contracts.RoleReviewer, Task: "review"},
		{AgentID: "agent-exec", Name: "exec", Role: contracts.RoleExecutor, Task: "fix"},
	}); err != nil {
		t.Fatal(err)
	}
	comment := toolbox.Call{Kind: toolbox.KindReviewComment, File: "a.go", Line: 3, Side: "new", Text: "nil map"}
	r.applyPrintedReview(ctx, "t1", "agent-rev", comment)
	r.applyPrintedReview(ctx, "t1", "agent-rev", comment)

	state, _ := eng.State(ctx, "t1")
	if len(state.ReviewThreads) != 1 {
		t.Fatalf("echoed comment opened %d threads, want 1", len(state.ReviewThreads))
	}
	id := state.ReviewThreads[0].ID
	reply := toolbox.Call{Kind: toolbox.KindReviewReply, Thread: id, Text: "guarded"}
	r.applyPrintedReview(ctx, "t1", "agent-exec", reply)
	r.applyPrintedReview(ctx, "t1", "agent-exec", reply)
	state, _ = eng.State(ctx, "t1")
	if n := len(state.ReviewThreads[0].Replies); n != 1 {
		t.Fatalf("echoed reply recorded %d times, want 1", n)
	}
}
