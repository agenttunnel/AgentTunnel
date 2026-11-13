package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
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
	Brief     string   `json:"brief,omitempty"`
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
// The task id is used as both the dependency name and the agent id suffix; an
// unnamed call gets a docker-style name that `taken` and earlier calls in this
// batch do not use.
func ToDispatchTasks(parentID contracts.AgentID, calls []ToolCall, taken func(string) bool) []contracts.DispatchTask {
	var out []contracts.DispatchTask
	minted := map[string]bool{}
	n := 0
	for _, c := range calls {
		if c.Name != "dispatch_task" {
			continue
		}
		n++
		name := c.TaskID
		if name == "" {
			name = generateAgentName(fmt.Sprintf("dispatch-%d", n), func(s string) bool {
				return minted[s] || taken(s)
			})
		}
		minted[name] = true
		out = append(out, contracts.DispatchTask{
			AgentID:       contracts.AgentID("agent-" + name),
			ParentAgentID: parentID,
			Name:          name,
			Role:          contracts.RoleID(c.Role),
			Task:          c.Task,
			Brief:         c.Brief,
			Harness:       contracts.Harness(c.Harness),
			Model:         c.Model,
			Pipeline:      contracts.PipelineID(c.Lane),
			DependsOn:     c.DependsOn,
		})
	}
	return out
}

// frameCall converts a protocol action frame into the equivalent inbox call,
// so a harness that speaks frames gets the same routing as one that prints
// command lines: a message forwards through the mailbox, a subagent's dispatch
// or spawn becomes a gated spawn request, and the lead's spawn or decision runs
// directly. Malformed frames (a message without a body, an approve without a
// name) and unknown names do not convert and stay ordinary output.
func frameCall(c *ToolCall) (toolbox.Call, bool) {
	call := toolbox.Call{
		Name:      c.TaskID,
		Role:      contracts.RoleID(c.Role),
		Task:      c.Task,
		Brief:     c.Brief,
		Harness:   c.Harness,
		Model:     c.Model,
		Lane:      c.Lane,
		DependsOn: c.DependsOn,
		To:        c.To,
		Text:      c.Body,
	}
	switch c.Name {
	case "message":
		call.Kind = toolbox.KindMessage
		if strings.TrimSpace(c.To) == "" || strings.TrimSpace(c.Body) == "" {
			return toolbox.Call{}, false
		}
	case "spawn":
		call.Kind = toolbox.KindSpawn
		if strings.TrimSpace(c.Task) == "" {
			return toolbox.Call{}, false
		}
	case "dispatch_task":
		call.Kind = toolbox.KindDispatch
		if strings.TrimSpace(c.Task) == "" {
			return toolbox.Call{}, false
		}
	case "approve":
		call.Kind = toolbox.KindApprove
		if strings.TrimSpace(c.TaskID) == "" {
			return toolbox.Call{}, false
		}
	case "reject":
		call.Kind = toolbox.KindReject
		if strings.TrimSpace(c.TaskID) == "" {
			return toolbox.Call{}, false
		}
	default:
		return toolbox.Call{}, false
	}
	return call, true
}
