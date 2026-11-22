package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/template"
)

func init() {
	Register(Command{
		Name:    "run",
		Summary: "Start a job from a template as a background thread",
		Usage:   runCommandUsage,
		Run:     runJob,
	})
}

const runCommandUsage = "agenttunnel run <file|name> [--follow] [--no-follow] [--wait [--timeout D] --json]"

// defaultRunWaitTimeout bounds `run --wait` so a stuck job cannot hang a CI
// step forever. --timeout 0 lifts the bound.
const defaultRunWaitTimeout = 30 * time.Minute

// runStartBound is how long `run --wait` tolerates a thread that shows no turn
// and no agent. The daemon accepts a detached start once the turn slot is
// claimed, then cuts the worktree and dispatches the lead in the background, so
// a healthy start can take a while on a large repo. A short fixed grace would
// fail a job that then runs fine. Only a start the daemon never records (a
// refused worktree or dispatch, logged server-side) reaches this bound.
const runStartBound = 5 * time.Minute

// runOptions is the parsed `agenttunnel run` invocation.
type runOptions struct {
	ref    string
	follow bool
	// wait starts the turn and blocks until it ends, then prints one JSON result.
	wait    bool
	timeout time.Duration
}

func parseRunArgs(args []string) (runOptions, error) {
	opts := runOptions{timeout: defaultRunWaitTimeout}
	timeoutSet := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		flag, _, _ := strings.Cut(arg, "=")
		switch {
		case arg == "--follow" || arg == "-f":
			opts.follow = true
		case arg == "--no-follow":
			opts.follow = false
		case arg == "--wait":
			opts.wait = true
		case flag == "--timeout":
			value, next, err := usageFlagValue(args, i, "--timeout")
			if err != nil {
				return opts, err
			}
			d, convErr := time.ParseDuration(strings.TrimSpace(value))
			if convErr != nil || d < 0 {
				return opts, usageError("--timeout wants a duration like 30m or 90s (0 = no limit), got %q", value)
			}
			opts.timeout, i, timeoutSet = d, next, true
		case strings.HasPrefix(arg, "-"):
			return opts, usageError("unknown flag %q (usage: %s)", arg, runCommandUsage)
		case opts.ref != "":
			return opts, usageError("agenttunnel run takes one template file or name")
		default:
			opts.ref = arg
		}
	}
	if opts.ref == "" {
		return opts, usageError("agenttunnel run requires a template file or name")
	}
	if timeoutSet && !opts.wait {
		return opts, usageError("--timeout only applies with --wait")
	}
	if opts.wait && opts.follow {
		return opts, usageError("agenttunnel run --wait already follows the turn: drop -f or --wait")
	}
	return opts, nil
}

