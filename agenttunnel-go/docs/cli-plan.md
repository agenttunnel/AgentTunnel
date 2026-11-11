# `at` — AgentTunnel CLI design and effort

First-party CLI for AgentTunnel. It is a vertical slice, not a second product:
the daemon remains the only server and the only writer of state. Symbols below
are the real surface; prefer them over line numbers.

## Decisions (locked)

1. `atd` is the single server/state owner. Desktop and `at` are thin clients of
   the same JSON-RPC WebSocket. `at` never imports `pkg/daemon` or the server
   half of `internal/transport`.
2. A daemon publishes `<state>/daemon.json`; `at` reads it and `at run`
   autostarts a detached `atd --port 0` when none answers.
3. Turns can detach from the client socket and bind to the daemon lifetime
   context; only `thread.turn.stop` or daemon shutdown ends them.
4. Docker-shaped commands: `run`, `ps`, `logs`, `attach`, `stop`, `projects`,
   `template`, `usage`, `daemon`, `models`, `harness`, `version`.
5. Templates are Markdown + YAML frontmatter (`name`, `project`,
   `modelConfig`). `modelConfig` becomes thread-scoped role-runtime overrides
   (`thread.setRoleRuntime`), never global settings.
6. The binary is `at`. Desktop app, artifact names, and macOS bundle are
   unchanged. `at` joins the release tarball, updater, and installer.
