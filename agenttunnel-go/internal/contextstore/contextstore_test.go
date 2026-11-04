package contextstore

import (
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestNotebookSetReplacesAndAppendAccumulates(t *testing.T) {
	n := NewNotebook()
	n.Set("decisions", "use Go", "orchestrator")
	n.Set("decisions", "use Go 1.26", "orchestrator")
	if got := n.Get("decisions"); len(got) != 1 || got[0].Value != "use Go 1.26" {
		t.Fatalf("set did not replace: %+v", got)
	}
	n.Append("findings", "x is slow", "executor")
	n.Append("findings", "y is fine", "tester")
	if got := n.Get("findings"); len(got) != 2 {
		t.Fatalf("append did not accumulate: %+v", got)
	}
}

func TestNotebookRenderIncludesAuthors(t *testing.T) {
	n := NewNotebook()
	n.Set("decisions", "use Go", "orchestrator")
	n.Append("findings", "x is slow", "executor")
	out := n.Render()
	if !strings.Contains(out, "Shared context") || !strings.Contains(out, "[executor] x is slow") {
		t.Fatalf("render missing content:\n%s", out)
	}
}

func TestInheritedContextEnsureRenderRecord(t *testing.T) {
	dir := t.TempDir()
	c := NewInheritedContext(dir)
	if err := c.Ensure(); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := c.Record("prefer table-driven tests"); err != nil {
		t.Fatalf("record: %v", err)
	}
	out, err := c.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "prefer table-driven tests") {
		t.Fatalf("render missing recorded learning:\n%s", out)
	}
}

func TestThreadStoreSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewThreadStore(dir)
	nb := NewNotebook()
	nb.Append("findings", "a", "executor")
	want := ThreadSnapshot{ThreadID: "t1", Notebook: nb.Snapshot(), History: []string{"turn 1"}}
	if err := s.Save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := s.Load("t1")
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if len(got.Notebook["findings"]) != 1 || got.History[0] != "turn 1" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if _, ok, _ := s.Load("missing"); ok {
		t.Fatal("missing thread should not load")
	}
}

func TestMailboxDisabledByDefault(t *testing.T) {
	m := NewMailbox()
	if m.Enabled() {
		t.Fatal("mailbox must start disabled")
	}
	if err := m.Post(contracts.MailboxPosted{From: "a", To: "b", Body: "hi"}); err != ErrMailboxDisabled {
		t.Fatalf("want ErrMailboxDisabled, got %v", err)
	}
	m.SetEnabled(true)
	if err := m.Post(contracts.MailboxPosted{From: "a", To: "b", Body: "hi"}); err != nil {
		t.Fatalf("post after enable: %v", err)
	}
	if inbox := m.Inbox("b"); len(inbox) != 1 || inbox[0].Body != "hi" {
		t.Fatalf("inbox = %+v", inbox)
	}
}