func runJob(ctx context.Context, env *Env, args []string) error {
	opts, err := parseRunArgs(args)
	if err != nil {
		return err
	}
	if opts.follow && env.JSON {
		return usageError("agenttunnel run --json cannot follow: JSON is one document, not a stream (drop -f or --json)")
	}
	if opts.wait && !env.JSON {
		return usageError("agenttunnel run --wait prints one JSON result: add --json")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	path, err := template.Find(cwd, opts.ref)
	if err != nil {
		return err
	}
	tmpl, err := template.Load(path)
	if err != nil {
		return err
	}

	c, _, err := env.Connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	result, err := startRun(ctx, c, tmpl)
	if err != nil {
		return err
	}
	if opts.wait {
		return awaitRunResult(ctx, env, c, result, runWaitOptions{timeout: opts.timeout})
	}
	if env.JSON {
		return env.PrintJSON(result)
	}
	printRunStarted(env, result)
	if !opts.follow {
		return nil
	}
	fmt.Fprintln(env.Out)
	followCtx, stop := withInterrupt(ctx)
	defer stop()
	row := threadListRow{ID: result.ThreadID, Title: result.Title, ProjectID: result.ProjectID}
	return streamLogs(followCtx, env, c, row, followOptions{
		lines: 200, follow: true, requireActivity: true,
	})
}

// runResult is what `agenttunnel run` reports about the thread it started.
type runResult struct {
	ThreadID  string `json:"threadId"`
	ProjectID string `json:"projectId"`
	Project   string `json:"project,omitempty"`
	Title     string `json:"title"`
	Detached  bool   `json:"detached"`
}

// Request shapes for the RPCs a run issues. They are structs rather than maps so
// a test can assert exactly what the CLI asked the daemon for.
type (
	runProjectCreateParams struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	runThreadCreateParams struct {
		Title     string `json:"title"`
		ProjectID string `json:"projectId"`
	}
	runRoleRuntimeParams struct {
		ThreadID string `json:"threadId"`
		Role     string `json:"role"`
		Harness  string `json:"harness,omitempty"`
		Model    string `json:"model,omitempty"`
		Variant  string `json:"variant,omitempty"`
	}
	runTurnStartParams struct {
		ThreadID string `json:"threadId"`
		Text     string `json:"text"`
		Detach   bool   `json:"detach"`
		// Attachments are worktree-relative files or folders sent as context.
		Attachments []string `json:"attachments,omitempty"`
	}
	runThreadCreateReply struct {
		ThreadID string `json:"threadId"`
	}
)

// startRun turns a parsed template into a running job: resolve (or create) the
// project, create the thread, pin the per-role runtimes, then start the turn
// detached. A failure part-way leaves an idle thread rather than a half-set
// runtime; the caller reports the thread so the user can retry or delete it.
func startRun(ctx context.Context, c cliCaller, tmpl *template.Template) (runResult, error) {
	project, err := resolveRunProject(ctx, c, tmpl.Project)
	if err != nil {
		return runResult{}, err
	}

	var created runThreadCreateReply
	if err := c.Call(ctx, "thread.create", runThreadCreateParams{Title: tmpl.Name, ProjectID: project.ID}, &created); err != nil {
		return runResult{}, err
	}
	if created.ThreadID == "" {
		return runResult{}, fmt.Errorf("thread.create returned no thread id")
	}

	for _, role := range sortedRoles(tmpl.ModelConfig) {
		cfg := tmpl.ModelConfig[role]
		if cfg.Harness == "" && cfg.Model == "" && cfg.Variant == "" {
			continue
		}
		if err := c.Call(ctx, "thread.setRoleRuntime", runRoleRuntimeParams{
			ThreadID: created.ThreadID, Role: role,
			Harness: cfg.Harness, Model: cfg.Model, Variant: cfg.Variant,
		}, nil); err != nil {
			return runResult{}, fmt.Errorf("thread.setRoleRuntime %s: %w", role, err)
		}
	}

	if err := c.Call(ctx, "thread.turn.start", runTurnStartParams{
		ThreadID: created.ThreadID, Text: tmpl.Body, Detach: true,
	}, nil); err != nil {
		return runResult{}, err
	}

	return runResult{
		ThreadID:  created.ThreadID,
		ProjectID: project.ID,
		Project:   projectLabel(project),
		Title:     tmpl.Name,
		Detached:  true,
	}, nil
}

// sortedRoles makes the runtime RPCs deterministic, so the same template always
// produces the same call order.
func sortedRoles(mc template.ModelConfig) []string {
	roles := make([]string, 0, len(mc))
	for role := range mc {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

// resolveRunProject maps a template's `project` to a registered project. An id
// or an existing directory wins; a directory the registry does not know is
// created under its base name. A non-directory that is not an id is tried as a
// project name, which must be unique.
func resolveRunProject(ctx context.Context, c cliCaller, selector string) (projectsRow, error) {
	var projects []projectsRow
	if err := c.Call(ctx, "project.list", nil, &projects); err != nil {
		return projectsRow{}, err
	}

	label := strings.TrimSpace(selector)
	if label == "" {
		label = "."
	}
	for _, p := range projects {
		if p.ID == label {
			return p, nil
		}
	}

	resolved, err := resolveProjectDir(expandHome(label))
	if err != nil {
		if match, ok, nameErr := projectByName(projects, label); nameErr != nil {
			return projectsRow{}, nameErr
		} else if ok {
			return match, nil
		}
		return projectsRow{}, fmt.Errorf("project %q: %w", label, err)
	}
	if existing, ok := findProjectByPath(projects, resolved); ok {
		return existing, nil
	}

	name := filepath.Base(resolved)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "project"
	}
	var created projectsCreateReply
	if err := c.Call(ctx, "project.create", runProjectCreateParams{Name: name, Path: resolved}, &created); err != nil {
		return projectsRow{}, err
	}
	if created.Project.ID == "" {
		return projectsRow{}, fmt.Errorf("project.create returned no project id")
	}
	return created.Project, nil
}

// projectByName resolves a project name to exactly one entry. Two projects
// sharing a name is an error rather than a guess.
func projectByName(projects []projectsRow, name string) (projectsRow, bool, error) {
	var matches []projectsRow
	for _, p := range projects {
		if strings.EqualFold(strings.TrimSpace(p.Name), name) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return projectsRow{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		listed := make([]string, len(matches))
		for i, m := range matches {
			listed[i] = fmt.Sprintf("%s (%s)", m.ID, m.Path)
		}
		return projectsRow{}, false, fmt.Errorf("project name %q is ambiguous; candidates: %s", name, strings.Join(listed, ", "))
	}
}

// printRunStarted is the detached receipt: how to watch the job that just
// started, so the user never has to guess the thread id.
func printRunStarted(env *Env, result runResult) {
	project := result.Project
	if project == "" {
		project = result.ProjectID
	}
	fmt.Fprintf(env.Out, "thread %s  %q  project %s\n", result.ThreadID, result.Title, project)
	fmt.Fprintln(env.Out, "started in the background")
	fmt.Fprintln(env.Out)
	fmt.Fprintf(env.Out, "agenttunnel attach %s\n", result.ThreadID)
	fmt.Fprintf(env.Out, "agenttunnel logs %s --follow\n", result.ThreadID)
}

// runWaitResult is the one JSON document `run --wait --json` prints on stdout.
type runWaitResult struct {
	ThreadID string         `json:"threadId"`
	Status   string         `json:"status"`
	Summary  string         `json:"summary"`
	Agents   []runWaitAgent `json:"agents"`
	// Note says what a non-outcome status means, e.g. that a timed-out turn is
	// still running on the daemon.
	Note string `json:"note,omitempty"`
}

type runWaitAgent struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

// runWaitOptions are the seams awaitRunTurn exposes. The zero value uses the
// production intervals; tests set pollEvery and startBound so the wait is
// driven by events, not by timers firing.
type runWaitOptions struct {
	timeout    time.Duration // 0 = no limit
	pollEvery  time.Duration
	startBound time.Duration
}

// waitEnd says why awaitRunTurn returned.
type waitEnd int

const (
	waitTurnEnded waitEnd = iota
	waitTimedOut
	waitNoTurn
	waitStreamClosed
	waitInterrupted
)

// awaitRunTurn follows the thread a run just created until its turn is over.
// The thread is new, so its roster starts empty and every agent it shows belongs
// to this turn; the tracker therefore needs no requireActivity guard. Live lines
// are not printed: stdout belongs to the JSON result, so frames only feed the
// tracker.
func awaitRunTurn(ctx context.Context, env *Env, c *Client, threadID string, opts runWaitOptions) waitEnd {
	quiet := *env
	quiet.Out = io.Discard

	// Subscribe before reading the roster so no event falls between the two. A
	// failed subscribe only loses live frames; the roster poll still sees the end.
	_ = subscribeThread(ctx, c, threadID)
	tracker := newTurnTracker(map[string]contracts.AgentStatus{}, false)
	if fresh, err := fetchAgentStatuses(ctx, c, threadID); err == nil {
		tracker.update(fresh)
	}
	if tracker.ended() {
		return waitTurnEnded
	}

	interval, bound := followPollInterval, runStartBound
	if opts.pollEvery > 0 {
		interval = opts.pollEvery
	}
	if opts.startBound > 0 {
		bound = opts.startBound
	}
	var deadline <-chan time.Time
	if opts.timeout > 0 {
		timer := time.NewTimer(opts.timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	noTurn := time.NewTimer(bound)
	defer noTurn.Stop()
	noTurnC := noTurn.C
	poll := time.NewTicker(interval)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			return waitInterrupted
		case <-deadline:
			return waitTimedOut
		case <-noTurnC:
			// The bound is a last resort. Ask the daemon first: a fresh roster,
			// then whether the thread recorded a turn at all.
			if fresh, err := fetchAgentStatuses(ctx, c, threadID); err == nil {
				tracker.update(fresh)
			}
			if tracker.started() {
				noTurnC = nil
				break
			}
			begun, err := threadTurnBegun(ctx, c, threadID)
			switch {
			case err != nil:
				// A failed read is not evidence that the turn never started.
				noTurn.Reset(bound)
			case begun:
				noTurnC = nil
			default:
				return waitNoTurn
			}
		case <-poll.C:
			if fresh, err := fetchAgentStatuses(ctx, c, threadID); err == nil {
				tracker.update(fresh)
			}
		case ev, open := <-c.Events():
			if !open {
				return waitStreamClosed
			}
			printFollowEvent(&quiet, ev, threadID, followOptions{}, tracker)
		}
		if tracker.ended() {
			return waitTurnEnded
		}
	}
}

// threadTurnBegun reports whether the daemon has recorded this thread's turn: a
// turn-opening message bumps Turn, and the lead appears once it is dispatched.
func threadTurnBegun(ctx context.Context, c cliCaller, threadID string) (bool, error) {
	var st domain.ThreadState
	if err := c.Call(ctx, "thread.get", map[string]string{"threadId": threadID}, &st); err != nil {
		return false, err
	}
	return st.Turn > 0 || len(st.AgentOrder) > 0, nil
}

// awaitRunResult waits for the turn a `run --wait` started, then prints the
// JSON result. Exit is 0 only when the turn succeeded.
func awaitRunResult(ctx context.Context, env *Env, c *Client, result runResult, opts runWaitOptions) error {
	waitCtx, stop := withInterrupt(ctx)
	defer stop()
	end := awaitRunTurn(waitCtx, env, c, result.ThreadID, opts)
	if end == waitInterrupted {
		fmt.Fprintf(env.Err, "agenttunnel: interrupted; thread %s keeps running\n", result.ThreadID)
		return failure()
	}
	// The wait's context may already be done, so the final read uses the caller's.
	var st domain.ThreadState
	if err := c.Call(ctx, "thread.get", map[string]string{"threadId": result.ThreadID}, &st); err != nil {
		return err
	}
	out := runWaitOutcome(end, st, result.ThreadID, opts.timeout)
	if err := env.PrintJSON(out); err != nil {
		return err
	}
	if out.Status == "succeeded" {
		return nil
	}
	return failure()
}

// runWaitOutcome builds the result document from why the wait stopped and the
// thread's final state.
func runWaitOutcome(end waitEnd, st domain.ThreadState, threadID string, timeout time.Duration) runWaitResult {
	out := runWaitResult{
		ThreadID: threadID,
		Summary:  finalOrchestratorText(st),
		Agents:   runWaitAgents(st),
	}
	switch end {
	case waitTimedOut:
		out.Status = "timeout"
		out.Note = fmt.Sprintf("timed out after %s; the turn is still running. Follow it with: agenttunnel logs %s", timeout, threadID)
	case waitNoTurn:
		out.Status = "failed"
		out.Note = "the turn did not start; see agenttunnel logs for errors"
	case waitStreamClosed:
		out.Status = "failed"
		out.Note = "event stream closed before the turn ended; the turn may still be running"
	default:
		out.Status = turnStatusFromRoster(st)
	}
	return out
}

// turnStatusFromRoster reads the outcome from the final roster, which is the
// authoritative record: the lead's terminal status decides it, and any failed
// or cancelled agent makes the turn failed.
func turnStatusFromRoster(st domain.ThreadState) string {
	var lead *contracts.Agent
	anyFailed := false
	for _, id := range st.AgentOrder {
		agent, ok := st.Agents[id]
		if !ok {
			continue
		}
		if agent.IsOrchestrator() && lead == nil {
			a := agent
			lead = &a
		}
		if agent.Status == contracts.AgentFailed || agent.Status == contracts.AgentCancelled {
			anyFailed = true
		}
	}
	switch {
	case lead != nil && lead.Status == contracts.AgentCancelled:
		return "cancelled"
	case anyFailed:
		return "failed"
	case lead != nil && lead.Status == contracts.AgentSucceeded:
		return "succeeded"
	default:
		return "failed"
	}
}

// finalOrchestratorText is the turn's summary: the last message the orchestrator
// wrote. The orchestrator records it before it marks the lead terminal.
func finalOrchestratorText(st domain.ThreadState) string {
	for i := len(st.Messages) - 1; i >= 0; i-- {
		if st.Messages[i].Author == "orchestrator" {
			return strings.TrimSpace(st.Messages[i].Text)
		}
	}
	return ""
}

// runWaitAgents lists the roster in spawn order. It is never nil, so an empty
// roster prints as [] rather than null.
func runWaitAgents(st domain.ThreadState) []runWaitAgent {
	out := make([]runWaitAgent, 0, len(st.AgentOrder))
	for _, id := range st.AgentOrder {
		agent, ok := st.Agents[id]
		if !ok {
			continue
		}
		out = append(out, runWaitAgent{
			ID: string(agent.ID), Name: agent.Name, Role: string(agent.Role), Status: string(agent.Status),
		})
	}
	return out
}
