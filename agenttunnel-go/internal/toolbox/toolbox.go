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
	// KindMessage sends a peer message to another agent (or the orchestrator)
	// through the thread's mailbox. It requires the peer mailbox to be enabled.
	KindMessage Kind = "message"
	// KindSpawn proposes a subagent of the caller's own; it only runs once the
	// team lead approves it.
	KindSpawn Kind = "spawn"
	// KindApprove and KindReject are the lead's decision on a pending spawn
	// request, selected by --name.
	KindApprove Kind = "approve"
	KindReject  Kind = "reject"
	// KindTaskCreate, KindTaskUpdate and KindTaskList maintain the thread's
	// agreed task list. They are applied immediately, not queued; see the
	// transport handler.
	KindTaskCreate Kind = "task-create"
	KindTaskUpdate Kind = "task-update"
	KindTaskList   Kind = "task-list"
	// KindSubthread starts a subthread for the lead's thread. It is answered at
	// once, not queued, so the lead learns of a refusal in the same pass.
	KindSubthread Kind = "subthread"
	// KindAsk asks the human a question and blocks until they answer. Like
	// subthread it is answered at once rather than queued; see question.go.
	KindAsk Kind = "ask"
	// KindReviewComment opens a reviewer's comment thread on a diff range,
	// KindReviewReply answers one, and KindReviewResolve closes or reopens one.
	// They apply at once rather than queueing; see orchestrator/review.go.
	KindReviewComment Kind = "review-comment"
	KindReviewReply   Kind = "review-reply"
	KindReviewResolve Kind = "review-resolve"
	// KindThreadList and KindThreadDelete let a team lead see and remove the
	// other threads in the environment. Both are answered at once and only
	// leads may call them; see transport's handleThreadCall.
	KindThreadList   Kind = "thread-list"
	KindThreadDelete Kind = "thread-delete"
	// KindReadFile, KindWriteFile and KindShell give a team lead the thread's
	// working tree and a shell in it. Answered at once, and only leads may call
	// them; see transport's handleEnvCall.
	KindReadFile  Kind = "read-file"
	KindWriteFile Kind = "write-file"
	KindShell     Kind = "shell"
	// KindSteer redirects a subagent the lead dispatched. It is answered at
	// once, not queued: the reply tells the lead whether the steer was queued
	// for that agent's next run. See Runner.Steer.
	KindSteer Kind = "steer"
	// KindAgentLogs reads a dispatched subagent's full output log, for when a
	// result summary is missing detail. Answered at once, and only leads may
	// call it; see transport's handleAgentLogsCall.
	KindAgentLogs Kind = "agent-logs"
	// KindRevive re-dispatches a finished subagent under its own name and role
	// with its prior output log plus a follow-up question. Like dispatch it is
	// queued in the tool inbox and drained by the orchestrator's toolTasks
	// after the pass, not answered at once.
	KindRevive Kind = "revive"
)

// IsReview reports whether a kind is a review-thread call.
func (k Kind) IsReview() bool {
	switch k {
	case KindReviewComment, KindReviewReply, KindReviewResolve:
		return true
	default:
		return false
	}
}

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
	To        string            `json:"to,omitempty"`
	// File, Line, EndLine and Side anchor a review comment; Thread names the
	// review thread a reply or resolution answers; Reopen moves it back to open.
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	EndLine int    `json:"endLine,omitempty"`
	Side    string `json:"side,omitempty"`
	Thread  string `json:"thread,omitempty"`
	Reopen  bool   `json:"reopen,omitempty"`
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

// HasDispatchNamed reports whether the thread holds a pending dispatch call
// under a roster name, so a peer message addressed to an agent that has not been
// dispatched yet can be deferred beside it instead of rejected.
func (b *Box) HasDispatchNamed(threadID contracts.ThreadID, name string) bool {
	if threadID == "" || name == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.pending[threadID] {
		if c.Kind == KindDispatch && c.Name == name {
			return true
		}
	}
	return false
}
