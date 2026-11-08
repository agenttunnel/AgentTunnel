# AgentTunnel

AgentTunnel is a minimal GUI for coding agents: a multi-agent coding workflow
where a planning **orchestrator** breaks a task into jobs and dispatches them to
**planner / executor / tester / reviewer** subagents, each of which can run on
any provider CLI (Codex, Claude, Cursor, Grok, OpenCode, Pi, Command Code).

The app is a daemon plus a native desktop client, both in Go. This directory is
the whole product; the repository root holds only CI, issue templates, and repo
docs. (The earlier TypeScript implementation was removed when this one became
the main app — git history retains it.)

## Why Go

The daemon supervises N concurrent provider subprocesses and fans their streams
out over WebSocket. Goroutines plus first-class process control are a natural fit,
builds are fast, and the result ships as a single static binary. The desktop
client is Wails v3 (Go + system webview), so the whole app is one language and
stays light.

## Layout

```
agenttunnel-go/
  cmd/atd/            the daemon entrypoint (HTTP + WebSocket server)
  internal/
    contracts/        wire types: roles, harnesses, commands, events, usage, logs
    domain/           event-sourced core: Decider (pure) + Projector + Engine
    providers/        provider abstraction: Driver + ProcessAdapter + registry
    orchestrator/     roles, dispatch protocol, auto mode, dependency scheduler,
                      spawn gating, peer mailbox, council
    contextstore/     shared notebook, per-thread context, inherited context, mailbox
    usage/            collective token/cost ledger (orchestrator vs subagents)
    logstore/         per-agent log buffer + JSONL file sink
    gitdiff/          working-tree diff parser for the review view
    transport/        JSON-RPC over WebSocket; streamed thread events
  pkg/daemon/         public assembly (engine + orchestrator + HTTP surface)
  desktop/            Wails v3 desktop client (embeds the daemon)
```

## Install from a release

The [AgentTunnel Release workflow](../.github/workflows/agenttunnel-release.yml)
builds the Go daemon and desktop client for macOS (arm64 and amd64), Linux
(amd64), and Windows (amd64), and attaches them to every GitHub release. Install
both binaries with:

```bash
curl -fsSL https://github.com/agenttunnel/AgentTunnel/releases/latest/download/install-agenttunnel-go.sh | sh
```

The installer writes `atd` (the daemon) and `agenttunnel` (the desktop client)
to `~/.local/bin`. Add that directory to `PATH` if it is not there already.

Install somewhere else, or pin a release:

```bash
curl -fsSL https://github.com/agenttunnel/AgentTunnel/releases/latest/download/install-agenttunnel-go.sh \
  | AGENTTUNNEL_INSTALL_DIR=/usr/local/bin sh

curl -fsSL https://github.com/agenttunnel/AgentTunnel/releases/latest/download/install-agenttunnel-go.sh \
  | AGENTTUNNEL_VERSION=v0.10.0 sh
```

The Linux desktop build needs GTK 4 and WebKitGTK 6.0 at runtime
(`libgtk-4-1` and `libwebkitgtk-6.0-4` on Debian/Ubuntu). `atd` has no GUI
dependencies.

## Run it

Daemon (the server any client connects to):

```bash
go run ./cmd/atd --port 7331 --workdir /path/to/a/repo
# prints: pairing URL: http://127.0.0.1:7331/?token=...
```

Desktop app (embeds the daemon and opens a native window):

```bash
cd desktop
./build.sh                       # builds the UI, then links bin/agenttunnel
./bin/agenttunnel
```

`build.sh` builds the frontend and then always relinks the binary. Plain
`wails3 build` caches its native link step and does not watch the parent module
(`../internal`, `../pkg`), so a server-side Go change can leave a stale binary.

macOS app bundle:

```bash
cd desktop
./build-app.sh --install         # /Applications/AgentTunnel.app + `agenttunnel` on PATH
```

Then `agenttunnel` starts the app from anywhere. Opening a project:

```bash
agenttunnel ~/code/my-project    # new thread in that project
```

An unknown directory becomes a new project (named after the directory); a known
one (including through a symlink) reuses its existing project. If the app is
already running, the request is forwarded to it and the window focuses instead
of booting a second instance.

Threads are only saved once something happens in them: creating one — from the
UI or `agenttunnel <dir>` — is in-memory until the first message, rename, or
setting change. A thread you create and never use leaves nothing behind.

Tests:

```bash
go test ./...                    # daemon + core (root module)
```

## Architecture

The server is event-sourced:

1. A client sends a typed **command** over WebSocket.
2. A single worker goroutine drains commands in order, so application is totally
   ordered.
3. The pure **decider** turns a command plus current state into **events**.
4. The **store** appends them and the **projector** folds them into the read model.
5. Committed events are pushed to subscribers (`thread.event` frames).

