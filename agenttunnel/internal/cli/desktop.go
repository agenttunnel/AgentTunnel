package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

func init() {
	// Registered under its flag spelling: parseGlobal leaves `--desktop` as the
	// first non-global argument, so it dispatches like any command name.
	Register(Command{
		Name:    "--desktop",
		Summary: "Open the AgentTunnel desktop app, on a project directory when given",
		Usage:   "agenttunnel --desktop [dir]",
		Run:     runDesktop,
	})
}

// runDesktop starts the desktop app. With a directory it opens that project:
// a registered project is reused, an unknown directory is registered first.
// The desktop client then gets the directory, so a running app forwards it and
// a closed one boots onto it.
func runDesktop(ctx context.Context, env *Env, args []string) error {
	if len(args) > 1 {
		return usageError("usage: agenttunnel --desktop [dir]")
	}
	dir := ""
	if len(args) == 1 {
		if args[0] == "" || strings.HasPrefix(args[0], "-") {
			return usageError("usage: agenttunnel --desktop [dir]")
		}
		dir = args[0]
	}
	path, err := lookDesktop()
	if err != nil {
		return err
	}
	if dir == "" {
		if err := startDesktop(path); err != nil {
			return fmt.Errorf("start %s: %w", desktopBinary, err)
		}
		fmt.Fprintln(env.Out, "Opening AgentTunnel desktop")
		return nil
	}
	// Validate the directory before connecting: a bad path must not create a daemon.
	resolved, err := resolveProjectDir(dir)
	if err != nil {
		return err
	}
	c, _, err := env.Connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	project, created, err := openProjectInDesktop(ctx, c, resolved, path, startDesktop)
	if err != nil {
		return err
	}
	if env.JSON {
		return env.PrintJSON(map[string]any{"project": project, "created": created})
	}
	if created {
		fmt.Fprintf(env.Out, "added project %q (%s) at %s\n", project.Name, project.ID, project.Path)
	}
	fmt.Fprintf(env.Out, "Opening %s in AgentTunnel desktop\n", project.Path)
	return nil
}

// openProjectInDesktop returns the registered project at resolved, registering
// it when none exists, then starts the desktop client on that directory.
// resolved must already be absolute and symlink-resolved.
func openProjectInDesktop(ctx context.Context, c cliCaller, resolved, path string, start func(string, ...string) error) (projectsRow, bool, error) {
	var projects []projectsRow
	if err := c.Call(ctx, "project.list", nil, &projects); err != nil {
		return projectsRow{}, false, err
	}
	project, found := findProjectByPath(projects, resolved)
	if !found {
		var reply projectsCreateReply
		params := map[string]string{"name": filepath.Base(resolved), "path": resolved}
		if err := c.Call(ctx, "project.create", params, &reply); err != nil {
			return projectsRow{}, false, err
		}
		project = reply.Project
	}
	if err := start(path, resolved); err != nil {
		return projectsRow{}, false, fmt.Errorf("start %s: %w", desktopBinary, err)
	}
	return project, !found, nil
}
