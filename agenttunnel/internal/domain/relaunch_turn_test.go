package domain

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// A relaunched thread's turns are authored by its controller, never a phantom
// user, and still open a real turn so per-turn spend stays attributable.
func TestControlledTurnStartIsAuthoredByController(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	s.ControlledBy = "t9"

	evs, err := Decide(contracts.Command{ID: "m1", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "go deeper"}, s)
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := evs[0].(contracts.MessageSent)
	if !ok {
		t.Fatalf("payload = %T, want MessageSent", evs[0])
	}
	if msg.Author != "t9" {
		t.Fatalf("author = %q, want the controller t9", msg.Author)
	}

	// An uncontrolled thread's turn keeps the user as its author.
	free := NewThreadState("t2")
	free.Exists = true
	evs, err = Decide(contracts.Command{ID: "m2", Type: contracts.CmdTurnStart, ThreadID: "t2", Text: "go"}, free)
	if err != nil {
		t.Fatal(err)
	}
	if msg, ok = evs[0].(contracts.MessageSent); !ok || msg.Author != "user" {
		t.Fatalf("uncontrolled turn payload = %#v, want a user-authored MessageSent", evs)
	}
}

// The controller-authored relaunch turn opens a turn like a user message, so
// its spend does not fold into the turn before it.
func TestControllerRelaunchOpensNewTurn(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})

	mustDispatch(t, e, ctx, contracts.Command{ID: "m1", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "first"})
	mustDispatch(t, e, ctx, contracts.Command{
		ID: "k1", Type: contracts.CmdThreadControl, ThreadID: "t1", Controller: "t9",
	})
	mustDispatch(t, e, ctx, contracts.Command{ID: "m2", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "relaunched prompt"})
	mustDispatch(t, e, ctx, contracts.Command{
		ID: "a1", Type: contracts.CmdMessageAppend, ThreadID: "t1",
		Message: &contracts.MessageSent{MessageID: "a1", Author: "orchestrator", Text: "relaunch summary"},
	})

	st, _ := e.State(ctx, "t1")
	if st.ControlledBy != "t9" {
		t.Fatalf("controlledBy = %q, want t9", st.ControlledBy)
	}
	if st.Turn != 2 {
		t.Fatalf("turn = %d, want 2 (user turn + controller relaunch)", st.Turn)
	}
	if len(st.Turns) != 2 {
		t.Fatalf("turns = %+v, want 2 tracked turns", st.Turns)
	}
	if n := len(st.Messages); n != 3 {
		t.Fatalf("messages = %d, want 3 (two turn openers plus the summary)", n)
	}
	if got := st.Messages[1]; got.Author != "t9" || got.Text != "relaunched prompt" {
		t.Fatalf("relaunch message = %+v, want the controller-authored prompt", got)
	}
	// The in-turn summary must not have opened a third turn.
	if st.Turn != 2 {
		t.Fatalf("summary opened a turn: %d", st.Turn)
	}
}
