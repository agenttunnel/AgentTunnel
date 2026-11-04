package providers

import (
	"context"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Event is a normalized stream event produced by an adapter, independent of the
// harness that produced it. Every provider is translated into this shape.
type Event struct {
	Kind     contracts.LogKind     `json:"kind"`
	Text     string                `json:"text,omitempty"`
	ToolName string                `json:"toolName,omitempty"`
	Usage    *contracts.TokenUsage `json:"usage,omitempty"`
}

// SessionSpec describes one agent run handed to an adapter.
type SessionSpec struct {
	AgentID  contracts.AgentID  `json:"agentId"`
	ThreadID contracts.ThreadID `json:"threadId"`
	Role     contracts.RoleID   `json:"role"`
	Harness  contracts.Harness  `json:"harness"`
	Model    string             `json:"model,omitempty"`
	Variant  string             `json:"variant,omitempty"`
	Prompt   string             `json:"prompt"`
	Workdir  string             `json:"workdir,omitempty"`
	Env      []string           `json:"env,omitempty"`
}

// Adapter is the per-provider runtime contract. Implementations translate a
// provider CLI's native protocol into normalized Events.
type Adapter interface {
	Harness() contracts.Harness
	Start(ctx context.Context, spec SessionSpec) error
	Send(ctx context.Context, message string) error
	Stream(ctx context.Context) (<-chan Event, error)
	Interrupt(ctx context.Context) error
	Stop(ctx context.Context) error
	// Wait blocks until the session process exits and returns its error (nil on
	// success). Call it after the Stream channel is drained.
	Wait(ctx context.Context) error
}

// CommandSpec is a concrete process invocation for one harness turn.
type CommandSpec struct {
	Binary    string
	Args      []string
	PromptVia string // "stdin" or "arg"
}

// Translator maps a raw stdout line into zero or more normalized events.
type Translator interface {
	Translate(line string) []Event
}

// TranslatorFunc adapts a function to the Translator interface.
type TranslatorFunc func(line string) []Event

func (f TranslatorFunc) Translate(line string) []Event { return f(line) }

// Driver builds adapters for exactly one harness. Adding a provider is a matter
// of implementing this interface — nothing above the adapter boundary changes.
type Driver interface {
	Harness() contracts.Harness
	Binary() string
	BuildInvocation(spec SessionSpec) (CommandSpec, Translator, error)
}
