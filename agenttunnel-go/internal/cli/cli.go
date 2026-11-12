// Package cli implements the `agenttunnel` command-line client for AgentTunnel. It is a
// thin client over the daemon's JSON-RPC WebSocket: commands are registered in
// init() by the files that own them, so adding a command never edits this file.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Command is one node of the `agenttunnel <group> <sub> [args]` tree. A command with
// Subcommands is a group: it only dispatches. Later command files call
// Register from init().
type Command struct {
	Name    string
	Summary string
	// Usage is the full usage line shown by `agenttunnel <command> --help`, e.g.
	// "agenttunnel daemon stop". Defaults to "agenttunnel <name>" when empty.
	Usage string
	// Run executes a leaf command. Groups leave it nil.
	Run func(ctx context.Context, env *Env, args []string) error
	// Subcommands turn this node into a group.
	Subcommands []Command
	// Hidden keeps a command out of the help listing.
	Hidden bool
}

// Env is the shared configuration every command receives: global flags, the
// output streams, and the release version.
type Env struct {
	JSON      bool
	DaemonURL string
	HomeDir   string
	Out       io.Writer
	Err       io.Writer
	Version   string
}

// StateDir is the daemon's state directory: --home-dir when given, else the
// platform default ~/.agenttunnel.
func (e *Env) StateDir() string {
	if e.HomeDir != "" {
		return e.HomeDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".agenttunnel"
	}
	return filepath.Join(home, ".agenttunnel")
}

// PrintJSON writes v as one indented JSON document.
func (e *Env) PrintJSON(v any) error {
	enc := json.NewEncoder(e.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// exitError carries an explicit process exit code. A nil Err means the command
// already printed whatever the user needed to see.
type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string {
	if e.err == nil {
		return "exit " + strconv.Itoa(e.code)
	}
	return e.err.Error()
}

func (e exitError) Unwrap() error { return e.err }

// usageError reports bad invocation: exit code 2.
func usageError(format string, args ...any) error {
	return exitError{code: 2, err: fmt.Errorf(format, args...)}
}

// failure reports a command failure after the command printed its own message:
// exit code 1 without a second print.
func failure() error { return exitError{code: 1} }

// registry holds top-level commands in registration order.
var registry []Command

// Register adds a top-level command or group. It panics on a duplicate name: a
// programming error, caught at process start rather than at dispatch.
func Register(cmd Command) {
	if cmd.Name == "" {
		panic("cli: command name is required")
	}
	for _, existing := range registry {
		if existing.Name == cmd.Name {
			panic("cli: duplicate command " + cmd.Name)
		}
	}
	registry = append(registry, cmd)
}

// Commands returns a copy of the registered top-level commands.
func Commands() []Command {
	return append([]Command(nil), registry...)
}

// Main runs the CLI against the process's streams.
func Main(args []string, version string) int {
	return Run(args, version, os.Stdout, os.Stderr)
}

// Run runs the CLI with explicit streams and returns the process exit code: 0
// success, 2 usage error, 1 command failure. version is the build's release
// version.
func Run(args []string, version string, out, errOut io.Writer) int {
	env := &Env{Out: out, Err: errOut, Version: version}

	// A bare --version is the one global flag that is itself a command.
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(env.Out, "agenttunnel "+version)
		return 0
	}

	rest, help, err := parseGlobal(args, env)
	if err != nil {
		fmt.Fprintln(env.Err, "agenttunnel:", err)
		return 2
	}
	if help {
		if len(rest) == 0 {
			printHelp(env.Out, nil)
		} else {
			printCommandHelp(env.Out, rest)
		}
		return 0
	}
	if len(rest) == 0 {
		printHelp(env.Out, nil)
		return 2
	}
	if rest[0] == "help" {
		printCommandHelp(env.Out, rest[1:])
		return 0
	}

	cmd, ok := findCommand(registry, rest[0])
	if !ok {
		fmt.Fprintf(env.Err, "agenttunnel: unknown command %q\n\n", rest[0])
		printHelp(env.Err, nil)
		return 2
	}
	args = rest[1:]

	// Walk groups until a leaf command is reached.
	path := "agenttunnel " + cmd.Name
	for len(cmd.Subcommands) > 0 {
		if len(args) == 0 {
			printHelp(env.Out, &cmd)
			return 2
		}
		next, ok := findCommand(cmd.Subcommands, args[0])
		if !ok {
			fmt.Fprintf(env.Err, "agenttunnel: unknown command %q\n\n", path+" "+args[0])
			printHelp(env.Err, &cmd)
			return 2
		}
		path += " " + next.Name
		cmd = next
		args = args[1:]
	}

	ctx := context.Background()
	if err := cmd.Run(ctx, env, args); err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			if ee.err != nil {
				fmt.Fprintln(env.Err, "agenttunnel:", ee.err)
			}
			return ee.code
		}
		fmt.Fprintln(env.Err, "agenttunnel:", err)
		return 1
	}
	return 0
}

