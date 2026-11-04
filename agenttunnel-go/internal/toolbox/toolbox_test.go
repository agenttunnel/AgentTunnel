package toolbox

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestBoxPostAndDrain(t *testing.T) {
	b := New()
	b.Post("t1", Call{Kind: KindDispatch, Role: contracts.RoleExecutor, Task: "do it"})
	b.Post("t1", Call{Kind: KindNote, Text: "found something"})
	b.Post("t2", Call{Kind: KindDispatch, Task: "other thread"})

	drained := b.Drain("t1")
	if len(drained) != 2 || drained[0].Task != "do it" || drained[1].Text != "found something" {
		t.Fatalf("drained = %+v", drained)
	}
	if again := b.Drain("t1"); len(again) != 0 {
		t.Fatalf("drain should clear the inbox, got %+v", again)
	}
	if other := b.Drain("t2"); len(other) != 1 {
		t.Fatalf("t2 = %+v", other)
	}
}

func TestBoxIgnoresInvalidCalls(t *testing.T) {
	b := New()
	b.Post("", Call{Kind: KindDispatch})
	b.Post("t1", Call{})
	if got := b.Drain("t1"); len(got) != 0 {
		t.Fatalf("invalid calls were recorded: %+v", got)
	}
}
