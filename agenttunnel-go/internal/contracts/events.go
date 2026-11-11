package contracts

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventType identifies a persisted domain event.
type EventType string

const (
	EvtThreadCreated      EventType = "thread.created"
	EvtThreadRenamed      EventType = "thread.renamed"
	EvtThreadDeleted      EventType = "thread.deleted"
	EvtWorktreeAttached   EventType = "thread.worktree-attached"
	EvtWorktreeDetached   EventType = "thread.worktree-detached"
	EvtWorktreeReplaced   EventType = "thread.worktree-replaced"
	EvtMessageSent        EventType = "thread.message-sent"
	EvtModelSet           EventType = "thread.model-set"
	EvtAutoModeSet        EventType = "thread.automode-set"
	EvtArchiveSet         EventType = "thread.archive-set"
	EvtPeerMailboxSet     EventType = "thread.peer-mailbox-set"
	EvtLeadsSet           EventType = "thread.leads-set"
	EvtLeadRuntimeSet     EventType = "thread.lead-runtime-set"
	EvtSubthreadSpawned   EventType = "thread.subthread-spawned"
	EvtTaskAdded          EventType = "thread.task-added"
	EvtTaskStatusChanged  EventType = "thread.task-status-changed"
	EvtTaskDispatched     EventType = "agent.task-dispatched"
	EvtAgentStatusChanged EventType = "agent.status-changed"
	EvtUsageRecorded      EventType = "agent.usage-recorded"
	EvtSharedContextSet   EventType = "context.shared-set"
	EvtMailboxPosted      EventType = "mailbox.posted"
	EvtReviewCommentAdded EventType = "review.comment-added"
	EvtReviewSubmitted    EventType = "review.comments-submitted"
	EvtReviewThreadOpened EventType = "review.thread-opened"
	EvtReviewThreadReply  EventType = "review.thread-replied"
	EvtReviewThreadStatus EventType = "review.thread-status-changed"
	EvtAgentOutput        EventType = "agent.output"
	EvtQuestionAsked      EventType = "thread.question-asked"
	EvtQuestionClosed     EventType = "thread.question-closed"
	EvtPRTracked          EventType = "thread.pr-tracked"
)

// EventPayload is implemented by every concrete event payload. The unexported
// marker method keeps the set closed to this package.
type EventPayload interface {
	eventType() EventType
}

// Event is an immutable fact appended to a thread's log.
type Event struct {
	Seq      int64        `json:"seq"`
	ThreadID ThreadID     `json:"threadId"`
	At       time.Time    `json:"at"`
	Payload  EventPayload `json:"payload"`
}

// Type returns the event's discriminating type.
func (e Event) Type() EventType { return e.Payload.eventType() }

// MarshalJSON adds the discriminating "type" field so clients can dispatch on it
// without knowing every payload struct in advance.
func (e Event) MarshalJSON() ([]byte, error) {
	type wire struct {
		Seq      int64        `json:"seq"`
		ThreadID ThreadID     `json:"threadId"`
		Type     EventType    `json:"type"`
		At       time.Time    `json:"at"`
		Payload  EventPayload `json:"payload"`
	}
	return json.Marshal(wire{Seq: e.Seq, ThreadID: e.ThreadID, Type: e.Type(), At: e.At, Payload: e.Payload})
}