// findCommand looks a command up by name.
func findCommand(cmds []Command, name string) (Command, bool) {
	for _, c := range cmds {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

// parseGlobal extracts the global flags anywhere before a `--`, returning the
// command and its arguments. A command's own flags are left untouched.
func parseGlobal(args []string, env *Env) (rest []string, help bool, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i+1:]...)
			break
		}
		switch {
		case arg == "-h" || arg == "--help":
			help = true
		case arg == "--json":
			env.JSON = true
		case strings.HasPrefix(arg, "--daemon="):
			env.DaemonURL = strings.TrimPrefix(arg, "--daemon=")
		case arg == "--daemon":
			if i+1 >= len(args) {
				return nil, help, errors.New("--daemon requires a ws:// URL")
			}
			i++
			env.DaemonURL = args[i]
		case strings.HasPrefix(arg, "--home-dir="):
			env.HomeDir = strings.TrimPrefix(arg, "--home-dir=")
		case arg == "--home-dir":
			if i+1 >= len(args) {
				return nil, help, errors.New("--home-dir requires a directory")
			}
			i++
			env.HomeDir = args[i]
		default:
			rest = append(rest, arg)
		}
	}
	return rest, help, nil
}

// printHelp writes the top-level help, or a group's help when cmd is set.
func printHelp(w io.Writer, cmd *Command) {
	if cmd != nil {
		usage := cmd.Usage
		if usage == "" {
			usage = "agenttunnel " + cmd.Name
		}
		if len(cmd.Subcommands) == 0 {
			fmt.Fprintf(w, "Usage: %s\n\n%s\n", usage, cmd.Summary)
			return
		}
		fmt.Fprintf(w, "Usage: %s <command> [args]\n\n%s\n\nCommands:\n", usage, cmd.Summary)
		printCommands(w, cmd.Subcommands)
		return
	}
	fmt.Fprintln(w, "Usage: agenttunnel <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run a coding agent through the AgentTunnel daemon.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	printCommands(w, registry)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global flags:")
	fmt.Fprintln(w, "  --json                 Print machine-readable JSON")
	fmt.Fprintln(w, "  --daemon <ws-url>      Talk to this daemon instead of <state>/daemon.json")
	fmt.Fprintln(w, "  --home-dir <dir>       Daemon state directory (default ~/.agenttunnel)")
	fmt.Fprintln(w, "  -h, --help             Show this help")
}

func printCommands(w io.Writer, cmds []Command) {
	sorted := append([]Command(nil), cmds...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	width := 0
	for _, c := range sorted {
		if !c.Hidden && len(c.Name) > width {
			width = len(c.Name)
		}
	}
	for _, c := range sorted {
		if c.Hidden {
			continue
		}
		fmt.Fprintf(w, "  %-*s  %s\n", width, c.Name, c.Summary)
	}
}

// printCommandHelp resolves a command path for `agenttunnel help …`.
func printCommandHelp(w io.Writer, path []string) {
	cmds := registry
	var found Command
	for _, name := range path {
		c, ok := findCommand(cmds, name)
		if !ok {
			fmt.Fprintf(w, "agenttunnel: unknown command %q\n", name)
			return
		}
		found = c
		cmds = c.Subcommands
	}
	if len(path) == 0 {
		printHelp(w, nil)
		return
	}
	target := found
	printHelp(w, &target)
}
