package cli

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

func init() {
	Register(Command{
		Name:    "ps",
		Summary: "List threads and what their agents are doing",
		Usage:   "at ps [-a|--all]",
		Run:     runPs,
	})
}

// psOptions is the parsed `at ps` invocation.
type psOptions struct {
	all bool
}

func parsePsArgs(args []string) (psOptions, error) {
	var opts psOptions
	for _, arg := range args {
		switch arg {
		case "-a", "--all":
			opts.all = true
		default:
			return opts, usageError("unknown argument %q (usage: at ps [-a|--all])", arg)
		}
	}
	return opts, nil
}

func runPs(ctx context.Context, env *Env, args []string) error {
	opts, err := parsePsArgs(args)
	if err != nil {
		return err
	}
	c, err := connectReadOnly(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()
	return renderPs(ctx, env, c, opts, time.Now())
}

// psRow is one line of the listing, after projects and staleness are folded in.
type psRow struct {
	thread  threadListRow
	project string
	status  string
}

// collectPs reads the thread list and the project registry and joins them. A
// thread whose project was deleted still lists, under its project id.
func collectPs(ctx context.Context, c cliCaller, opts psOptions) ([]psRow, error) {
	var threads []threadListRow
	if err := c.Call(ctx, "thread.list", nil, &threads); err != nil {
		return nil, err
	}
	var projects []projectsRow
	if err := c.Call(ctx, "project.list", nil, &projects); err != nil {
		return nil, err
	}

	rows := make([]psRow, 0, len(threads))
	for _, thread := range threads {
		status := threadRowStatus(thread)
		if status == "stale" && !opts.all {
			continue
		}
		project := usageDash(thread.ProjectID)
		if p, ok := projectByID(projects, thread.ProjectID); ok {
			project = projectLabel(p)
		}
		rows = append(rows, psRow{thread: thread, project: project, status: status})
	}
	return rows, nil
}

func renderPs(ctx context.Context, env *Env, c cliCaller, opts psOptions, now time.Time) error {
	rows, err := collectPs(ctx, c, opts)
	if err != nil {
		return err
	}
	if env.JSON {
		threads := make([]threadListRow, 0, len(rows))
		for _, row := range rows {
			threads = append(threads, row.thread)
		}
		return env.PrintJSON(threads)
	}
	if len(rows) == 0 {
		if opts.all {
			fmt.Fprintln(env.Out, "no threads yet; start one with `at run <template>`")
			return nil
		}
		fmt.Fprintln(env.Out, "no active threads; pass -a to include stale ones")
		return nil
	}

	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		title := row.thread.Title
		if title == "" {
			title = "(untitled)"
		}
		tableRows = append(tableRows, []string{
			shortThreadID(row.thread.ID),
			title,
			row.project,
			row.status,
			psAgentCount(row.thread.RunningAgents, row.thread.Agents),
			relativeAgo(row.thread.UpdatedAt, now),
		})
	}
	renderDockerTable(env.Out, []string{"ID", "TITLE", "PROJECT", "STATUS", "AGENTS", "UPDATED"}, tableRows)
	return nil
}

// psAgentCount is docker's "running/total" agent column, e.g. "2/4".
func psAgentCount(running, total int) string {
	return strconv.Itoa(running) + "/" + strconv.Itoa(total)
}
