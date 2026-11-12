package providers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// BuiltinDrivers returns the drivers for the harnesses AgentTunnel ships.
//
// Each invocation mirrors upstream AgentTunnel's argv shape: the prompt is passed
// as an argument (never written to stdin), and the harness is run in its
// non-interactive mode so it cannot block on an approval prompt. (Upstream gates
// the permission flags behind its "dangerously skip permissions" setting; the
// workflow runner has no approval plumbing yet, so they are always on here.)
func BuiltinDrivers() []Driver {
	return []Driver{
		NewGenericDriver(contracts.HarnessOpenCode, "opencode", openCodeInvocation, JSONLineTranslator{}).WithImageSupport(ImagesSupported),
		NewGenericDriver(contracts.HarnessClaude, "claude", claudeInvocation, JSONLineTranslator{}).WithImageSupport(ImagesSupported),
		NewGenericDriver(contracts.HarnessCodex, "codex", codexInvocation, JSONLineTranslator{}).WithImageSupport(ImagesSupported),
		NewGenericDriver(contracts.HarnessCursor, "cursor-agent", cursorInvocation, JSONLineTranslator{}).WithImageSupport(ImagesUnsupported),
		NewGenericDriver(contracts.HarnessGrok, "grok", grokInvocation, JSONLineTranslator{}),
		NewGenericDriver(contracts.HarnessPi, "pi", piInvocation, JSONLineTranslator{}).WithImageSupport(ImagesSupported),
		NewGenericDriver(contracts.HarnessCommandCode, "commandcode", commandCodeInvocation, CommandCodeTranslator{}).WithImageSupport(ImagesUnsupported),
	}
}

// BuiltinRegistry returns a registry populated with the shipped drivers and the
// model discovery they support.
//
// Only harnesses with a real, non-interactive listing command are probed:
//   - opencode:            `opencode models`
//   - pi:                  `pi --list-models` (a fixed-width table, on stderr)
//   - commandcode / cmd:   `--list-models` (provider/model slugs)
//   - cursor-agent:        `--list-models` ("<slug> - <description>" rows)
//
// The rest fall back to the seed catalog: claude rejects `--list-models`, and
// codex's list is behind its app-server handshake while grok reports over ACP.
// Probing must never write to stdin: an unknown subcommand can be treated as a
// prompt (opencode prints its usage for an unrecognized flag).
func BuiltinRegistry() *Registry {
	r := NewRegistry(BuiltinDrivers()...)
	// `opencode models` prints one provider/model slug per line.
	r.RegisterDiscoverer(contracts.HarnessOpenCode, NewCommandDiscoverer("opencode", "models"))
	// `pi --list-models` prints a fixed-width table, on stderr.
	r.RegisterDiscoverer(contracts.HarnessPi,
		NewCommandDiscoverer("pi", "--list-models").WithParser(ParsePiModelListTable))
	// Command Code's CLI is installed as `commandcode` and `cmd`; both print
	// provider/model slugs and bare short model names from `--list-models`.
	r.RegisterDiscoverer(contracts.HarnessCommandCode, NewChainDiscoverer(
		NewCommandDiscoverer("commandcode", "--list-models").WithParser(ParseCommandCodeModels),
		NewCommandDiscoverer("cmd", "--list-models").WithParser(ParseCommandCodeModels),
	))
	// `cursor-agent --list-models` prints "<slug> - <description>" rows.
	r.RegisterDiscoverer(contracts.HarnessCursor,
		NewCommandDiscoverer("cursor-agent", "--list-models").WithParser(ParseCursorModelList))
	return r
}

func openCodeInvocation(spec SessionSpec) (CommandSpec, error) {
	args := []string{"run"}
	if spec.Model != "" {
		args = append(args, "-m", spec.Model)
	}
	args = append(args, "--auto", "--format", "json", "--thinking")
	if spec.Variant != "" {
		args = append(args, "--variant", spec.Variant)
	}
	// --file attaches an image by path; it is repeatable.
	for _, img := range spec.Images {
		args = append(args, "--file", img.Path)
	}
	args = append(args, spec.Prompt)
	return CommandSpec{Binary: "opencode", Args: args, PromptVia: "arg"}, nil
}

