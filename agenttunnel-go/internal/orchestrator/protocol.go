package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// ToolPrefix marks an out-of-band action emitted on a log line. The orchestrator
// and its subagents use it to dispatch work and report findings without wiring a
// separate channel.
const ToolPrefix = "__AGENTTUNNEL_TOOL__"

// ToolCall is a parsed action. It is a superset of every action the workflow
// understands; only the fields relevant to a given Name are populated.
type ToolCall struct {
	Name      string   `json:"name"`
	TaskID    string   `json:"taskId,omitempty"`
	Task      string   `json:"task,omitempty"`
	Role      string   `json:"role,omitempty"`
	Harness   string   `json:"harness,omitempty"`
	Model     string   `json:"model,omitempty"`
	Lane      string   `json:"lane,omitempty"`
	DependsOn []string `json:"dependsOn,omitempty"`
	Key       string   `json:"key,omitempty"`
	Value     string   `json:"value,omitempty"`
	To        string   `json:"to,omitempty"`
	Body      string   `json:"body,omitempty"`
	Verdict   string   `json:"verdict,omitempty"`
}

// ParseToolLine extracts a tool call from a log line, tolerating surrounding
// decoration by scanning for the prefix.
func ParseToolLine(line string) (*ToolCall, bool) {
	idx := strings.Index(line, ToolPrefix)
	if idx < 0 {
		return nil, false
	}
	payload := strings.TrimSpace(line[idx+len(ToolPrefix):])
	if payload == "" {
		return nil, false
	}
	var call ToolCall
	if err := json.Unmarshal([]byte(payload), &call); err != nil {
		return nil, false
	}
	if call.Name == "" {
		return nil, false
	}
	return &call, true
}

// Dispatches filters the calls down to dispatch_task actions.
func Dispatches(calls []ToolCall) []ToolCall {
	var out []ToolCall
	for _, c := range calls {
		if c.Name == "dispatch_task" {
			out = append(out, c)
		}
	}
	return out
}

// ToDispatchTasks converts dispatch_task calls into command dispatch payloads.
// The task id is used as both the dependency name and the agent id suffix.
func ToDispatchTasks(parentID contracts.AgentID, calls []ToolCall) []contracts.DispatchTask {
	var out []contracts.DispatchTask
	n := 0
	for _, c := range calls {
		if c.Name != "dispatch_task" {
			continue
		}
		n++
		name := c.TaskID
		if name == "" {
			name = fmt.Sprintf("task-%d", n)
		}
		out = append(out, contracts.DispatchTask{
			AgentID:       contracts.AgentID("agent-" + name),
			ParentAgentID: parentID,
			Name:          name,
			Role:          contracts.RoleID(c.Role),
			Task:          c.Task,
			Harness:       contracts.Harness(c.Harness),
			Model:         c.Model,
			Pipeline:      contracts.PipelineID(c.Lane),
			DependsOn:     c.DependsOn,
		})
	}
	return out
}
