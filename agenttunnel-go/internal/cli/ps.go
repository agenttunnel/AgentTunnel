package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func init() {
	Register(Command{
		Name:    "ps",
		Summary: "List threads and what their agents are doing",
		Usage:   "agenttunnel ps [-a|--all]",
		Run:     runPs,
	})
}

// psOptions is the parsed `agenttunnel ps` invocation.
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
			return opts, usageError("unknown argument %q (usage: agenttunnel ps [-a|--all])", arg)
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
	// depth is how many subthread levels below a listed root this row sits.
	depth int
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

// nestPsRows orders rows so every subthread follows its parent, indented one
// level deeper. A subthread whose parent is not listed (hidden as stale, or
// deleted) stays at the root. Siblings keep the recency order they arrived in,
// and a seen-set keeps a malformed parent cycle from looping or dropping rows.
func nestPsRows(rows []psRow) []psRow {
	listed := make(map[string]bool, len(rows))
	for _, row := range rows {
		listed[row.thread.ID] = true
	}
	children := make(map[string][]int)
	var roots []int
	for i, row := range rows {
		parent := row.thread.ParentThreadID
		if parent != "" && listed[parent] && parent != row.thread.ID {
			children[parent] = append(children[parent], i)
		} else {
			roots = append(roots, i)
		}
	}

	out := make([]psRow, 0, len(rows))
	seen := make([]bool, len(rows))
	var walk func(i, depth int)
	walk = func(i, depth int) {
		if seen[i] {
			return
		}
		seen[i] = true
		row := rows[i]
		row.depth = depth
		out = append(out, row)
		for _, child := range children[row.thread.ID] {
			walk(child, depth+1)
		}
	}
	for _, i := range roots {
		walk(i, 0)
	}
	// Rows only reachable through a cycle never became roots; list them flat
	// rather than lose them.
	for i := range rows {
		walk(i, 0)
	}
	return out
}

func renderPs(ctx context.Context, env *Env, c cliCaller, opts psOptions, now time.Time) error {
	rows, err := collectPs(ctx, c, opts)
	if err != nil {
		return err
	}
	rows = nestPsRows(rows)
	if env.JSON {
		threads := make([]threadListRow, 0, len(rows))
		for _, row := range rows {
			threads = append(threads, row.thread)
		}
		return env.PrintJSON(threads)
	}
	if len(rows) == 0 {
		if opts.all {
			fmt.Fprintln(env.Out, "no threads yet; start one with `agenttunnel run <template>`")
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
		parent := "-"
		if row.thread.ParentThreadID != "" {
			parent = shortThreadID(string(row.thread.ParentThreadID))
		}
		if row.depth > 0 {
			title = strings.Repeat("  ", row.depth-1) + "└ " + title
		}
		tableRows = append(tableRows, []string{
			shortThreadID(row.thread.ID),
			title,
			parent,
			row.project,
			row.status,
			psAgentCount(row.thread.RunningAgents, row.thread.Agents),
			relativeAgo(row.thread.UpdatedAt, now),
		})
	}
	renderDockerTable(env.Out, []string{"ID", "TITLE", "PARENT", "PROJECT", "STATUS", "AGENTS", "UPDATED"}, tableRows)
	return nil
}

// psAgentCount is docker's "running/total" agent column, e.g. "2/4".
func psAgentCount(running, total int) string {
	return strconv.Itoa(running) + "/" + strconv.Itoa(total)
}
