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
