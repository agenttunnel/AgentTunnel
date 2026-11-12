# `agenttunnel` CLI core

The `agenttunnel` binary is a thin client over the daemon's JSON-RPC WebSocket. This
package owns the command tree, global flags, the daemon discovery/autostart
path, and the RPC client. Command implementations live in their own files in
this package and register from `init()`, so the core never names them.

## Adding a command

```go
package cli

func init() {
    Register(Command{
        Name:    "ps",
        Summary: "List running turns",
        Usage:   "agenttunnel ps",
        Run:     runPs,
    })
}

func runPs(ctx context.Context, env *Env, args []string) error {
    c, info, err := env.Connect(ctx) // autostarts a daemon when none answers
    if err != nil {
        return err
    }
    defer c.Close()

    var threads []struct {
        ID    string `json:"id"`
        Title string `json:"title"`
    }
    if err := c.Call(ctx, "thread.list", nil, &threads); err != nil {
        return err
    }
    if env.JSON {
        return env.PrintJSON(threads)
    }
    for _, t := range threads {
        fmt.Fprintf(env.Out, "%s\t%s\n", t.ID, t.Title)
    }
    return nil
}
```

Groups are the same type with `Subcommands` and no `Run`:

```go
Register(Command{
    Name: "projects", Summary: "Manage projects",
    Subcommands: []Command{
        {Name: "add", Summary: "Add a project", Usage: "agenttunnel projects add <path>", Run: runProjectsAdd},
        {Name: "ls", Summary: "List projects", Usage: "agenttunnel projects ls", Run: runProjectsLs},
    },
})
```

Rules:

- `Register` panics on a duplicate top-level name.
- `env.Out` / `env.Err` are the streams to write to; `env.JSON` is the global
  `--json` flag.
- Return an error for exit code 1; use `usageError(...)` for exit code 2.
  Commands that already printed their own message return `failure()`.
- Global flags (`--json`, `--daemon`, `--home-dir`, `-h/--help`) are stripped
  before the command sees its arguments.
- Never put the token in a URL. `Env.Connect` sends it as
  `Authorization: Bearer`.

## Thread and agent references

Every thread and subthread has a stable id (`th_` plus 16 hex). Listings show
its 12-character short form, the docker-style id prefix. Any command that takes
a thread accepts, in order:

1. the exact id;
2. the exact title;
3. a unique id prefix (the short form, or a prefix of the hex body);
4. a unique title substring.

Ambiguity is an error that lists the candidates. Agents resolve the same way
within one thread: the exact id (or the id without its `agent-` prefix), then the
exact name, then a unique id prefix. Resolution lives in `refs.go`.

Subthreads are threads with a parent. `ps` nests them under the parent and
shows a PARENT column; `attach` names the parent in its header.

## Thread and agent commands

- `ps [-a]`: threads with their status and agent count. Stale threads are
  hidden; `-a` includes them. Subthreads nest under their parent.
- `agents <thread> [--json]`: a thread's agents with ID, NAME, ROLE, STATUS, HARNESS.
- `logs <thread> [agent] [-f] [--tail N]`: a thread's output, or one agent's
  when given. `--agent <ref>` and `-n` work too.
- `describe thread <thread> [-o json|yaml]`: a kubectl-style definition: name,
  id, parent, project, worktree and branch, harness and model, status, turns,
  spend, roles, the agents table (with parent and pipeline), tasks, peer mailbox,
  subthreads, and the newest events. `--json` is the same as `-o json`.
- `describe agent <thread> <agent> [-o json|yaml]`: one agent's definition
  (parent, pipeline, task, dependencies, status and reason, spend).
- `inspect` is a docker-style alias of `describe`, with the same subcommands.
- `attach <thread>`: replays recent output, streams live output, and takes
  steering text (`/stop`, `/detach`, `/help`). It leaves when the turn it
  watches ends, or on Ctrl-C (which detaches and leaves the turn running).

## Desktop

- `--desktop [dir]`: start the desktop app. With a directory, it opens that
  project: a registered project (matched by its symlink-resolved path) is reused,
  and an unknown directory is registered first. A running app is focused over the
  daemon; a closed one boots onto the project. Relative paths resolve, and a
  non-directory is an error. Like `open`, it needs `agenttunnel-desktop` on PATH
  and reports how to install it if missing.
- `open <thread>`: show a thread in the desktop app, the same way.

## Client

`Env.Connect` resolves the daemon in this order:

1. `--daemon <ws-url>` (token from `AT_TOKEN`, or a matching `daemon.json`);
2. `<state>/daemon.json` when it answers a `project.list` ping;
3. a detached `atd --port 0 --home-dir <state>` autostart, unless
   `AT_AGENT_ID` is set (then it refuses with an explanation).

`Client.Call` correlates replies by id on one shared read loop. Pushed frames
arrive on `Client.Events()`; commands that need them (attach, logs) consume that
channel, and a slow consumer drops frames rather than stalling replies.

`env.StateDir()` is `--home-dir`, else `~/.agenttunnel`. Discovery lives in
`internal/discovery` (`daemon.json`).
