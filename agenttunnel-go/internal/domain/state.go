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
}

// ThreadState is the read model for one thread, derived entirely from its event
// log by the projector. It is never mutated by clients directly.
type ThreadState struct {
	ID          contracts.ThreadID       `json:"id"`
	Exists      bool                     `json:"exists"`
	Title       string                   `json:"title"`
	ProjectID   string                   `json:"projectId,omitempty"`
	Model       contracts.ModelSelection `json:"model"`
	AutoMode    bool                     `json:"autoMode"`
	PeerMailbox bool                     `json:"peerMailbox"`
	Messages    []Message                `json:"messages"`

	// Turn counts user messages: the turn spend currently accrues to.
	Turn  int         `json:"turn"`
	Turns []TurnUsage `json:"turns"`

	Agents     map[contracts.AgentID]contracts.Agent      `json:"agents"`
	AgentOrder []contracts.AgentID                        `json:"agentOrder"`
	Usage      map[contracts.AgentID]contracts.AgentUsage `json:"usage"`
	// Steps is every model step's spend per agent, newest last (bounded).
	Steps   map[contracts.AgentID][]UsageStep `json:"steps"`
	Mailbox []contracts.MailboxPosted         `json:"mailbox"`

	ReviewComments []contracts.ReviewCommentAdded `json:"reviewComments"`

	Shared      map[string]string `json:"shared"`
	SharedOrder []string          `json:"sharedOrder"`

	Version int64 `json:"version"`
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

// Clone returns a deep copy so readers cannot race the engine's worker.
func (s *ThreadState) Clone() *ThreadState {
	c := *s
	c.Messages = append([]Message(nil), s.Messages...)
	c.AgentOrder = append([]contracts.AgentID(nil), s.AgentOrder...)
	c.SharedOrder = append([]string(nil), s.SharedOrder...)
	c.Mailbox = append([]contracts.MailboxPosted(nil), s.Mailbox...)
	c.ReviewComments = append([]contracts.ReviewCommentAdded(nil), s.ReviewComments...)
	c.Turns = make([]TurnUsage, len(s.Turns))
	for i, t := range s.Turns {
		c.Turns[i] = TurnUsage{
			Turn:       t.Turn,
			At:         t.At,
			AgentOrder: append([]contracts.AgentID(nil), t.AgentOrder...),
			Agents:     make(map[contracts.AgentID]contracts.TokenUsage, len(t.Agents)),
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
