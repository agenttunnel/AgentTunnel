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

// ImageInput is one image a harness should see, as a process-readable file
// in the attachment store. The file is passed by reference; only harnesses
// that read stdin frames (claude) see the bytes inline.
type ImageInput struct {
	Path string `json:"path"`
	Mime string `json:"mime"`
	// Name is the original filename; prompts reference it even when the
	// harness cannot view the image.
	Name string `json:"name,omitempty"`
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
	// Images are the turn's attachments. Each harness consumes them through
	// its own mechanism; a harness with none ignores them (the orchestrator
	// degrades to a path note in the prompt).
	Images  []ImageInput `json:"images,omitempty"`
	Workdir string       `json:"workdir,omitempty"`
	Env     []string     `json:"env,omitempty"`
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
	// StdinJSONFrames are newline-delimited JSON frames written to the
	// child's stdin right after start, then closed. claude uses this to feed
	// a stream-json user message (images as base64 blocks plus the prompt).
	StdinJSONFrames []string
}

// ImageSupport is the tri-state verdict on a driver's image input.
type ImageSupport int

const (
	// ImagesUnknown means no evidence either way: the turn is accepted and
	// the images degrade to a path note in the prompt.
	ImagesUnknown ImageSupport = iota
	// ImagesSupported means the harness ingests images through its own flag
	// or input format.
	ImagesSupported
	// ImagesUnsupported means the harness has no image input in its
	// non-interactive mode.
	ImagesUnsupported
)

// String renders the verdict as harness.list reports it.
func (s ImageSupport) String() string {
	switch s {
	case ImagesSupported:
		return "supported"
	case ImagesUnsupported:
		return "unsupported"
	default:
		return "unknown"
	}
}

// ImageCapable is the optional capability a driver implements to declare how
// it takes image input. Drivers without it read as ImagesUnknown, which
// degrades to a prompt note rather than refusing the turn.
type ImageCapable interface {
	ImageSupport() ImageSupport
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
