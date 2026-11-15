package orchestrator

import (
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// The subthread tool is offered to the lead only when the thread may start one.
func TestOrchestratorPromptOffersSubthreadOnlyWhenAllowed(t *testing.T) {
	role := contracts.Role{SystemPrompt: "SYS"}
	with := BuildOrchestratorPrompt(OrchestratorPrompt{Role: role, UserMessage: "x", Subthreads: true})
	without := BuildOrchestratorPrompt(OrchestratorPrompt{Role: role, UserMessage: "x"})

	if !strings.Contains(with, `tool subthread --task`) {
		t.Fatalf("an allowed thread should document the subthread tool:\n%s", with)
	}
	if strings.Contains(without, "subthread") {
		t.Fatalf("a thread that cannot start subthreads must not mention them:\n%s", without)
	}
}

// Each thread's notebook is its own: a subthread running beside its parent must
// not read the parent's notes, or write its own into them.
func TestNotebooksArePerThread(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)

	r.notebookFor("parent").Append("notes", "parent finding", "agent-orchestrator")
	if got := r.notebookFor("child").Get("notes"); len(got) != 0 {
		t.Fatalf("child saw the parent's notes: %+v", got)
	}
	if got := r.notebookFor("parent").Get("notes"); len(got) != 1 {
		t.Fatalf("parent lost its own note: %+v", got)
	}
	if r.notebookFor("parent") != r.notebookFor("parent") {
		t.Fatal("a thread must keep one notebook across calls")
	}
}

// The lead is offered the subthread tool only while the thread has a free slot:
// a child thread, or a full set of subthreads, gets no offer.
func TestSubthreadsOfferedWhileASlotIsFree(t *testing.T) {
	full := make([]contracts.ThreadID, contracts.MaxSubthreads)
	for i := range full {
		full[i] = contracts.ThreadID("sub-" + string(rune('a'+i)))
	}
	cases := []struct {
		name  string
		state domain.ThreadState
		peer  bool
		want  bool
	}{
		{"top-level with a free slot", domain.ThreadState{Subthreads: full[:1]}, true, true},
		{"peer mailbox off", domain.ThreadState{}, false, false},
		{"a subthread cannot start its own", domain.ThreadState{ParentThreadID: "parent"}, true, false},
		{"all slots used", domain.ThreadState{Subthreads: full}, true, false},
	}
	for _, c := range cases {
		if got := subthreadsOffered(&c.state, c.peer); got != c.want {
			t.Errorf("%s: subthreadsOffered = %v, want %v", c.name, got, c.want)
		}
	}
}
