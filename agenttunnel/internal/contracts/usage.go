package contracts

// TokenUsage is a normalized token/cost/tool rollup. It is the common currency
// for per-agent, per-step, per-turn, and thread-wide accounting.
type TokenUsage struct {
	InputTokens     int64   `json:"inputTokens"`
	OutputTokens    int64   `json:"outputTokens"`
	CachedTokens    int64   `json:"cachedTokens"`
	ReasoningTokens int64   `json:"reasoningTokens,omitempty"`
	CostUSD         float64 `json:"costUsd"`
	ToolUses        int64   `json:"toolUses"`
}

// Total is the total token count (input + output).
func (u TokenUsage) Total() int64 { return u.InputTokens + u.OutputTokens }

// Processed is every token the provider touched, including cache reads.
func (u TokenUsage) Processed() int64 {
	return u.InputTokens + u.OutputTokens + u.CachedTokens
}

// Add returns the element-wise sum of two usage values.
func (u TokenUsage) Add(o TokenUsage) TokenUsage {
	return TokenUsage{
		InputTokens:     u.InputTokens + o.InputTokens,
		OutputTokens:    u.OutputTokens + o.OutputTokens,
		CachedTokens:    u.CachedTokens + o.CachedTokens,
		ReasoningTokens: u.ReasoningTokens + o.ReasoningTokens,
		CostUSD:         u.CostUSD + o.CostUSD,
		ToolUses:        u.ToolUses + o.ToolUses,
	}
}

// AgentUsage attributes a usage rollup to a specific agent (and its role).
type AgentUsage struct {
	AgentID AgentID    `json:"agentId"`
	Role    RoleID     `json:"role"`
	Usage   TokenUsage `json:"usage"`
}

// ThreadUsage is the collective rollup for a thread. Orchestrator and subagent
// spend are reported separately so a user can see where tokens actually go.
type ThreadUsage struct {
	Orchestrator TokenUsage   `json:"orchestrator"`
	Subagents    TokenUsage   `json:"subagents"`
	ByAgent      []AgentUsage `json:"byAgent"`
}

// Total is orchestrator + subagent usage combined.
func (t ThreadUsage) Total() TokenUsage { return t.Orchestrator.Add(t.Subagents) }
