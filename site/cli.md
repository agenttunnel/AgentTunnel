---
layout: default
title: CLI
---

# CLI reference

AgentTunnel has two command-line programs:

- **`agenttunnel`** is the client. It talks to a running daemon, and starts one when needed.
- **`atd`** is the daemon. It runs agents and serves the API. It is normally started for you, but you can run it yourself.

There is also **`agenttunnel-desktop`**, which opens the desktop app. Its only argument is an optional project directory.

## Global flags

These go before the command name, for example `agenttunnel --json ps`.

| Flag | Meaning |
| --- | --- |
| `--json` | print machine-readable JSON. Errors go to stderr as `{"error","code"}`. |
| `--daemon <ws-url>` | use this daemon instead of the one in `<state>/daemon.json`. An explicit daemon is never auto-started. |
| `--home-dir <dir>` | the daemon's state directory. Default `~/.agenttunnel`. |
| `-h`, `--help` | show help. `agenttunnel help <command>` does the same. |
| `--version` | print the version and exit. |

**Exit codes:** `0` success, `2` usage error, `1` command failed.

## Daemon

### `agenttunnel daemon start`

Start a detached daemon if none is running.

### `agenttunnel daemon status`

Show whether a daemon is running, and whether the always-on service is installed.

### `agenttunnel daemon install`

Run `atd` in the background from login: launchd on macOS, `systemd --user` on Linux, Task Scheduler on Windows. Idempotent: running it again rewrites the service and restarts it.

### `agenttunnel daemon uninstall`

Remove the always-on service.

### `agenttunnel daemon stop`

Stop the running daemon and wait for it to exit.

### `atd`

Run the daemon in the foreground.

```sh
atd --port 7331 --workdir /path/to/a/repo
# prints: pairing URL: http://127.0.0.1:7331/?token=...
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--host` | `127.0.0.1` | interface to bind |
| `--port` | `7331` | port to bind. `0` picks a free port. |
| `--home-dir` | `~/.agenttunnel` | state directory |
| `--token` | generated | auth token. Generated when empty. |
| `--workdir` | `.` | working directory handed to agents |
| `--max-passes` | `3` | orchestrator passes per turn |
| `--concurrency` | `4` | maximum concurrent subagents |
| `--relay` | none | enable the outbound relay at this `wss://` URL (or a loopback `ws://` URL). Saved to settings. |
| `--log-file` | none | append logs to this file, rotated to `<file>.1` at 5 MB |

A daemon bound to a non-loopback address does not print the pairing URL, so the token stays out of logs. Send the token as an `Authorization: Bearer` header.

If a daemon already serves the same state directory, `atd` exits `0` without starting a second one.

### `atd --version`

Print the daemon version.

### `atd update`

Check for and install a newer daemon build.

| Flag | Meaning |
| --- | --- |
| `--check` | report whether an update is available, then exit |
| `-y` | install without asking |

A running daemon keeps its old build until you restart it. Updates are refused inside an agent session.

## Threads and agents

### `agenttunnel ps [-a|--all]`

List threads and what their agents are doing. Stale threads are hidden by default; `-a` shows them too.

### `agenttunnel agents <thread>`

List a thread's agents, with their roles and statuses.

### `agenttunnel describe thread <thread> [-o json|yaml]`

Show a thread's full definition, its agents, tasks, and recent events.

### `agenttunnel describe agent <thread> <agent> [-o json|yaml]`

Show one agent's definition and spend.

`agenttunnel inspect` is an alias for `describe`.

### `agenttunnel logs <thread> [agent] [flags]`

Print a thread's agent output.

| Flag | Meaning |
| --- | --- |
| `[agent]` or `--agent <agent>` | show one agent only. Give the name once, not both. |
| `-f`, `--follow` | keep printing new output |
| `--tail N`, `--lines N`, `-n N` | number of lines (default `200`, maximum `5000`) |

### `agenttunnel attach <thread>`

Watch a thread live and steer it from the terminal.

### `agenttunnel stop <thread> [--agents]`

Stop the thread's running turn. With `--agents`, stop its agents as well.

### `agenttunnel tui [thread]`

Open the full-screen terminal UI. With a thread, open straight into it. The keys are in [Features](features.html#terminal-ui).

## Projects

### `agenttunnel projects add <dir> [--name <name>]`

Register a directory as a project. The name defaults to the directory's name.

### `agenttunnel projects ls`

List registered projects.

### `agenttunnel projects rm <id-or-path>`

Remove a project from the registry. Your files are not touched.

### `agenttunnel open <thread | dir>`

Open a thread, or a project directory, in the desktop app. A running app is focused. A closed one starts.

### `agenttunnel desktop [dir]`

Start the desktop app. With a directory, open that project (registering it first if it is new).

## Jobs

### `agenttunnel template [path] [--force]`

Write a commented starter job template. The default path is `agenttunnel.md` in the current directory. Refuses to overwrite a file unless you pass `--force`.

### `agenttunnel run <file|name> [flags]`

Start a job from a template as a background thread. `<name>` is looked up in the current directory, with `.md` and `.agenttunnel.md` added as needed.

| Flag | Meaning |
| --- | --- |
| *(none)* | start the job and return |
| `-f`, `--follow` | start the job and follow its output |
| `--no-follow` | start the job and return (the default) |
| `--wait` | block until the turn ends. Requires `--json`. |
| `--timeout D` | with `--wait`, how long to wait, such as `30m` or `90s`. Default `30m`. `0` means no limit. |

`--json` cannot be combined with `--follow`.

## Models and harnesses

### `agenttunnel harness ls`

List harnesses, whether each one is available, and whether it is enabled.

### `agenttunnel models ls [--harness <h>]`

List known models, their variants, and the role defaults. `--harness` limits the list to one harness.

## Usage

### `agenttunnel usage [--thread <ref>] [--project <id>] [--limits]`

Show token usage and cost. Narrow it with `--thread` or `--project`. `--limits` shows subscription limits where the provider exposes them.

## Web and WhatsApp

### `agenttunnel web [dir]`

Serve the web UI on `127.0.0.1` and open it in your browser. If a daemon is already running, the web UI attaches to it. Otherwise it starts one for this session. Refused with `--daemon`.

### `agenttunnel whatsapp connect`

Pair WhatsApp by scanning a QR code shown in the terminal.

## Relay

### `atrelay`

Run the self-hosted relay that lets a phone reach a daemon with no inbound port. Run it on a host you control.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--listen` | `127.0.0.1:7777` | address to listen on. Put TLS in front for public use. |
| `--state` | `~/.agenttunnel-relay/state.json` | file for instance and token hashes. Empty keeps state in memory. |
| `--trust-proxy-header` | none | header your TLS proxy sets to the client address, such as `X-Forwarded-For` |

See [Features](features.html#remote-access) for the setup.

## Agent-only commands

Agents call `atd tool <command>` (and `agenttunnel-desktop tool`) from their shell to dispatch work and report back. The commands are `dispatch`, `message`, `spawn`, `approve`, `reject`, `ask`, and others. They need the agent environment (`AT_DAEMON_URL`), so they fail outside an agent session. You don't need them yourself.
