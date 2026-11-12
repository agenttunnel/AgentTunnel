package contracts

import "time"

type (
	AgentID    string
	ThreadID   string
	TaskID     string
	PipelineID string
)

// AgentStatus is the lifecycle state of a single agent instance.
type AgentStatus string

const (
	AgentPending   AgentStatus = "pending"
	AgentRunning   AgentStatus = "running"
	AgentSucceeded AgentStatus = "succeeded"
	AgentFailed    AgentStatus = "failed"
	AgentCancelled AgentStatus = "cancelled"
)

// Terminal reports whether the status is final.
func (s AgentStatus) Terminal() bool {
	switch s {
	case AgentSucceeded, AgentFailed, AgentCancelled:
		return true
	default:
		return false
	}
}

// Agent is a single running (or completed) agent instance. An agent is either the
// top-level orchestrator or a subagent dispatched by it.
type Agent struct {
	ID            AgentID     `json:"id"`
	ParentAgentID AgentID     `json:"parentAgentId,omitempty"`
	Name          string      `json:"name,omitempty"`
	ThreadID      ThreadID    `json:"threadId"`
	Role          RoleID      `json:"role"`
	Harness       Harness     `json:"harness"`
	Model         string      `json:"model"`
	PipelineID    PipelineID  `json:"pipelineId,omitempty"`
	Task          string      `json:"task"`
	DependsOn     []string    `json:"dependsOn,omitempty"`
	Status        AgentStatus `json:"status"`
	// Reason says why the latest status change happened, e.g. why it failed.
	Reason    string     `json:"reason,omitempty"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
}

// IsOrchestrator reports whether this agent is the thread's top-level team lead.
func (a Agent) IsOrchestrator() bool { return a.ParentAgentID == "" }
