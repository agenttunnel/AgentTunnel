package domain

import (
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Message is a rendered conversation message in a thread.
type Message struct {
	ID     string    `json:"id"`
	Author string    `json:"author"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

// UsageStep is one model step's spend for a single agent — a row in an agent's
// drill-in table.
type UsageStep struct {
	At    time.Time            `json:"at"`
	Usage contracts.TokenUsage `json:"usage"`
}

// TurnUsage is the spend of every agent within one user turn, so the orchestrator
// and each subagent can be seen per turn rather than only in total.
type TurnUsage struct {
	Turn       int                                        `json:"turn"`
	At         time.Time                                  `json:"at,omitempty"`
	AgentOrder []contracts.AgentID                        `json:"agentOrder"`
	Agents     map[contracts.AgentID]contracts.TokenUsage `json:"agents"`
	// EndedAt is when a lead last reached a terminal status in this turn: the
	// turn's end as the team saw it. Zero while the turn is open, and for turns
	// recorded before this was tracked.
	EndedAt time.Time `json:"endedAt,omitempty"`
	// Failed is set when a lead ended failed or cancelled in this turn.
	Failed bool `json:"failed,omitempty"`
}

// MailboxMessage is one peer message in a thread's read model: the durable
// MailboxPosted payload stamped with the time it was recorded and the turn it
// belongs to. The turn is the dedupe window for harnesses that both execute
// the message command and echo the same line as text.
type MailboxMessage struct {
	From contracts.AgentID `json:"from"`
	To   contracts.AgentID `json:"to"`
	Kind string            `json:"kind"`
	Body string            `json:"body"`
	At   time.Time         `json:"at"`
	Turn int               `json:"turn"`
}

// maxMailboxEntries bounds the mailbox read model, the way usage steps are
// bounded: agent traffic is machine-generated and thread.get ships the whole
// list, so without a cap the snapshot grows for the life of the thread. The
// durable event log keeps everything; only the read model truncates.
const maxMailboxEntries = 200

// TaskItem is one task on a thread's agreed list.
type TaskItem struct {
	Name   string               `json:"name"`
	Title  string               `json:"title"`
	Status contracts.TaskStatus `json:"status"`
}

// maxTasks bounds the task list: the leads' plan, shipped whole by thread.get.
const maxTasks = 100

// ThreadState is the read model for one thread, derived entirely from its event
// log by the projector. It is never mutated by clients directly.
type ThreadState struct {
	ID     contracts.ThreadID `json:"id"`
	Exists bool               `json:"exists"`
	// Provisional marks a thread whose creation lives in memory only: nothing
	// was appended to the event log, so a thread that is never used leaves no
	// trace. The first durable command materializes it. Never serialized — it
	// is an engine-side condition, not client-visible state.
	Provisional bool `json:"-"`
	// Deleted marks a tombstoned thread: the event log is retained, but the
	// thread is gone from listings and refuses further commands.
	Deleted bool `json:"deleted"`
	// Archived is set by the user; it moves the thread to the board's Archived column.
	Archived    bool                     `json:"archived"`
	Title       string                   `json:"title"`
	ProjectID   string                   `json:"projectId,omitempty"`
	UpdatedAt   time.Time                `json:"updatedAt"`
	Model       contracts.ModelSelection `json:"model"`
	AutoMode    bool                     `json:"autoMode"`
	PeerMailbox bool                     `json:"peerMailbox"`
	// Leads is the stored orchestrator team size; zero means one. Read it
	// through LeadCount.
	Leads int `json:"leads"`
	// LeadRuntimes holds per-lead runtime overrides, indexed by lead. A missing
	// entry means the lead inherits the orchestrator role. Read through
	// LeadRuntimeFor.
	LeadRuntimes []contracts.LeadRuntime `json:"leadRuntimes,omitempty"`
	// ParentThreadID is set on a subthread: the thread that started it. Its
	// presence is also the depth check — a subthread starts no subthreads.
	ParentThreadID contracts.ThreadID `json:"parentThreadId,omitempty"`
	// Worktree is the checkout the thread's agents run in, when the project is
	// a git repository. Nil means the project's own path.
	Worktree *contracts.WorktreeRef `json:"worktree,omitempty"`
	// Subthreads lists the threads this one started, in start order.
	Subthreads []contracts.ThreadID `json:"subthreads,omitempty"`
	Messages   []Message            `json:"messages"`
	// Tasks is the agreed task list, in the order the leads added them.
	Tasks []TaskItem `json:"tasks"`
	// PullRequests lists the pull requests this thread opened, in the order they
	// were first seen. Keyed by URL.
	PullRequests []contracts.PullRequestRef `json:"pullRequests,omitempty"`

	// Turn counts user messages: the turn spend currently accrues to.
	Turn  int         `json:"turn"`
	Turns []TurnUsage `json:"turns"`

	Agents     map[contracts.AgentID]contracts.Agent      `json:"agents"`
	AgentOrder []contracts.AgentID                        `json:"agentOrder"`
	Usage      map[contracts.AgentID]contracts.AgentUsage `json:"usage"`
	// Steps is every model step's spend per agent, newest last (bounded).
	Steps   map[contracts.AgentID][]UsageStep `json:"steps"`
	Mailbox []MailboxMessage                  `json:"mailbox"`

	ReviewComments []ReviewComment `json:"reviewComments"`
	// ReviewThreads are reviewer comment threads with agent replies, oldest first.
	ReviewThreads []ReviewThread `json:"reviewThreads"`

	// Questions are the agent questions asked of the human, oldest first.
	Questions []Question `json:"questions"`

	Shared      map[string]string `json:"shared"`
	SharedOrder []string          `json:"sharedOrder"`

	Version int64 `json:"version"`
}

// ReviewComment is a human line comment on the diff. Submitted comments were
// handed to the team in a review turn and stay listed for reference.
type ReviewComment struct {
	contracts.ReviewCommentAdded
	Submitted bool `json:"submitted"`
}

// ReviewThread is a reviewer's comment thread on a diff range. The reviewer
// who opened it is the only one who may change its status; any agent may reply.
type ReviewThread struct {
	ID        string                 `json:"id"`
	File      string                 `json:"file"`
	Line      int                    `json:"line"`
	EndLine   int                    `json:"endLine"`
	Side      string                 `json:"side,omitempty"`
	Body      string                 `json:"body"`
	AuthorID  contracts.AgentID      `json:"authorId"`
	Status    contracts.ReviewStatus `json:"status"`
	Replies   []ReviewReply          `json:"replies"`
	OpenedAt  time.Time              `json:"openedAt"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

// ReviewReply is one agent's reply on a review thread.
type ReviewReply struct {
	AuthorID contracts.AgentID `json:"authorId"`
	Body     string            `json:"body"`
	At       time.Time         `json:"at"`
}

// reviewThreadIndex returns the position of a review thread, or -1.
func (s *ThreadState) reviewThreadIndex(id string) int {
	for i := range s.ReviewThreads {
		if s.ReviewThreads[i].ID == id {
			return i
		}
	}
	return -1
}

// taskIndex returns the position of a named task, or -1.
func (s *ThreadState) taskIndex(name string) int {
	for i := range s.Tasks {
		if s.Tasks[i].Name == name {
			return i
		}
	}
	return -1
}

// LeadRuntimeFor is lead i's runtime override; the zero value inherits.
func (s *ThreadState) LeadRuntimeFor(i int) contracts.LeadRuntime {
	if i < 0 || i >= len(s.LeadRuntimes) {
		return contracts.LeadRuntime{}
	}
	return s.LeadRuntimes[i]
}

// LeadCount is the number of orchestrators leading the thread's turns.
func (s *ThreadState) LeadCount() int {
	if s.Leads < 1 {
		return 1
	}
	return s.Leads
}

// NewThreadState returns an empty read model for a thread.
func NewThreadState(id contracts.ThreadID) *ThreadState {
	return &ThreadState{
		ID:     id,
		Agents: make(map[contracts.AgentID]contracts.Agent),
		Usage:  make(map[contracts.AgentID]contracts.AgentUsage),
		Steps:  make(map[contracts.AgentID][]UsageStep),
		Shared: make(map[string]string),
	}
}

// ThreadUsage rolls up orchestrator vs subagent spend, plus a per-agent listing.
func (s *ThreadState) ThreadUsage() contracts.ThreadUsage {
	var out contracts.ThreadUsage
	for _, id := range s.AgentOrder {
		attributed := s.Usage[id]
		if attributed.AgentID == "" {
			attributed.AgentID = id
			attributed.Role = s.Agents[id].Role
		}
		if s.Agents[id].IsOrchestrator() {
			out.Orchestrator = out.Orchestrator.Add(attributed.Usage)
		} else {
			out.Subagents = out.Subagents.Add(attributed.Usage)
		}
		out.ByAgent = append(out.ByAgent, attributed)
	}
	return out
}

// turnFor returns the rollup for a turn, creating it on first use.
func (s *ThreadState) turnFor(turn int) *TurnUsage {
	for i := range s.Turns {
		if s.Turns[i].Turn == turn {
			return &s.Turns[i]
		}
	}
	s.Turns = append(s.Turns, TurnUsage{Turn: turn, Agents: map[contracts.AgentID]contracts.TokenUsage{}})
	return &s.Turns[len(s.Turns)-1]
}

// HasPullRequest reports whether the thread already tracks the request at url.
func (s *ThreadState) HasPullRequest(url string) bool {
	for _, ref := range s.PullRequests {
		if ref.URL == url {
			return true
		}
	}
	return false
}

// Clone returns a deep copy so readers cannot race the engine's worker.
func (s *ThreadState) Clone() *ThreadState {
	c := *s
	c.Messages = append([]Message(nil), s.Messages...)
	c.AgentOrder = append([]contracts.AgentID(nil), s.AgentOrder...)
	c.SharedOrder = append([]string(nil), s.SharedOrder...)
	c.Mailbox = append([]MailboxMessage(nil), s.Mailbox...)
	c.ReviewComments = append([]ReviewComment(nil), s.ReviewComments...)
	c.ReviewThreads = make([]ReviewThread, len(s.ReviewThreads))
	for i, t := range s.ReviewThreads {
		c.ReviewThreads[i] = t
		c.ReviewThreads[i].Replies = append([]ReviewReply(nil), t.Replies...)
	}
	c.Questions = append([]Question(nil), s.Questions...)
	c.Subthreads = append([]contracts.ThreadID(nil), s.Subthreads...)
	c.PullRequests = append([]contracts.PullRequestRef(nil), s.PullRequests...)
	c.LeadRuntimes = append([]contracts.LeadRuntime(nil), s.LeadRuntimes...)
	c.Tasks = append([]TaskItem(nil), s.Tasks...)
	c.Turns = make([]TurnUsage, len(s.Turns))
	for i, t := range s.Turns {
		c.Turns[i] = TurnUsage{
			Turn:       t.Turn,
			At:         t.At,
			AgentOrder: append([]contracts.AgentID(nil), t.AgentOrder...),
			Agents:     make(map[contracts.AgentID]contracts.TokenUsage, len(t.Agents)),
			EndedAt:    t.EndedAt,
			Failed:     t.Failed,
		}
		for k, v := range t.Agents {
			c.Turns[i].Agents[k] = v
		}
	}
	c.Steps = make(map[contracts.AgentID][]UsageStep, len(s.Steps))
	for k, v := range s.Steps {
		c.Steps[k] = append([]UsageStep(nil), v...)
	}
	c.Agents = make(map[contracts.AgentID]contracts.Agent, len(s.Agents))
	for k, v := range s.Agents {
		c.Agents[k] = v
	}
	c.Usage = make(map[contracts.AgentID]contracts.AgentUsage, len(s.Usage))
	for k, v := range s.Usage {
		c.Usage[k] = v
	}
	c.Shared = make(map[string]string, len(s.Shared))
	for k, v := range s.Shared {
		c.Shared[k] = v
	}
	return &c
}
