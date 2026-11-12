package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func init() {
	Register(Command{
		Name:    "stop",
		Summary: "Stop a thread's running turn, or its agents",
		Usage:   "agenttunnel stop <thread> [--agents]",
		Run:     runStop,
	})
}

// stopOptions is the parsed `agenttunnel stop` invocation.
type stopOptions struct {
	ref    string
	agents bool
}

func parseStopArgs(args []string) (stopOptions, error) {
	var opts stopOptions
	for _, arg := range args {
		switch {
		case arg == "--agents":
			opts.agents = true
		case strings.HasPrefix(arg, "-"):
			return opts, usageError("unknown flag %q (usage: agenttunnel stop <thread> [--agents])", arg)
		case opts.ref != "":
			return opts, usageError("agenttunnel stop takes one thread reference")
		default:
			opts.ref = arg
		}
	}
	if opts.ref == "" {
		return opts, usageError("agenttunnel stop requires a thread id, id prefix, or title")
	}
	return opts, nil
}

func runStop(ctx context.Context, env *Env, args []string) error {
	opts, err := parseStopArgs(args)
	if err != nil {
		return err
	}
	c, err := connectReadOnly(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()

	// Resolve first so a title or prefix is reported back as the real id, and
	// an ambiguous ref stops nothing.
	row, err := resolveThreadRow(ctx, c, opts.ref)
	if err != nil {
		return err
	}

	var stopped struct {
		Stopped bool `json:"stopped"`
	}
	if err := c.Call(ctx, "thread.turn.stop", map[string]string{"threadId": row.ID}, &stopped); err != nil {
		return err
	}
	if !opts.agents {
		printTurnStop(env, row.ID, stopped.Stopped)
		return nil
	}
	return stopAgents(ctx, env, c, row.ID)
}

func printTurnStop(env *Env, threadID string, stopped bool) {
	if env.JSON {
		_ = env.PrintJSON(map[string]any{"threadId": threadID, "stopped": stopped})
		return
	}
	if stopped {
		fmt.Fprintf(env.Out, "stopped turn on %s\n", threadID)
		return
	}
	fmt.Fprintf(env.Out, "%s has no running turn\n", threadID)
}

// stopAgents stops every agent that is still running. It reports both what it
// stopped and what was already terminal, so "nothing happened" is never
// ambiguous.
func stopAgents(ctx context.Context, env *Env, c cliCaller, threadID string) error {
	statuses, err := fetchAgentStatuses(ctx, c, threadID)
	if err != nil {
		return err
	}
	var running []string
	for id, status := range statuses {
		if status == contracts.AgentRunning || status == contracts.AgentPending {
			running = append(running, id)
		}
	}

	stopped := make([]string, 0, len(running))
	for _, id := range running {
		var res struct {
			Stopped bool `json:"stopped"`
		}
		if err := c.Call(ctx, "agents.stop", map[string]string{"agentId": id}, &res); err != nil {
			return fmt.Errorf("agents.stop %s: %w", id, err)
		}
		if res.Stopped {
			stopped = append(stopped, id)
		}
	}

	if env.JSON {
		if stopped == nil {
			stopped = []string{}
		}
		return env.PrintJSON(map[string]any{"threadId": threadID, "stopped": stopped})
	}
	switch {
	case len(running) == 0:
		fmt.Fprintf(env.Out, "%s has no running agents\n", threadID)
	case len(stopped) == 0:
		fmt.Fprintf(env.Out, "no agents stopped on %s\n", threadID)
	default:
		fmt.Fprintf(env.Out, "stopped %d agent(s) on %s: %s\n", len(stopped), threadID, strings.Join(stopped, ", "))
	}
	return nil
}
