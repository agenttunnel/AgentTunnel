package contracts

// CommandType identifies a state-change request. Client-facing commands are
// validated by the decider; internal commands are emitted by reactors.
type CommandType string

// DefaultThreadTitle is the placeholder a thread carries until something names
// it — the orchestrator model, or the user.
const DefaultThreadTitle = "New thread"

const (
	CmdThreadCreate       CommandType = "thread.create"
	CmdThreadRename       CommandType = "thread.rename"
	CmdThreadDelete       CommandType = "thread.delete"
	CmdWorktreeAttach     CommandType = "thread.worktree.attach"
	CmdWorktreeDetach     CommandType = "thread.worktree.detach"
	CmdWorktreeReplace    CommandType = "thread.worktree.replace"
	CmdTurnStart          CommandType = "thread.turn.start"
	CmdModelSet           CommandType = "thread.model.set"
	CmdAutoModeSet        CommandType = "thread.automode.set"
	CmdPeerMailboxSet     CommandType = "thread.peer-mailbox.set"
	CmdLeadsSet           CommandType = "thread.leads.set"
	CmdLeadRuntimeSet     CommandType = "thread.leads.runtime.set"
	CmdSubthreadSpawn     CommandType = "thread.subthread.spawn"
	CmdTaskAdd            CommandType = "thread.task.add"
	CmdTaskStatusSet      CommandType = "thread.task.status.set"
	CmdTaskDispatch       CommandType = "agent.task.dispatch"
	CmdAgentStatusSet     CommandType = "agent.status.set"
	CmdUsageRecord        CommandType = "agent.usage.record"
	CmdSharedContextSet   CommandType = "context.shared.set"
	CmdMailboxPost        CommandType = "mailbox.post"
	CmdMessageAppend      CommandType = "thread.message.append"
	CmdReviewCommentAdd   CommandType = "review.comment.add"
	CmdReviewSubmit       CommandType = "review.submit"
	CmdReviewThreadOpen   CommandType = "review.thread.open"
	CmdReviewThreadReply  CommandType = "review.thread.reply"
	CmdReviewThreadStatus CommandType = "review.thread.status"
	CmdQuestionAsk        CommandType = "thread.question.ask"
	CmdQuestionClose      CommandType = "thread.question.close"
	CmdPRTrack            CommandType = "thread.pr.track"
	CmdSteeringInjected   CommandType = "thread.steering.injected"
)

// DispatchTask is the orchestrator's instruction to spawn a subagent.
type DispatchTask struct {
	AgentID       AgentID    `json:"agentId"`
	ParentAgentID AgentID    `json:"parentAgentId,omitempty"`
	Name          string     `json:"name,omitempty"`
	Role          RoleID     `json:"role"`
	Task          string     `json:"task"`
	Harness       Harness    `json:"harness,omitempty"`
	Model         string     `json:"model,omitempty"`
	Pipeline      PipelineID `json:"pipelineId,omitempty"`
	DependsOn     []string   `json:"dependsOn,omitempty"`

	// Override honors Harness/Model even outside auto mode. It marks an
	// explicit user choice (council membership) rather than an orchestrator
	// pick, so it does not persist and never crosses a client boundary.
	Override bool `json:"-"`
}

// Command is a request to change thread state. Exactly one payload field is
// meaningful per Type; the decider rejects mismatches.
type Command struct {
	ID             string                     `json:"id"`
	Type           CommandType                `json:"type"`
	ThreadID       ThreadID                   `json:"threadId"`
	Text           string                     `json:"text,omitempty"`
	Title          string                     `json:"title,omitempty"`
	ProjectID      string                     `json:"projectId,omitempty"`
	ModelSelection *ModelSelection            `json:"modelSelection,omitempty"`
	AutoMode       *bool                      `json:"autoMode,omitempty"`
	PeerMailbox    *bool                      `json:"peerMailbox,omitempty"`
	Leads          *int                       `json:"leads,omitempty"`
	LeadRuntime    *LeadRuntimeSet            `json:"leadRuntime,omitempty"`
	ParentThreadID ThreadID                   `json:"parentThreadId,omitempty"`
	Worktree       *WorktreeRef               `json:"worktree,omitempty"`
	Subthread      *SubthreadSpawned          `json:"subthread,omitempty"`
	TaskAdd        *TaskAdded                 `json:"taskAdd,omitempty"`
	TaskStatus     *TaskStatusChanged         `json:"taskStatus,omitempty"`
	Dispatch       *DispatchTask              `json:"dispatch,omitempty"`
	Usage          *UsageRecorded             `json:"usage,omitempty"`
	Status         *AgentStatusChanged        `json:"status,omitempty"`
	SharedContext  *SharedContextSet          `json:"sharedContext,omitempty"`
	Mailbox        *MailboxPosted             `json:"mailbox,omitempty"`
	Message        *MessageSent               `json:"message,omitempty"`
	Injected       *SteeringInjected          `json:"injected,omitempty"`
	ReviewComment  *ReviewCommentAdded        `json:"reviewComment,omitempty"`
	ReviewSubmit   *ReviewCommentsSubmitted   `json:"reviewSubmit,omitempty"`
	ReviewOpen     *ReviewThreadOpened        `json:"reviewOpen,omitempty"`
	ReviewReply    *ReviewThreadReplied       `json:"reviewReply,omitempty"`
	ReviewStatus   *ReviewThreadStatusChanged `json:"reviewStatus,omitempty"`
	QuestionAsk    *QuestionAsked             `json:"questionAsk,omitempty"`
	QuestionClose  *QuestionClosed            `json:"questionClose,omitempty"`
	PullRequest    *PullRequestRef            `json:"pullRequest,omitempty"`
}
