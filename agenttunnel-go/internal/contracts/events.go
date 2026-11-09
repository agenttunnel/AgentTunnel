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
	EvtMessageSent        EventType = "thread.message-sent"
	EvtModelSet           EventType = "thread.model-set"
	EvtAutoModeSet        EventType = "thread.automode-set"
	EvtPeerMailboxSet     EventType = "thread.peer-mailbox-set"
	EvtLeadsSet           EventType = "thread.leads-set"
	EvtTaskAdded          EventType = "thread.task-added"
	EvtTaskStatusChanged  EventType = "thread.task-status-changed"
	EvtTaskDispatched     EventType = "agent.task-dispatched"
	EvtAgentStatusChanged EventType = "agent.status-changed"
	EvtUsageRecorded      EventType = "agent.usage-recorded"
	EvtSharedContextSet   EventType = "context.shared-set"
	EvtMailboxPosted      EventType = "mailbox.posted"
	EvtReviewCommentAdded EventType = "review.comment-added"
	EvtReviewSubmitted    EventType = "review.comments-submitted"
	EvtAgentOutput        EventType = "agent.output"
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
	case EvtMessageSent:
		return decodePayload[MessageSent](data)
	case EvtModelSet:
		return decodePayload[ModelSet](data)
	case EvtAutoModeSet:
		return decodePayload[AutoModeSet](data)
	case EvtPeerMailboxSet:
		return decodePayload[PeerMailboxSet](data)
	case EvtLeadsSet:
		return decodePayload[LeadsSet](data)
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
}

func (ThreadCreated) eventType() EventType { return EvtThreadCreated }

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

// PeerMailboxSet toggles peer-to-peer agent messaging for a thread. On by
// default: thread creation records it enabled.
type PeerMailboxSet struct{ Enabled bool }

func (PeerMailboxSet) eventType() EventType { return EvtPeerMailboxSet }

// MaxLeads bounds a thread's orchestrator team. One lead is the classic single
// orchestrator; more lead a discussion among peers that agree on the plan.
const MaxLeads = 3

// LeadsSet sets how many orchestrators lead a thread's turns. Threads with no
// LeadsSet event have one lead.
type LeadsSet struct{ Count int }

func (LeadsSet) eventType() EventType { return EvtLeadsSet }

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

// ReviewCommentAdded is a human review comment anchored to a diff line. Pending
// comments are handed to the agents on the next turn.
type ReviewCommentAdded struct {
	ID        string    `json:"id"`
	File      string    `json:"file"`
	Line      int       `json:"line"`
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

// AgentOutput is a transient progress line streamed to clients while an agent
// runs. It is never appended to the event log — it exists only so a running turn
// is visible as it happens.
type AgentOutput struct {
	AgentID AgentID `json:"agentId"`
	Kind    LogKind `json:"kind"`
	Text    string  `json:"text"`
}

func (AgentOutput) eventType() EventType { return EvtAgentOutput }