7. v1 is the slice in [Phased plan](#phased-plan-and-exact-steps); everything in
   [Excluded](#excluded-from-v1) is out.

## Baseline

| Area | Reality at HEAD | Consequence |
| --- | --- | --- |
| `cmd/atd/main.go` | flags `--host/--port/--home-dir/--token/--workdir/--relay`; port 7331; ephemeral token; no discovery file; refuses `AT_AGENT_ID` | add discovery, live-daemon guard, `--port 0`; scrub `AT_*` on autostart |
| `internal/transport/server.go` | `/healthz`, `/tool/call`, `/ws`; Bearer any peer, `?token=` loopback only; `handleFrame` uses the socket context | socket close cancels turns today |
| `internal/transport/rpc.go` | `thread.turn.start` → `runTurn` → `beginTurn(ctx)` → `RunTurn`, replies when the turn ends; `thread.logs` tail 400; `thread.subscribe` streams `thread.event` | needs `detach`, turn status, a follow path |
| `desktop/main.go`, `launch.go` | embedded daemon on `127.0.0.1:0`; writes/removes `desktop.json`; `agenttunnel <dir>` forwards `thread.open` + `app.open` | share discovery without losing the one-window lock |
| `internal/orchestrator/runner.go` | `roleRuntime(role)` = builtin default + global `settings.RoleConfig`; `leadRuntime` adds thread overrides for leads only | non-lead role overrides do not exist |
| `internal/settings` | daemon-global roles, models, projects, relay | per-job runtime here would leak across concurrent threads |
| Release | tarball `atd` + `agenttunnel`; updater replaces both; installer loops both; macOS app separate | add `at` additively, rename nothing |

## Architecture

### Ownership, discovery, autostart

One daemon per state directory (`--home-dir`, else `~/.agenttunnel`). `atd`
refuses to boot when `daemon.json` names a daemon that answers `/healthz`.
Desktop keeps `desktop.json` as the "a window is open" lock so
`agenttunnel <dir>` still only forwards when a UI exists.

`internal/discovery` writes `<state>/daemon.json`, mode `0600`, via
`CreateTemp` + `Chmod` + `Sync` + `Rename` (atomic; never a partial token):

```json
{
  "wsUrl": "ws://127.0.0.1:51234/ws",
  "httpUrl": "http://127.0.0.1:51234",
  "token": "<32 hex>",
  "pid": 4213,
  "version": "0.4.2",
  "startedAt": "2026-11-11T20:00:00Z"
}
```

Both entrypoints write it after `net.Listen`/`SetEndpoint` and remove it on
exit; the desktop writes it only while it owns the daemon. Readers probe with
`project.list` (the CLI uses it because `roles.list` panics in transport
fixtures). A stale file is removed; `at run` then autostarts, other commands
tell the user to.

Autostart is `at run` only: take `<state>/daemon.start.lock` with `O_EXCL`
(break locks older than 30 s whose pid is gone), re-read discovery, spawn
`atd --port 0 --home-dir <state>` detached with stdin `/dev/null`, stdout/stderr
appended to `<state>/logs/atd.log`, env scrubbed of `AT_AGENT_ID`,
`AT_THREAD_ID`, `AT_DAEMON_URL`, `AT_TOKEN`; wait up to 10 s for a valid file
and a successful probe. Windows needs `DETACHED_PROCESS`; if unverified, ship
Unix autostart and a clear manual-start error.

### Detached turns

`thread.turn.start` gains `detach: true`. `pkg/daemon` passes the daemon `ctx`
into `DispatcherDeps.BaseCtx`. `startDetachedTurn` claims the slot with
`beginTurn(BaseCtx, ...)`, starts a goroutine (`ensureWorktree`, dispatch
`CmdTurnStart`, `RunTurn`), and replies `{ok, detached:true, threadId}` at once;
`done()` releases the slot on unwind. Failures go to `logstore` as `error`
entries, not `MessageSent` with author `user` (which would bump
`ThreadState.Turn`), so `at logs` shows them without corrupting usage.

`thread.turn.status` returns `{"threadIds":[...]}` from the dispatcher's turn
map for `ps`, `attach`, and `run --follow`. Polling at CLI cadence is the v1
compromise; a durable `turn.finished` event is the follow-up.

Known gap: the detached goroutine cuts the worktree after the reply, so `at run`
can print "started" before the checkout exists or a harness is found. Either
move `ensureWorktree` before the reply or make the CLI say "started, see logs"
and fail `--follow` when nothing runs.

### Handoff matrix

| From → to | Mechanism | v1 |
| --- | --- | --- |
| `at run` → desktop watch | desktop attaches its UI to a live `daemon.json`; `app.open` still broadcasts | yes |
| desktop daemon → `at ps` | desktop writes `daemon.json` while it owns the daemon | yes |
| desktop → background | keep-running toggle spawns a detached `atd`, waits, attaches; next launch attaches again | yes |
| desktop → CLI next turn | same thread, same turn slot; a running turn blocks a new one | yes |
| another terminal → running turn | `at attach` streams events read-only | yes |
| `at attach` → send steering | `thread.steer` exists; input/echo/detach semantics undesigned | no |

Closing the window with keep-running off ends the embedded daemon and its
turns; the toggle must say so.

## CLI surface

Global flags `--json`, `--daemon <ws-url>`, `--home-dir <dir>`, `--help`;
exit 0/1/2 = success/failure/usage; `--json` prints the raw RPC result.

| Command | Does | RPCs |
| --- | --- | --- |
| `run <file> [--project P] [--follow] [--foreground]` | parse template, resolve/create project, create worktree thread, apply runtimes, start detached | `project.list/create`, `thread.create`, `thread.setRoleRuntime`, `thread.turn.start` |
| `ps [-a]` | thread, project, title, turn, agents, usage, updated | `thread.list`, `thread.turn.status`, `thread.get` per row |
| `logs <thread> [-f] [--agent ID]` | history; `-f` follows `agent.output` | `thread.logs`, `agents.logs`, `thread.subscribe` |
| `attach <thread>` | live agent/tool/error view; exits on turn end or Ctrl-C | `thread.subscribe`, `thread.turn.status` |
| `stop <thread>... [--all]` | cancel turns | `thread.turn.stop` |
| `projects add <path> [--name N]`, `ls`, `rm <id\|path> [--force]` | project registry | `project.create/list/delete` |
| `template [path]` | print or write a starter; never overwrites | none |
| `usage [--limits] [--thread ID]` | ledger, subscription windows, or durable thread turns | `usage.get/limits`, `thread.get` |
| `daemon start\|status\|stop` | lifecycle | probe, `daemon.shutdown` |
| `models ls [--refresh]`, `harness ls` | model/harness inventory | `harness.list`, `models.refresh` |
| `version` | CLI and reachable daemon versions | discovery |

```text
$ at run jobs/nightly-audit.md
thread th_9f2a1c7d  project acme
worktree /code/acme.worktrees/nightly-audit-9f2a1c7d
detached: at logs th_9f2a1c7d -f

$ at ps
THREAD       PROJECT  TITLE            TURN  AGENTS  USAGE   UPDATED
th_9f2a1c7d  acme     nightly-audit    run   3/3     128.4k  12s ago
th_41b0de22  acme     fix-worktree     idle  0/2     42.1k   6m ago

$ at logs th_9f2a1c7d
20:41:02 orchestrator   text   planning 3 jobs
20:41:05 executor-1     tool   $ go test ./...
20:41:19 executor-1     error  exit status 1

$ at usage
orchestrator   12.1k in   3.4k out   $0.42
subagents      98.2k in  31.0k out   $2.10
total         110.3k in  34.4k out   $2.52
```

`usage.get` is the in-memory ledger and resets on daemon restart; `--thread`
reads durable `ThreadState.Turns`. Say that in `--help` rather than implying the
total is historical. `ps` usage comes from `thread.get` (N+1 locally is fine);
adding usage to `ThreadSummary` is a follow-up.

## Templates

```markdown
---
name: nightly-audit
project: ~/code/acme
modelConfig:
  orchestrator: {harness: claude, model: claude-opus-4-5}
  executor:     {harness: codex, model: gpt-5-codex, variant: high}
---
Audit this repository for dependency drift. Open one PR per finding.
```

Frontmatter starts on line 1 and closes with `---`; the body is non-empty and
becomes the turn text verbatim. `name` is the thread title (default: file stem).
`project` is an absolute/`~` path matched against `project.list`, else created
with the directory's base name; `--project` overrides. `modelConfig` keys are
builtin role ids (`orchestrator`, `planner`, `executor`, `tester`, `reviewer`,
`facilitator`, `context-manager`); unknown role/harness is an error; an omitted
field inherits. Parsing adds `gopkg.in/yaml.v3`, the only new dependency.
`at template path.md` refuses to overwrite.

`modelConfig` is applied one `thread.setRoleRuntime` per role before the turn.
The override is event-sourced (`thread.role-runtime-set`), projected into
`ThreadState.RoleRuntimes`, and read by
`Runner.roleRuntimeFor(ctx, threadID, role)` with precedence:

```text
thread role override  >  settings.RoleConfig  >  role default
```

A harness-only override clears model and variant, mirroring `leadRuntime`.
Because the state is per thread, two jobs can run different executors
concurrently; the global settings route would race. `thread.get` already returns
`roleRuntimes`, so the desktop needs no new read path. v1 gates overrides to
builtin roles (`contracts.IsBuiltinRole`) because the decider has no registry;
custom roles are a follow-up.

## Release and naming

| File | Change |
| --- | --- |
| `.github/workflows/agenttunnel-release.yml` | build `go-dist/at` beside `atd`; tarball and macOS asset names unchanged |
| `internal/update/update.go` | `binaryNames()` → `["atd","agenttunnel","at"]` (+ `.exe`) |
| `scripts/install.sh` | install `atd`, `agenttunnel`, `at`; unchanged app symlink logic |
| `desktop/`, `build-app.sh` | no rename, no artifact change |
| `cmd/at/main.go` | `var version = "dev"`, same `-X main.version` ldflags |

`at` shadows Unix `at(1)` when `~/.local/bin` precedes `/usr/bin`. The name is
decided; mitigate with an install warning when another `at` is on `PATH` and
document `command -v -a at`. This is a real compatibility cost.

## Phased plan and exact steps

**P0 — this document:** `agenttunnel-go/docs/cli-plan.md`.

**P1 — foundation.**

1. `internal/discovery/discovery.go` + tests: `Info`, `Write`, `Read`, `Remove`,
   `FileName`, 0600, atomic replace, malformed rejection.
2. `cmd/atd/main.go`: live-daemon guard; write discovery after `net.Listen`;
   remove on shutdown; wire a cancellable run ctx into `Options.OnShutdown`.
3. `pkg/daemon/daemon.go`: `OnShutdown`; pass `BaseCtx: ctx` and `Shutdown` in
   `DispatcherDeps`.
4. `internal/transport/rpc.go`: `turnParams.Detach`, `startDetachedTurn`,
   `thread.turn.status`; detached errors to `logstore`. `server.go`:
   `daemon.shutdown` acknowledged before cancel.
5. `cmd/at/main.go` (calls `cli.Main(os.Args[1:], version)`),
   `internal/cli/cli.go` (command tree, flags, help, exit codes),
   `internal/cli/client.go` (WS client, Bearer auth, reply correlation, events).
6. `internal/cli/autostart.go`: probe, lock, spawn, scrub, log, 10 s wait.

**P2 — role runtime (parallel with P1.1–1.4).**

7. `internal/contracts/{commands,events}.go`: `CmdRoleRuntimeSet`,
   `EvtRoleRuntimeSet`, `RoleRuntimeSet`, `IsBuiltinRole`.
8. `internal/domain/{state,projector,decider}.go`: `RoleRuntimes`,
   `RoleRuntimeFor`, projection, validation; `role_runtime_test.go`.
9. `internal/orchestrator/runner.go`, `btw.go`: `roleRuntimeFor`, overlay in
   dispatch, task run, model resolution, title generation.
10. `internal/transport/rpc.go`: `thread.setRoleRuntime`.

**P3 — templates (needs P2 and P1.5).**

11. `internal/cli/template.go`: frontmatter split, `yaml.v3` decode, validation,
    starter text; `go.mod`/`go.sum`.
12. `internal/cli/run.go`: project resolve, `thread.create{worktree:true}`,
    per-role `setRoleRuntime`, `turn.start{detach:true}`, `--follow`.
13. `internal/cli/command_template.go`: `at template [path]`.

**P4 — jobs and parity (needs P1.5; `run` needs P3).**

14. `internal/cli/ps.go`, `logs.go`, `attach.go`, `stop.go`, `format.go`.
    Follow subscribes first, then prints the tail, deduping by
    `(agentId, at, kind, text)`.
15. `internal/cli/projects.go`: `add|ls|rm`; `rm` warns and needs `--force`
    when the project has threads (deleting a project does not delete threads or
    worktrees).
16. `internal/cli/usage.go`, `models.go`, `harness.go`, `version.go`.
17. `internal/cli/daemon.go`: `start|status|stop`; refuse desktop-owned stop
    without `--force`.

**P5 — desktop (needs P1.1; parallel with P2–P4).**

18. `desktop/attach.go` (new) + `desktop/main.go`: read discovery before boot;
    attach mode serves assets but creates no daemon; `conn` gains `attached`;
    do not remove `daemon.json` on exit.
19. `desktop/prefs.go` (new): `<state>/desktop-prefs.json` `{keepRunning}`,
    Wails events `at:get-prefs`/`at:set-prefs`. Keep-running boot spawns `atd`,
    waits, attaches; if missing, disable with a reason.
20. `desktop/frontend/src/App.tsx`, `style.css`, `lib/rpc.ts`: toggle, attached
    banner, `detach:true` on `thread.turn.start` when attached. Keep this diff
    small; App.tsx is the likeliest shared-worktree conflict.

**P6 — release:** workflow, updater `binaryNames`, `install.sh`, README line.

**P7 — verification:** focused unit/transport/CLI tests, then the full suite by
the lead.

## Effort

One engineer who knows the codebase, including tests; ranges because desktop
attach and detached-turn tests are the unknowns.

| Workstream | Scope | Days |
| --- | --- | --- |
| Foundation | discovery, `atd` lifecycle, detach + `thread.turn.status`, shutdown, CLI skeleton/client/autostart | 3.5–4.5 |
| Jobs | run, ps, logs/follow, attach, stop, formatting | 2.0–2.5 |
| Templates | parser + scaffold, role-runtime domain path | 2.0–2.5 |
| Parity | projects, usage, models/harness, version, daemon | 1.5–2.0 |
| Desktop | attach, keep-running, detached-when-attached, UI | 1.5–2.0 |
| Tests/review | CLI integration, packaging checks, review fixes | 1.5–2.5 |
| **Total** | | **12–16** |

The agreed vertical slice (foundation + run/ps/logs/attach/stop + template and
projects + usage + keep-running) is **9–11**; the leads' 7–9 assumes templates
ship without `modelConfig`, which drops the role-runtime path (2.0). Parity and
release are parallelizable, not free.

## Testing and limits

- Unit: discovery round-trip/mode/stale, template parsing and validation, CLI
  flags and golden output strings.
- Transport/`pkg/daemon`: a detached turn survives its socket closing (scripted
  harness blocks on a channel; close the WS; assert completion — no sleeps);
  `thread.turn.status` empties; `setRoleRuntime` reaches the runner and stays
  per thread; two threads with different overrides run concurrently;
  `daemon.shutdown` stops the server.
- CLI integration: build `atd` and `at` into a temp dir, run `atd --port 0` with
  a scrubbed env, then `projects add`, `run` a template against a scripted
  harness, `ps/logs/stop`; follow mode asserts event order, not wall time.
- Race: `go test -race` on the detach paths and `internal/cli`.
- Packaging: the release build compiling `./cmd/at` is the check; grepping
  `install.sh` is not.
- Hard limits: the executor git guard refuses `git init`, so worktree tests in
  `internal/transport` and `pkg/daemon` fail under subagents; verify with
  targeted `-run` and let the lead run the full suite. No live credentials:
  always a scripted harness. No browser/computer use for the desktop toggle;
  test the attach decision as a pure function and state the manual step.

## Risks and open questions

1. **`at` shadows `at(1)`.** Name locked; install warning + docs. Reopening it
   costs one rename in `cmd/`, release, updater, installer.
2. **Detach returns before failure is knowable.** Worktree cut, missing harness,
   and a busy slot are asynchronous; either move `ensureWorktree` before the
   reply or accept "started, see logs".
3. **No completion event.** Polling `thread.turn.status` is the v1 compromise.
4. **Detached turns die with the daemon.** No resume; discovery removal plus
   logs is the honest recovery story.
5. **Discovery token.** 0600, but any process as the user can control the daemon
   — same trust boundary as `settings.json`/`desktop.json`; document it.
6. **Desktop attach edge cases:** tool endpoint ownership, `desktop.json` on
   attach, and whether closing the window kills an attached turn.
7. **Windows:** spawn flags and signals differ; RPC stop works, `SIGTERM` does
   not. If untested, ship Unix autostart with a manual-start error.
8. **Version skew:** one tarball, but a long-lived daemon can differ; warn on
   mismatch instead of failing. `at ps` after a crash can show agents but no
   turn — print "turn: unknown".

## Excluded from v1

No `at update`, TUI, `rm`/`prune`, template inheritance, secrets injection,
remote/SSH daemon, Windows service, `at exec`, steering input in `attach`,
custom-role `modelConfig`, `turn.finished`, usage in `ThreadSummary`, desktop
role-runtime editing, or multi-job orchestration. The slice ends at one detached
job, observable from either surface, with per-job runtimes.
