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
	Tool    *ToolCall `json:"tool,omitempty"`
	At      time.Time `json:"at"`
}

// ToolStatus is where a structured tool call stands.
type ToolStatus string

const (
	ToolRunning ToolStatus = "running"
	ToolDone    ToolStatus = "done"
	ToolFailed  ToolStatus = "failed"
)

// ToolCall is the structured side of a tool line, set when the provider stream
// exposes it. Text stays the full plain rendering, so a client that ignores
// Tool loses nothing. A call and its result are separate lines sharing ID: the
// call (kind "tool") says what ran, the result (kind "raw" or "error") carries
// the final status with the output in Text. Clients fold them by ID; a harness
// that reports a call only once finished sends a single "tool" line.
type ToolCall struct {
	ID         string     `json:"id"`
	Name       string     `json:"name,omitempty"`
	Command    string     `json:"command,omitempty"`
	Path       string     `json:"path,omitempty"`
	Status     ToolStatus `json:"status"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	DurationMs int64      `json:"durationMs,omitempty"`
	// Diff is an edit as unified-diff body lines ("-", "+", " ", and "@@"
	// hunk headers), capped by the adapter.
	Diff string `json:"diff,omitempty"`
}