// UnmarshalJSON decodes an event, rebuilding the concrete payload from its
// discriminating type. This is what lets a persisted event log be replayed.
func (e *Event) UnmarshalJSON(data []byte) error {
	var wire struct {
		Seq      int64           `json:"seq"`
		ThreadID ThreadID        `json:"threadId"`
		Type     EventType       `json:"type"`
		At       time.Time       `json:"at"`
		Payload  json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	payload, err := UnmarshalPayload(wire.Type, wire.Payload)
	if err != nil {
		return err
	}
	e.Seq = wire.Seq
	e.ThreadID = wire.ThreadID
	e.At = wire.At
	e.Payload = payload
	return nil
}

// UnmarshalPayload decodes a persisted payload by its event type. Transient
// payloads (agent output) are deliberately not decodable: they never persist.
func UnmarshalPayload(t EventType, data []byte) (EventPayload, error) {
	switch t {
	case EvtThreadCreated:
		return decodePayload[ThreadCreated](data)
	case EvtThreadRenamed:
		return decodePayload[ThreadRenamed](data)
	case EvtThreadDeleted:
		return decodePayload[ThreadDeleted](data)
	case EvtWorktreeAttached:
		return decodePayload[WorktreeAttached](data)
	case EvtWorktreeDetached:
		return decodePayload[WorktreeDetached](data)
	case EvtWorktreeReplaced:
		return decodePayload[WorktreeReplaced](data)
	case EvtMessageSent:
		return decodePayload[MessageSent](data)
	case EvtModelSet:
		return decodePayload[ModelSet](data)
	case EvtAutoModeSet:
		return decodePayload[AutoModeSet](data)
	case EvtArchiveSet:
		return decodePayload[ArchiveSet](data)
	case EvtPeerMailboxSet:
		return decodePayload[PeerMailboxSet](data)
	case EvtLeadsSet:
		return decodePayload[LeadsSet](data)
	case EvtLeadRuntimeSet:
		return decodePayload[LeadRuntimeSet](data)
	case EvtSubthreadSpawned:
		return decodePayload[SubthreadSpawned](data)
	case EvtTaskAdded:
		return decodePayload[TaskAdded](data)
	case EvtTaskStatusChanged:
		return decodePayload[TaskStatusChanged](data)
	case EvtTaskDispatched:
		return decodePayload[TaskDispatched](data)
	case EvtAgentStatusChanged:
		return decodePayload[AgentStatusChanged](data)
	case EvtUsageRecorded:
		return decodePayload[UsageRecorded](data)
	case EvtSharedContextSet:
		return decodePayload[SharedContextSet](data)
	case EvtMailboxPosted:
		return decodePayload[MailboxPosted](data)
	case EvtReviewCommentAdded:
		return decodePayload[ReviewCommentAdded](data)
	case EvtReviewSubmitted:
		return decodePayload[ReviewCommentsSubmitted](data)
	case EvtReviewThreadOpened:
		return decodePayload[ReviewThreadOpened](data)
	case EvtReviewThreadReply:
		return decodePayload[ReviewThreadReplied](data)
	case EvtReviewThreadStatus:
		return decodePayload[ReviewThreadStatusChanged](data)
	case EvtQuestionAsked:
		return decodePayload[QuestionAsked](data)
	case EvtQuestionClosed:
		return decodePayload[QuestionClosed](data)
	case EvtPRTracked:
		return decodePayload[PRTracked](data)
	default:
		return nil, fmt.Errorf("unknown event type %q", t)
	}
}

func decodePayload[T EventPayload](data []byte) (EventPayload, error) {
	var payload T
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// --- concrete payloads ---

type ThreadCreated struct {
	Title     string `json:"title"`
	ProjectID string `json:"projectId,omitempty"`
	// ParentThreadID is set on a subthread: the thread that started it.
	ParentThreadID ThreadID `json:"parentThreadId,omitempty"`
	// Worktree is set when the thread was created with its worktree already
	// cut, or inherits a parent's.
	Worktree *WorktreeRef `json:"worktree,omitempty"`
}

func (ThreadCreated) eventType() EventType { return EvtThreadCreated }

// WorktreeAttached gives a thread its worktree after creation, the first time a
// turn runs in a git project. A thread has at most one worktree.
type WorktreeAttached struct {
	Worktree WorktreeRef `json:"worktree"`
}

func (WorktreeAttached) eventType() EventType { return EvtWorktreeAttached }

// WorktreeDetached clears a thread's own worktree, so the thread can be given a
// fresh one. It is written when the worktree was removed or its directory was
// lost; an inherited worktree is never detached, the subthread follows its parent.
type WorktreeDetached struct{}

func (WorktreeDetached) eventType() EventType { return EvtWorktreeDetached }

// WorktreeReplaced swaps a thread's worktree for a fresh one in a single event,
// so the thread never holds no ref between the old and the new. It is written
// when the old directory was lost and a new one was cut; the ownership rules
// (own or inherited) are the same as for WorktreeAttached.
type WorktreeReplaced struct {
	Worktree WorktreeRef `json:"worktree"`
}

func (WorktreeReplaced) eventType() EventType { return EvtWorktreeReplaced }

// ThreadRenamed renames a thread, typically after the model has read the first
// exchange and picked a title.
type ThreadRenamed struct {
	Title string `json:"title"`
}

func (ThreadRenamed) eventType() EventType { return EvtThreadRenamed }

// ThreadDeleted tombstones a thread: the log is kept for audit, but the read
// model marks the thread gone and it disappears from listings.
type ThreadDeleted struct{}

func (ThreadDeleted) eventType() EventType { return EvtThreadDeleted }

type MessageSent struct {
	MessageID string `json:"messageId"`
	Author    string `json:"author"` // "user" | "orchestrator" | agent id
	Text      string `json:"text"`
}

func (MessageSent) eventType() EventType { return EvtMessageSent }

type ModelSet struct{ Selection ModelSelection }

func (ModelSet) eventType() EventType { return EvtModelSet }

type AutoModeSet struct{ Enabled bool }

func (AutoModeSet) eventType() EventType { return EvtAutoModeSet }

// ArchiveSet files a thread under Archived or brings it back. An archived
// thread is hidden from the board's working columns but keeps its history.
type ArchiveSet struct{ Archived bool }

func (ArchiveSet) eventType() EventType { return EvtArchiveSet }

// PeerMailboxSet toggles peer-to-peer agent messaging for a thread. On by
// default: thread creation records it enabled.
type PeerMailboxSet struct{ Enabled bool }

func (PeerMailboxSet) eventType() EventType { return EvtPeerMailboxSet }

// LeadsSet sets how many orchestrators lead a thread's turns. One lead is the
// classic single orchestrator; more lead a discussion among peers that agree on
// the plan. Threads with no LeadsSet event have one lead.
type LeadsSet struct{ Count int }

func (LeadsSet) eventType() EventType { return EvtLeadsSet }

// LeadRuntime overrides one lead's runtime on a thread. An empty Harness means
// the lead inherits the orchestrator role; an empty Model or Variant means the
// harness default. Overriding the harness resets the model to its default.
type LeadRuntime struct {
	Harness Harness `json:"harness,omitempty"`
	Model   string  `json:"model,omitempty"`
	Variant string  `json:"variant,omitempty"`
}

// LeadRuntimeSet sets lead Index's (zero-based) runtime override.
type LeadRuntimeSet struct {
	Index   int         `json:"index"`
	Runtime LeadRuntime `json:"runtime"`
}

func (LeadRuntimeSet) eventType() EventType { return EvtLeadRuntimeSet }

// MaxSubthreads bounds how many subthreads one thread may start. A subthread
// cannot start its own, so this is also the whole tree below a thread.
const MaxSubthreads = 4

// SubthreadSpawned records that a thread started the subthread ThreadID. The
// subthread is a thread of its own; this event is the parent's side of the link.
type SubthreadSpawned struct {
	ThreadID ThreadID `json:"threadId"`
}

func (SubthreadSpawned) eventType() EventType { return EvtSubthreadSpawned }

// TaskStatus is where a task on a thread's agreed list stands. Statuses are
// set by the leads as they judge progress; they are not derived from agent runs.
type TaskStatus string

const (
	TaskTodo  TaskStatus = "todo"
	TaskDoing TaskStatus = "doing"
	TaskDone  TaskStatus = "done"
)

// Valid reports whether s is a known task status.
func (s TaskStatus) Valid() bool {
	switch s {
	case TaskTodo, TaskDoing, TaskDone:
		return true
	}
	return false
}

// TaskAdded puts a task on the thread's list. Name is the handle later status
// changes use; it is unique within the thread.
type TaskAdded struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

func (TaskAdded) eventType() EventType { return EvtTaskAdded }

// TaskStatusChanged moves a task named on the thread's list to a new status.
type TaskStatusChanged struct {
	Name   string     `json:"name"`
	Status TaskStatus `json:"status"`
}

func (TaskStatusChanged) eventType() EventType { return EvtTaskStatusChanged }

type TaskDispatched struct {
	AgentID       AgentID    `json:"agentId"`
	ParentAgentID AgentID    `json:"parentAgentId,omitempty"`
	Name          string     `json:"name,omitempty"`
	Role          RoleID     `json:"role"`
	Task          string     `json:"task"`
	Harness       Harness    `json:"harness,omitempty"`
	Model         string     `json:"model"`
	PipelineID    PipelineID `json:"pipelineId,omitempty"`
	DependsOn     []string   `json:"dependsOn,omitempty"`
}

func (TaskDispatched) eventType() EventType { return EvtTaskDispatched }

type AgentStatusChanged struct {
	AgentID AgentID     `json:"agentId"`
	Status  AgentStatus `json:"status"`
	Reason  string      `json:"reason,omitempty"`
}

func (AgentStatusChanged) eventType() EventType { return EvtAgentStatusChanged }

type UsageRecorded struct {
	AgentID AgentID    `json:"agentId"`
	Role    RoleID     `json:"role"`
	Usage   TokenUsage `json:"usage"`
	// Turn is the user turn this spend belongs to (0 when unknown).
	Turn int `json:"turn,omitempty"`
}

func (UsageRecorded) eventType() EventType { return EvtUsageRecorded }

type SharedContextSet struct {
	Key      string  `json:"key"`
	Value    string  `json:"value"`
	AuthorID AgentID `json:"authorId"`
}

func (SharedContextSet) eventType() EventType { return EvtSharedContextSet }

// MailboxPosted is a message from one agent to another (or to the orchestrator).
type MailboxPosted struct {
	From AgentID `json:"from"`
	To   AgentID `json:"to"`
	Kind string  `json:"kind"` // "decision-request" | "note" | "reply"
	Body string  `json:"body"`
}

func (MailboxPosted) eventType() EventType { return EvtMailboxPosted }

// ReviewCommentAdded is a human review comment anchored to a diff range. Pending
// comments are handed to the agents on the next turn. EndLine is zero for
// comments recorded before ranges existed; the projector reads it as Line. Side
// is "old" for removed lines and empty or "new" otherwise.
type ReviewCommentAdded struct {
	ID        string    `json:"id"`
	File      string    `json:"file"`
	Line      int       `json:"line"`
	EndLine   int       `json:"endLine,omitempty"`
	Side      string    `json:"side,omitempty"`
	Body      string    `json:"body"`
	AuthorID  AgentID   `json:"authorId"`
	CreatedAt time.Time `json:"createdAt"`
}

func (ReviewCommentAdded) eventType() EventType { return EvtReviewCommentAdded }

// ReviewCommentsSubmitted marks pending review comments as handed to the
// team. It is recorded alongside the user turn that carries them.
type ReviewCommentsSubmitted struct {
	CommentIDs []string `json:"commentIds"`
}

func (ReviewCommentsSubmitted) eventType() EventType { return EvtReviewSubmitted }

// ReviewStatus is where a review thread stands. Only the reviewer who opened a
// thread moves it between open and resolved.
type ReviewStatus string

const (
	ReviewStatusOpen     ReviewStatus = "open"
	ReviewStatusResolved ReviewStatus = "resolved"
)

// ReviewThreadOpened starts a reviewer's comment thread on a diff range. The
// embedded comment carries the anchor; its ID is the thread ID every later
// reply and status change refers to. Only a reviewer-role agent may open one.
type ReviewThreadOpened struct {
	ReviewCommentAdded
}

func (ReviewThreadOpened) eventType() EventType { return EvtReviewThreadOpened }

// ReviewThreadReplied is a reply on an existing review thread, from any agent.
// ThreadID names the review thread (the opening comment's ID), not the
// conversation thread the event belongs to.
type ReviewThreadReplied struct {
	ThreadID  string    `json:"threadId"`
	AuthorID  AgentID   `json:"authorId"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

func (ReviewThreadReplied) eventType() EventType { return EvtReviewThreadReply }

// ReviewThreadStatusChanged moves a review thread to open or resolved. A
// non-empty Body is recorded as a reply by the same author.
type ReviewThreadStatusChanged struct {
	ThreadID  string       `json:"threadId"`
	AuthorID  AgentID      `json:"authorId"`
	Status    ReviewStatus `json:"status"`
	Body      string       `json:"body,omitempty"`
	CreatedAt time.Time    `json:"createdAt"`
}

func (ReviewThreadStatusChanged) eventType() EventType { return EvtReviewThreadStatus }

// QuestionKind is the shape of an answer an agent asks the human for.
type QuestionKind string

const (
	// QuestionChoice takes exactly one of the options (or free text as "other").
	QuestionChoice QuestionKind = "choice"
	// QuestionMulti takes any non-empty subset of the options (or free text).
	QuestionMulti QuestionKind = "multi"
	// QuestionText takes free text only.
	QuestionText QuestionKind = "text"
)

// Valid reports whether k is a known question kind.
func (k QuestionKind) Valid() bool {
	switch k {
	case QuestionChoice, QuestionMulti, QuestionText:
		return true
	}
	return false
}

// Question limits. Options are bounded so the question card stays readable;
// the pending cap keeps an agent from parking a pile of questions on the human.
const (
	MaxQuestionOptions      = 12
	MaxQuestionPending      = 5
	MaxQuestionLength       = 2000
	MaxQuestionOptionLength = 200
)

// QuestionAsked is an agent's question for the human. It stays pending until a
// QuestionClosed for the same id: the agent's blocked tool call is the only
// thing that keeps it pending.
type QuestionAsked struct {
	QuestionID string       `json:"questionId"`
	AgentID    AgentID      `json:"agentId"`
	Kind       QuestionKind `json:"kind"`
	Question   string       `json:"question"`
	Options    []string     `json:"options,omitempty"`
	// Notify names the agent (or "orchestrator") that also hears the answer as
	// a peer message. Empty means no extra recipient.
	Notify string `json:"notify,omitempty"`
}

func (QuestionAsked) eventType() EventType { return EvtQuestionAsked }

// QuestionClosed ends a pending question, either with the human's answer or with
// the reason it could not be answered. One closing event carries both shapes, so
// replay never has to reconcile two events.
type QuestionClosed struct {
	QuestionID string `json:"questionId"`
	Answered   bool   `json:"answered"`
	// Selected holds chosen options for choice and multi kinds.
	Selected []string `json:"selected,omitempty"`
	// Text is free text: the whole answer for a text question, or an "other"
	// answer for a choice or multi question.
	Text string `json:"text,omitempty"`
	// Reason says why an unanswered question closed.
	Reason string `json:"reason,omitempty"`
}

func (QuestionClosed) eventType() EventType { return EvtQuestionClosed }

// ReasonQuestionDeclined closes a question the human dismissed. It is not a
// failure: the asking agent is told so it can carry on without the answer.
const ReasonQuestionDeclined = "declined by user"

// AgentOutput is a transient progress line streamed to clients while an agent
// runs. It is never appended to the event log — it exists only so a running turn
// is visible as it happens.
type AgentOutput struct {
	AgentID AgentID `json:"agentId"`
	Kind    LogKind `json:"kind"`
	Text    string  `json:"text"`
}

func (AgentOutput) eventType() EventType { return EvtAgentOutput }

// PullRequestRef identifies a pull request a thread opened. URL is the key:
// a thread tracks every request it has opened, and each one is recorded once.
type PullRequestRef struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// PRTracked records that a thread has a pull request. Details such as title,
// comments, and reviewers are read from the forge on demand, never stored.
type PRTracked struct{ PullRequest PullRequestRef }

func (PRTracked) eventType() EventType { return EvtPRTracked }
