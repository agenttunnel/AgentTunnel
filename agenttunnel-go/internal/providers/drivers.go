package providers

import "github.com/agenttunnel/agenttunnel-go/internal/contracts"

// BuiltinDrivers returns the drivers for the harnesses AgentTunnel ships.
//
// Each invocation mirrors upstream AgentTunnel's argv shape: the prompt is the
// final argument, never written to stdin, and the harness is run in its
// non-interactive mode so it cannot block on an approval prompt. (Upstream gates
// the permission flags behind its "dangerously skip permissions" setting; the
// workflow runner has no approval plumbing yet, so they are always on here.)
func BuiltinDrivers() []Driver {
	return []Driver{
		NewGenericDriver(contracts.HarnessOpenCode, "opencode", openCodeInvocation, JSONLineTranslator{}),
		NewGenericDriver(contracts.HarnessClaude, "claude", claudeInvocation, JSONLineTranslator{}),
		NewGenericDriver(contracts.HarnessCodex, "codex", codexInvocation, JSONLineTranslator{}),
		NewGenericDriver(contracts.HarnessCursor, "cursor-agent", cursorInvocation, JSONLineTranslator{}),
		NewGenericDriver(contracts.HarnessGrok, "grok", grokInvocation, JSONLineTranslator{}),
		NewGenericDriver(contracts.HarnessPi, "pi", piInvocation, JSONLineTranslator{}),
		NewGenericDriver(contracts.HarnessCommandCode, "command-code", commandCodeInvocation, JSONLineTranslator{}),
	}
}

// BuiltinRegistry returns a registry populated with the shipped drivers and the
// model discovery they support.
//
// Only harnesses with a real, non-interactive listing command are probed:
//   - opencode:            `opencode models`
//   - pi:                  `pi --list-models` (a fixed-width table, on stderr)
//   - command-code / cmd:  `--list-models` (provider/model slugs)
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
	// Command Code's CLI is `command-code`, also installed as `cmd`; both print
	// provider/model slugs.
	r.RegisterDiscoverer(contracts.HarnessCommandCode, NewChainDiscoverer(
		NewCommandDiscoverer("command-code", "--list-models"),
		NewCommandDiscoverer("cmd", "--list-models"),
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
	args = append(args, spec.Prompt)
	return CommandSpec{Binary: "claude", Args: args, PromptVia: "arg"}, nil
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
	args = append(args, spec.Prompt)
	return CommandSpec{Binary: "codex", Args: args, PromptVia: "arg"}, nil
}

func piInvocation(spec SessionSpec) (CommandSpec, error) {
	args := []string{"--mode", "json", "-p", spec.Prompt, "--no-session"}
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
	args := []string{"--print"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	args = append(args, spec.Prompt)
	return CommandSpec{Binary: "command-code", Args: args, PromptVia: "arg"}, nil
}