The orchestrator drives a turn: it runs the team-lead agent, parses
`__AGENTTUNNEL_TOOL__` actions off its output, dispatches tasks, runs them with a
wave-based dependency scheduler, and feeds results back into shared context
before the next pass.

Providers are behind an adapter boundary: a `Driver` describes a harness (binary,
argv, line translator) and a `ProcessAdapter` runs it, streaming normalized
events. Adding a provider means implementing one interface.

Agents coordinate beyond the turn loop:

- **Peer messages** — a running subagent messages the lead or a peer with
  `"$AT_TOOL" tool message --to <agent name or 'orchestrator'> --text "..."`.
  Messages are durable (`mailbox.posted` events, visible in `thread.get`) and
  show up as arrows between agents in the desktop's graph tab.
- **Gated sub-spawns** — a subagent that wants help proposes it with
  `"$AT_TOOL" tool spawn --role executor --name <short-name> --task "..."`.
  The proposal pends until the lead runs `tool approve`/`tool reject`; an
  approved child runs parented on its proposer (visible in the graph).
- **Council** — `thread.council.start` runs a set of roles through parallel
  rounds of debate and synthesizes an outcome.

## Where features live

| Feature                                                       | Where                                                                      |
| ------------------------------------------------------------- | -------------------------------------------------------------------------- |
| Roles: orchestrator, planner, executor, tester (QA), reviewer | `contracts/role.go` — a **registry**, so custom roles can be added         |
| Run any agent                                                 | `providers/` — 7 built-in drivers behind one interface                     |
| Auto mode (orchestrator picks each task's model)              | `orchestrator/runner.go` `resolveModel`                                    |
| Collective usage incl. the orchestrator                       | `usage/ledger.go`, `domain/thread.go` (orchestrator vs subagent split)     |
| Logs for every agent/subagent                                 | `logstore/`, exposed as `agents.logs`                                      |
| Shared context between agents                                 | `contextstore/notebook.go`                                                 |
| Global inherited context                                      | `contextstore/inherited.go` (`.orchestrator-context/`)                     |
| Peer agent-to-agent messaging                                 | `contextstore/mailbox.go` — **on by default**, toggled per thread          |
| Subagent sub-spawns gated by the lead                         | `orchestrator/spawns.go` — proposals pend until approved                   |
| Council (parallel debate to a synthesized outcome)            | `orchestrator/council.go`                                                  |
| Agent graph (spawn tree, dependencies, message arcs)          | `desktop/frontend/src/lib/graph.ts`                                        |
| Steering a running turn                                       | `thread.steer`; consumed by the lead's next pass                           |
| Review diff with line comments back to agents                 | `gitdiff/`, `review.*` RPC, Review tab; comments flow into the next prompt |
| Stoppable turns and agents                                    | `thread.turn.stop`, `agents.stop` — real process-group teardown            |
| GitHub/GitLab awareness                                       | **not yet** — next up                                                      |

## RPC surface

Threads: `thread.create`, `thread.list`, `thread.get`, `thread.rename`,
`thread.delete`, `thread.subscribe`, `thread.turn.start`, `thread.turn.stop`,
`thread.steer`, `thread.setAutoMode`, `thread.setPeerMailbox`,
`thread.council.start`.

Agents and usage: `agents.list`, `agents.logs`, `agents.stop`, `usage.get`.

Review: `review.diff`, `review.comment`.

Context: `context.save`, `context.inherited.get`, `context.inherited.save`,
`context.inherited.rule.save`, `context.inherited.rule.delete`.

Desktop handoff: `thread.open` (open a thread for a directory, creating the
project when unknown), `app.open` (broadcast an open-thread/focus request to
every connected client — how `agenttunnel <dir>` reaches a running app).

Files: `files.tree`, `files.read`.

Registry and settings: `roles.list`, `harness.list`, `models.refresh`,
`models.add`, `project.create`, `project.list`, `project.delete`,
`settings.get`, `settings.set`.

## Where state lives

Everything the daemon persists sits under `<home>/.agenttunnel` (override with
`--home-dir`), so a restart does not lose work:

```
~/.agenttunnel/
  settings.json           configuration (projects, roles, harnesses, models)
  threads/<threadId>.jsonl  the append-only event log per thread
  logs/agent-output/      per-agent log files
  .orchestrator-context/  the global inherited context
```

Threads are event-sourced, so the file is the source of truth: the read model is
rebuilt by replaying it on start. A torn final line from an interrupted write is
skipped rather than failing the whole thread.

## Next

- A SQLite-backed event store in place of the JSONL log, for larger histories and
  indexed queries.
- GitHub / GitLab connection + PR awareness as a `sourceControl` layer.
- Streaming token deltas from adapters into the thread timeline.
- Per-role harness/model configuration from the UI.