func claudeInvocation(spec SessionSpec) (CommandSpec, error) {
	// Claude only reports token usage and cost in its JSON output, and
	// stream-json requires --verbose in print mode.
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	args = append(args, "--dangerously-skip-permissions")
	if len(spec.Images) == 0 {
		args = append(args, spec.Prompt)
		return CommandSpec{Binary: "claude", Args: args, PromptVia: "arg"}, nil
	}
	// Print mode has no image flag: images ride a stream-json user message
	// on stdin, as base64 content blocks alongside the text.
	args = append(args, "--input-format", "stream-json")
	frames, err := claudeInputFrames(spec)
	if err != nil {
		return CommandSpec{}, err
	}
	return CommandSpec{Binary: "claude", Args: args, PromptVia: "arg", StdinJSONFrames: frames}, nil
}

// claudeInputFrames builds the one user-message frame a stream-json input
// turn needs: every image as a base64 block, then the prompt text. The child
// reads until EOF and answers.
func claudeInputFrames(spec SessionSpec) ([]string, error) {
	type source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	type block struct {
		Type   string  `json:"type"`
		Text   string  `json:"text,omitempty"`
		Source *source `json:"source,omitempty"`
	}
	var blocks []block
	for _, img := range spec.Images {
		data, err := os.ReadFile(img.Path)
		if err != nil {
			return nil, fmt.Errorf("read image %s: %w", img.Path, err)
		}
		blocks = append(blocks, block{Type: "image", Source: &source{
			Type: "base64", MediaType: img.Mime, Data: base64.StdEncoding.EncodeToString(data),
		}})
	}
	blocks = append(blocks, block{Type: "text", Text: spec.Prompt})
	frame := struct {
		Type    string `json:"type"`
		Message struct {
			Role    string  `json:"role"`
			Content []block `json:"content"`
		} `json:"message"`
	}{Type: "user"}
	frame.Message.Role = "user"
	frame.Message.Content = blocks
	encoded, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	return []string{string(encoded)}, nil
}

func codexInvocation(spec SessionSpec) (CommandSpec, error) {
	args := []string{"exec"}
	if spec.Model != "" {
		args = append(args, "-m", spec.Model)
	}
	if spec.Variant != "" {
		args = append(args, "-c", `model_reasoning="`+spec.Variant+`"`)
	}
	args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	// --image attaches a file the model sees; it is repeatable.
	for _, img := range spec.Images {
		args = append(args, "--image", img.Path)
	}
	args = append(args, spec.Prompt)
	return CommandSpec{Binary: "codex", Args: args, PromptVia: "arg"}, nil
}

func piInvocation(spec SessionSpec) (CommandSpec, error) {
	// Pi has no image flag; @path mentions in the prompt are its attachment
	// mechanism ("pi @image.png \"what is this?\"" per its own examples).
	prompt := spec.Prompt
	for _, img := range spec.Images {
		prompt += " @" + img.Path
	}
	args := []string{"--mode", "json", "-p", prompt, "--no-session"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Variant != "" {
		args = append(args, "--thinking", spec.Variant)
	}
	return CommandSpec{Binary: "pi", Args: args, PromptVia: "arg"}, nil
}

func cursorInvocation(spec SessionSpec) (CommandSpec, error) {
	args := []string{"--print"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	args = append(args, spec.Prompt)
	return CommandSpec{Binary: "cursor-agent", Args: args, PromptVia: "arg"}, nil
}

func grokInvocation(spec SessionSpec) (CommandSpec, error) {
	args := []string{"-p"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	args = append(args, spec.Prompt)
	return CommandSpec{Binary: "grok", Args: args, PromptVia: "arg"}, nil
}

func commandCodeInvocation(spec SessionSpec) (CommandSpec, error) {
	// --print takes its query as the next argument, so the prompt must follow
	// it directly; a trailing positional is not read as the query. Headless
	// mode blocks writes and shell commands unless --yolo is set.
	args := []string{"-p", spec.Prompt, "--yolo", "--output-format", "json"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	return CommandSpec{Binary: "commandcode", Args: args, PromptVia: "arg"}, nil
}
