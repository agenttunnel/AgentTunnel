package cli

import (
	"context"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func init() {
	Register(Command{
		Name:    "agents",
		Summary: "List a thread's agents, their roles, and their status",
		Usage:   "agenttunnel agents <thread>",
		Run:     runAgents,
	})
}

func runAgents(ctx context.Context, env *Env, args []string) error {
	var ref string
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "-"):
			return usageError("unknown flag %q (usage: agenttunnel agents <thread>)", arg)
		case ref != "":
			return usageError("agenttunnel agents takes one thread reference")
		default:
			ref = arg
		}
	}
	if ref == "" {
		return usageError("agenttunnel agents requires a thread id, id prefix, or title")
	}
	c, err := connectReadOnly(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()

	row, err := resolveThreadRow(ctx, c, ref)
	if err != nil {
		return err
	}
	return renderAgents(ctx, env, c, row.ID)
}

// renderAgents prints one thread's roster as a table, or as JSON with --json.
func renderAgents(ctx context.Context, env *Env, c cliCaller, threadID string) error {
	agents, err := listAgents(ctx, c, threadID)
	if err != nil {
		return err
	}
	if env.JSON {
		return env.PrintJSON(agents)
	}
	rows := make([][]string, 0, len(agents))
	for _, a := range agents {
		rows = append(rows, []string{
			string(a.ID),
			agentDisplayName(a),
			string(a.Role),
			string(a.Status),
			string(a.Harness),
		})
	}
	renderDockerTable(env.Out, []string{"ID", "NAME", "ROLE", "STATUS", "HARNESS"}, rows)
	return nil
}

// agentDisplayName is the NAME column: the agent's name, or a dash for the
// roster's unnamed lead.
func agentDisplayName(a contracts.Agent) string {
	if strings.TrimSpace(a.Name) == "" {
		return "-"
	}
	return a.Name
}
