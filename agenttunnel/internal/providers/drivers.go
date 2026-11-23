package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

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
		NewGenericDriver(contracts.HarnessCodex, "codex", codexInvocation, CodexTranslator{}).WithImageSupport(ImagesSupported),
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
//
// Claude and codex have no usable listing command (claude rejects
// `--list-models`; codex's list is behind its app-server handshake), so their
// models come only from the user. Probing must never write to stdin: an unknown
// subcommand can be treated as a prompt (opencode prints its usage for an
// unrecognized flag).
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
	if spec.ToolSteering {
		env, err := openCodeSteeringEnv()
		if err != nil {
			return CommandSpec{}, err
		}
		return CommandSpec{Binary: "opencode", Args: args, PromptVia: "arg", Env: env}, nil
	}
	return CommandSpec{Binary: "opencode", Args: args, PromptVia: "arg"}, nil
}

// openCodeSteeringEnv loads the steering plugin by directory through
// OPENCODE_CONFIG_CONTENT, which merges into the user's own config. It is not
// OPENCODE_CONFIG_DIR, which replaces the config directory. Our entry comes
// after the inherited environment, so it wins over any value the user set.
func openCodeSteeringEnv() ([]string, error) {
	plugin, err := writeSteeringGlue("opencode-steering", "index.js", openCodeSteeringPlugin)
	if err != nil {
		return nil, err
	}
	content, err := json.Marshal(map[string]any{"plugins": []string{filepath.Dir(plugin)}})
	if err != nil {
		return nil, err
	}
	return []string{"OPENCODE_CONFIG_CONTENT=" + string(content)}, nil
}

func claudeInvocation(spec SessionSpec) (CommandSpec, error) {
	// Claude only reports token usage and cost in its JSON output, and
	// stream-json requires --verbose in print mode.
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	args = append(args, "--dangerously-skip-permissions")
	if spec.ToolSteering {
		args = append(args, "--settings", claudeSteeringSettings())
	}
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

// toolSteeringCommand runs after each tool call. The daemon answers with the
// steering pending for the thread, which the hook hands to the model beside the
// tool result: a steer lands between tool calls, not after the run ends.
const toolSteeringCommand = `"$AT_TOOL" tool steering`

// claudeSteeringSettings is the --settings JSON that wires the steering hook
// into both tool outcomes: a failed tool call is still a tool call.
func claudeSteeringSettings() string {
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type group struct {
		Hooks []hook `json:"hooks"`
	}
	g := []group{{Hooks: []hook{{Type: "command", Command: toolSteeringCommand, Timeout: 10}}}}
	b, _ := json.Marshal(map[string]any{"hooks": map[string]any{
		"PostToolUse":        g,
		"PostToolUseFailure": g,
	}})
	return string(b)
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
	// --json streams items (messages, commands, file changes) and the turn's
	// usage as JSONL; the plain mode prints prose only. A Codex too old to
	// have --json runs plain: CodexTranslator reads prose lines as text.
	args := []string{"exec"}
	if codexSupportsJSON() {
		args = append(args, "--json")
	}
	if spec.Model != "" {
		args = append(args, "-m", spec.Model)
	}
	if spec.Variant != "" {
		args = append(args, "-c", `model_reasoning_effort="`+spec.Variant+`"`)
	}
	if spec.ToolSteering {
		args = append(args, codexSteeringArgs(codexExecHelpHas("--dangerously-bypass-hook-trust"))...)
	}
	args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	// --image attaches a file the model sees; it is repeatable.
	for _, img := range spec.Images {
		args = append(args, "--image", img.Path)
	}
	args = append(args, spec.Prompt)
	return CommandSpec{Binary: "codex", Args: args, PromptVia: "arg"}, nil
}

// codexExecHelp remembers, per resolved codex binary, the text of its
// `exec --help`. An upgrade in place keeps the old text until the daemon
// restarts.
var codexExecHelp sync.Map

// codexExecHelpHas reports whether `codex exec --help` lists flag. A missing
// binary reads as yes: the spawn then fails with its own error. A probe that
// fails is not remembered, and the answer is no.
func codexExecHelpHas(flag string) bool {
	path, err := exec.LookPath("codex")
	if err != nil {
		return true
	}
	if known, ok := codexExecHelp.Load(path); ok {
		return bytes.Contains(known.([]byte), []byte(flag))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "exec", "--help").CombinedOutput()
	if err != nil {
		return false
	}
	codexExecHelp.Store(path, out)
	return bytes.Contains(out, []byte(flag))
}

// codexSupportsJSON is whether this codex's exec takes --json. See codexExecHelpHas.
func codexSupportsJSON() bool { return codexExecHelpHas("--json") }

// codexSteeringArgs wires the steering hook for codex exec. The hook is a
// config override, so no file lands in the user's codex home or project. Hooks
// from a non-interactive run must be trusted, and exec has no review step, so
// the bypass flag is passed when this codex has it. The hook command is the
// daemon's own, never user-supplied.
func codexSteeringArgs(trustFlag bool) []string {
	args := []string{"-c", `hooks.PostToolUse=[{hooks=[{type="command",command='` + toolSteeringCommand + `',timeout=10}]}]`}
	if trustFlag {
		args = append(args, "--dangerously-bypass-hook-trust")
	}
	return args
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
	if spec.ToolSteering {
		// -e loads the glue as an extension; auto-discovered extensions are
		// left as the user configured them.
		glue, err := writeSteeringGlue("pi-steering", "extension.js", piSteeringExtension)
		if err != nil {
			return CommandSpec{}, err
		}
		args = append(args, "-e", glue)
	}
	return CommandSpec{Binary: "pi", Args: args, PromptVia: "arg"}, nil
}

func commandCodeInvocation(spec SessionSpec) (CommandSpec, error) {
	// --print takes its query as the next argument, so the prompt must follow
	// it directly; a trailing positional is not read as the query. Headless
	// mode blocks writes and shell commands unless --yolo is set.
	args := []string{"-p", spec.Prompt, "--yolo", "--output-format", "json"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.ToolSteering {
		// --mod loads a file for this session only; print mode loads --mod mods.
		mod, err := writeSteeringGlue("commandcode-steering", "steering.ts", commandCodeSteeringMod)
		if err != nil {
			return CommandSpec{}, err
		}
		args = append(args, "--mod", mod)
	}
	return CommandSpec{Binary: "commandcode", Args: args, PromptVia: "arg"}, nil
}
