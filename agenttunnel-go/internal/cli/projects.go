package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

func init() {
	Register(Command{
		Name:    "projects",
		Summary: "Manage the daemon's project registry",
		Usage:   "at projects <add|ls|rm> [args]",
		Run:     runProjects,
	})
}

// projectsRow mirrors transport.ProjectView: a registered project and the
// number of threads that belong to it.
type projectsRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Threads int    `json:"threads"`
}

// projectsCreateReply mirrors project.create's result.
type projectsCreateReply struct {
	Project  projectsRow   `json:"project"`
	Projects []projectsRow `json:"projects"`
}

// runProjects dispatches the subcommands by hand: the registry prints help and
// exits for a bare group, but `at projects` must list instead.
func runProjects(ctx context.Context, env *Env, args []string) error {
	if len(args) == 0 {
		return runProjectsLs(ctx, env, nil)
	}
	switch args[0] {
	case "add":
		return runProjectsAdd(ctx, env, args[1:])
	case "ls", "list":
		return runProjectsLs(ctx, env, args[1:])
	case "rm", "remove":
		return runProjectsRm(ctx, env, args[1:])
	case "help":
		printProjectsUsage(env.Out)
		return nil
	default:
		fmt.Fprintf(env.Err, "at: unknown projects command %q\n\n", args[0])
		printProjectsUsage(env.Err)
		return exitError{code: 2}
	}
}

func printProjectsUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: at projects <add|ls|rm> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  add <path> [--name <name>]  Register a project by its directory")
	fmt.Fprintln(w, "  ls                          List registered projects")
	fmt.Fprintln(w, "  rm <id|path>                Unregister a project by id or path")
}

// connectProjects resolves the daemon without starting one. Project commands
// are registry edits, not job launches, so they must not spawn a daemon as a
// side effect (autostart belongs to `at run`). An explicit --daemon still wins.
func connectProjects(ctx context.Context, env *Env) (*Client, error) {
	if env.DaemonURL != "" {
		c, _, err := env.Connect(ctx)
		return c, err
	}
	c, _, err := env.connectExisting(ctx)
	if err != nil {
		return nil, errors.New("no AgentTunnel daemon is running; start one with `at daemon start`")
	}
	return c, nil
}

func runProjectsAdd(ctx context.Context, env *Env, args []string) error {
	var path, name string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--name":
			if i+1 >= len(args) {
				return usageError("--name requires a value")
			}
			i++
			name = args[i]
		case strings.HasPrefix(arg, "--name="):
			name = strings.TrimPrefix(arg, "--name=")
		case strings.HasPrefix(arg, "-"):
			return usageError("unknown flag %q for at projects add", arg)
		case path != "":
			return usageError("at projects add takes one path and an optional --name")
		default:
			path = arg
		}
	}
	if path == "" {
		return usageError("at projects add requires a directory path")
	}

	resolved, err := resolveProjectDir(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(resolved)
	}

	c, err := connectProjects(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()

	var existing []projectsRow
	if err := c.Call(ctx, "project.list", nil, &existing); err != nil {
		return err
	}
	if match, ok := findProjectByPath(existing, resolved); ok {
		if env.JSON {
			return env.PrintJSON(map[string]any{"project": match, "created": false})
		}
		fmt.Fprintf(env.Out, "%s is already registered as %q (%s)\n", resolved, match.Name, match.ID)
		return nil
	}

	var created projectsCreateReply
	if err := c.Call(ctx, "project.create", map[string]string{"name": name, "path": resolved}, &created); err != nil {
		return err
	}
	if env.JSON {
		return env.PrintJSON(created)
	}
	fmt.Fprintf(env.Out, "added project %q (%s) at %s\n", created.Project.Name, created.Project.ID, created.Project.Path)
	return nil
}

func runProjectsLs(ctx context.Context, env *Env, args []string) error {
	if len(args) > 0 {
		return usageError("at projects ls takes no arguments")
	}
	c, err := connectProjects(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()

	var projects []projectsRow
	if err := c.Call(ctx, "project.list", nil, &projects); err != nil {
		return err
	}
	if env.JSON {
		if projects == nil {
			projects = []projectsRow{}
		}
		return env.PrintJSON(projects)
	}
	return renderProjectsTable(env.Out, projects)
}

func runProjectsRm(ctx context.Context, env *Env, args []string) error {
	var ref string
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "-"):
			return usageError("unknown flag %q for at projects rm", arg)
		case ref != "":
			return usageError("at projects rm takes one project id or path")
		default:
			ref = arg
		}
	}
	if ref == "" {
		return usageError("at projects rm requires a project id or path")
	}

	c, err := connectProjects(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()

	var projects []projectsRow
	if err := c.Call(ctx, "project.list", nil, &projects); err != nil {
		return err
	}
	target, err := matchProject(projects, ref)
	if err != nil {
		return err
	}

	var remaining []projectsRow
	if err := c.Call(ctx, "project.delete", map[string]string{"id": target.ID}, &remaining); err != nil {
		return err
	}
	if env.JSON {
		if remaining == nil {
			remaining = []projectsRow{}
		}
		return env.PrintJSON(remaining)
	}
	fmt.Fprintf(env.Out, "deleted project %q (%s)\n", target.Name, target.ID)
	return nil
}

// resolveProjectDir turns a user-given project path into the absolute,
// symlink-resolved directory the registry stores, so two spellings of one
// directory cannot become two projects.
func resolveProjectDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("project path %s: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project path %s is not a directory", path)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve project path %s: %w", path, err)
	}
	return resolved, nil
}

// resolvePathForMatch normalizes a path for comparison. It does not require the
// path to exist: a removed directory still matches the registry entry.
func resolvePathForMatch(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

func projectPathMatches(registered, resolved string) bool {
	if strings.TrimSpace(registered) == "" {
		return false
	}
	got, err := resolvePathForMatch(registered)
	if err != nil {
		return false
	}
	return got == resolved
}

func findProjectByPath(projects []projectsRow, resolved string) (projectsRow, bool) {
	for _, p := range projects {
		if projectPathMatches(p.Path, resolved) {
			return p, true
		}
	}
	return projectsRow{}, false
}

// matchProject resolves an id or path to exactly one project. A path that hits
// more than one registry entry is an error rather than a guess.
func matchProject(projects []projectsRow, ref string) (projectsRow, error) {
	for _, p := range projects {
		if p.ID == ref {
			return p, nil
		}
	}
	resolved, err := resolvePathForMatch(ref)
	if err != nil {
		return projectsRow{}, err
	}
	var matches []projectsRow
	for _, p := range projects {
		if projectPathMatches(p.Path, resolved) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return projectsRow{}, fmt.Errorf("no project matches %q; run `at projects ls` to see ids and paths", ref)
	case 1:
		return matches[0], nil
	default:
		listed := make([]string, len(matches))
		for i, m := range matches {
			listed[i] = fmt.Sprintf("%s (%q)", m.ID, m.Name)
		}
		return projectsRow{}, fmt.Errorf("%q matches more than one project (%s); remove one by id", ref, strings.Join(listed, ", "))
	}
}

// renderProjectsTable writes the docker-style listing: uppercase headers,
// aligned columns, sorted by name so output is stable.
func renderProjectsTable(w io.Writer, projects []projectsRow) error {
	rows := append([]projectsRow(nil), projects...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		if rows[i].Path != rows[j].Path {
			return rows[i].Path < rows[j].Path
		}
		return rows[i].ID < rows[j].ID
	})

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tPATH\tTHREADS")
	for _, p := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.ID, p.Name, p.Path, strconv.Itoa(p.Threads))
	}
	return tw.Flush()
}
