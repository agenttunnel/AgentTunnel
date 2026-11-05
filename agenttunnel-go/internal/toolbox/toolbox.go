// Package toolbox is the in-process inbox for the `atd tool` command.
//
// A harness agent that can run shell commands dispatches work by executing the
// daemon binary:
//
//	"$AT_TOOL" tool dispatch --role executor --task "add a test for X"
//
// The command posts to the running daemon, which drops the call in this inbox.
// The orchestrator drains the inbox after each pass and runs what it finds. This
// gives agents a real, callable tool instead of a magic text prefix they have no
// reason to trust.
package toolbox

import (
	"sync"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Kind is the kind of tool call.
type Kind string

const (
	KindDispatch Kind = "dispatch"
	KindNote     Kind = "note"
	KindProgress Kind = "progress"
	KindDone     Kind = "done"
	// KindLearn appends a durable learning to the environment's inherited
	// context, so later threads start with what this one discovered.
	KindLearn Kind = "learn"
)

// Call is one recorded tool invocation.
type Call struct {
	Kind      Kind              `json:"kind"`
	AgentID   contracts.AgentID `json:"agentId"`
	Name      string            `json:"name,omitempty"`
	Lane      string            `json:"lane,omitempty"`
	Role      contracts.RoleID  `json:"role"`
	Task      string            `json:"task,omitempty"`
	Harness   string            `json:"harness,omitempty"`
	Model     string            `json:"model,omitempty"`
	DependsOn []string          `json:"dependsOn,omitempty"`
	Text      string            `json:"text,omitempty"`
}

// Box holds pending tool calls, partitioned by thread.
type Box struct {
	mu      sync.Mutex
	pending map[contracts.ThreadID][]Call
}

// New returns an empty inbox.
func New() *Box {
	return &Box{pending: map[contracts.ThreadID][]Call{}}
}

// Post records a call for a thread.
func (b *Box) Post(threadID contracts.ThreadID, call Call) {
	if threadID == "" || call.Kind == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending[threadID] = append(b.pending[threadID], call)
}

// Drain returns and clears a thread's pending calls.
func (b *Box) Drain(threadID contracts.ThreadID) []Call {
	b.mu.Lock()
	defer b.mu.Unlock()
	calls := b.pending[threadID]
	delete(b.pending, threadID)
	return calls
}
