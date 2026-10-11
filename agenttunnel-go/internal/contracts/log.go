package contracts

import "time"

// LogKind classifies an agent log entry for rendering.
type LogKind string

const (
	LogText      LogKind = "text"
	LogReasoning LogKind = "reasoning"
	LogTool      LogKind = "tool"
	LogUsage     LogKind = "usage"
	LogRaw       LogKind = "raw"
	LogError     LogKind = "error"
)

// LogEntry is one line of an agent's output stream.
type LogEntry struct {
	AgentID AgentID   `json:"agentId"`
	Kind    LogKind   `json:"kind"`
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
}
