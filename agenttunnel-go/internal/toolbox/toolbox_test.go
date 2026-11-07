package toolbox

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestHasDispatchNamed(t *testing.T) {
	b := New()
	b.Post("t1", Call{Kind: KindDispatch, AgentID: "agent-orchestrator", Name: "worker", Role: contracts.RoleExecutor, Task: "work"})
	b.Post("t1", Call{Kind: KindMessage, AgentID: "agent-orchestrator", To: "worker", Text: "hi"})

	if !b.HasDispatchNamed("t1", "worker") {
		t.Fatal("pending dispatch under the name should be found")
	}
	if b.HasDispatchNamed("t1", "ghost") {
		t.Fatal("unknown name must not be found")
	}
	if b.HasDispatchNamed("t2", "worker") {
		t.Fatal("another thread's dispatch must not be found")
	}

	b.Drain("t1")
	if b.HasDispatchNamed("t1", "worker") {
		t.Fatal("drained dispatch must not be found")
	}
}

func TestHasDispatchNamedIgnoresOtherKinds(t *testing.T) {
	b := New()
	b.Post("t1", Call{Kind: KindNote, AgentID: "agent-orchestrator", Name: "worker", Text: "hi"})
	if b.HasDispatchNamed("t1", "worker") {
		t.Fatal("a note carrying a name must not count as a pending dispatch")
	}
}
