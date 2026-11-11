package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/template"
)

func init() {
	Register(Command{
		Name:    "run",
		Summary: "Start a job from a template as a background thread",
		Usage:   "at run <file|name> [--follow] [--no-follow]",
		Run:     runJob,
	})
}

// runOptions is the parsed `at run` invocation.
type runOptions struct {
	ref    string
	follow bool
}

func parseRunArgs(args []string) (runOptions, error) {
	var opts runOptions
	for _, arg := range args {
		switch {
		case arg == "--follow" || arg == "-f":
			opts.follow = true
		case arg == "--no-follow":
			opts.follow = false
		case strings.HasPrefix(arg, "-"):
			return opts, usageError("unknown flag %q (usage: at run <file|name> [--follow] [--no-follow])", arg)
		case opts.ref != "":
			return opts, usageError("at run takes one template file or name")
		default:
			opts.ref = arg
		}
	}
	if opts.ref == "" {
		return opts, usageError("at run requires a template file or name")
	}
	return opts, nil
}

func runJob(ctx context.Context, env *Env, args []string) error {
	opts, err := parseRunArgs(args)
	if err != nil {
		return err
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

// runResult is what `at run` reports about the thread it started.
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
	fmt.Fprintf(env.Out, "at attach %s\n", result.ThreadID)
	fmt.Fprintf(env.Out, "at logs %s --follow\n", result.ThreadID)
}
