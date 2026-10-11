package contracts

// CommandType identifies a state-change request. Client-facing commands are
// validated by the decider; internal commands are emitted by reactors.
type CommandType string

// DefaultThreadTitle is the placeholder a thread carries until something names
// it — the orchestrator model, or the user.
const DefaultThreadTitle = "New thread"

const (
	CmdThreadCreate     CommandType = "thread.create"
	CmdThreadRename     CommandType = "thread.rename"
	CmdTurnStart        CommandType = "thread.turn.start"
	CmdModelSet         CommandType = "thread.model.set"
	CmdAutoModeSet      CommandType = "thread.automode.set"
	CmdPeerMailboxSet   CommandType = "thread.peer-mailbox.set"
	CmdTaskDispatch     CommandType = "agent.task.dispatch"
	CmdAgentStatusSet   CommandType = "agent.status.set"
	CmdUsageRecord      CommandType = "agent.usage.record"
	CmdSharedContextSet CommandType = "context.shared.set"
	CmdMailboxPost      CommandType = "mailbox.post"
	CmdMessageAppend    CommandType = "thread.message.append"
	CmdReviewCommentAdd CommandType = "review.comment.add"
)

// DispatchTask is the orchestrator's instruction to spawn a subagent.
type DispatchTask struct {
	AgentID       AgentID    `json:"agentId"`
	ParentAgentID AgentID    `json:"parentAgentId,omitempty"`
	Name          string     `json:"name,omitempty"`
	Role          RoleID     `json:"role"`
	Task          string     `json:"task"`
	Model         string     `json:"model,omitempty"`
	Pipeline      PipelineID `json:"pipelineId,omitempty"`
	DependsOn     []string   `json:"dependsOn,omitempty"`
}

// Command is a request to change thread state. Exactly one payload field is
// meaningful per Type; the decider rejects mismatches.
type Command struct {
	ID             string              `json:"id"`
	Type           CommandType         `json:"type"`
	ThreadID       ThreadID            `json:"threadId"`
	Text           string              `json:"text,omitempty"`
	Title          string              `json:"title,omitempty"`
	ProjectID      string              `json:"projectId,omitempty"`
	ModelSelection *ModelSelection     `json:"modelSelection,omitempty"`
	AutoMode       *bool               `json:"autoMode,omitempty"`
	PeerMailbox    *bool               `json:"peerMailbox,omitempty"`
	Dispatch       *DispatchTask       `json:"dispatch,omitempty"`
	Usage          *UsageRecorded      `json:"usage,omitempty"`
	Status         *AgentStatusChanged `json:"status,omitempty"`
	SharedContext  *SharedContextSet   `json:"sharedContext,omitempty"`
	Mailbox        *MailboxPosted      `json:"mailbox,omitempty"`
	Message        *MessageSent        `json:"message,omitempty"`
	ReviewComment  *ReviewCommentAdded `json:"reviewComment,omitempty"`
}
