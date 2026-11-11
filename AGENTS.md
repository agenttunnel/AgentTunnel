# AgentTunnel

AgentTunnel is a minimal GUI for coding agents. The Go daemon (`atd`) wraps
provider CLIs (Codex, Claude, Cursor, OpenCode, Pi, Command Code) behind
an HTTP/WebSocket surface, and a Wails v3 desktop app drives it.

The Go app in `agenttunnel-go/` is the product. The previous TypeScript
monorepo (web, Electron desktop, mobile, relay) was removed when it was
deprecated; git history retains it. Don't resurrect its tooling — `vp`, pnpm,
`apps/`, `packages/` are gone.

You can think of AgentTunnel as an open source "bring-your-own-subscription"
alternative to Claude Desktop, Codex App, and Conductor.

## What makes AgentTunnel special?

We have a large, devoted user base. It's important we maintain the things they
love as we continue to iterate. A brief list of the things we can never
compromise on:

### 1. Open at the core

We share our roadmap, how we think about things, and all our code. A large
number of users run forks. Work in the open, and stay that way.

### 2. Performance without compromise

No slop. Audit for performance regressions: too much data over the WebSocket,
CSS animations causing GPU spikes, lists that are hard to render. All changes
must be considerate of performance impact — our users drive agents all day and
notice a dropped frame, a lying spinner, and a stale label.

### 3. Desktop-first, daemon underneath

The Wails desktop app is the main surface; the daemon it embeds also runs
standalone, so a desktop app can host remote connections. Features need to
work in both shapes: embedded and headless `atd`.

## A note from Theo

I like ambitious ideas, simple systems, and software that feels obvious. Do
not preserve complexity just because it already exists. Do not introduce
machinery because it looks architecturally impressive. Understand the real
constraint, then fight for the smallest model that makes the correct behavior
unsurprising.

Channel both "measure twice, cut once" and "yagni". Fight scope creep. Honor
the dev's intent in both a minimal and realistic fashion.

The rest of this document helps you navigate the codebase and change it. These
are good defaults; the developer's preferences override anything here.

Of note: most contributions come from AgentTunnel itself, often remotely. Be
careful about accessing data, killing dev servers, or anything that could
damage the AgentTunnel instance the contributor is using.

## A small glossary

- **you** — the agent reading this file and changing AgentTunnel.
- **we, us, maintainers** — Theo, Julius, and the people building AgentTunnel.
- **user** — the person using AgentTunnel to direct coding agents.
- **agent / subagent** — a coding agent a user runs inside AgentTunnel; depending on context, also you.
- **provider / harness** — the agent runtime AgentTunnel talks to (Codex, Claude, OpenCode, …).
- **client** — the Wails desktop app.
- **environment** — one running AgentTunnel daemon and the machine, filesystem, provider credentials, and state it owns.
- **thread** — the durable conversation and work history for a project.
- **turn** — one user-to-agent cycle.
- **worktree** — isolated git checkout a thread's agents run in.
- **lead / orchestrator** — the agent that plans and dispatches subagents.

## The two ways to hurt yourself

1. **Killing by pattern.** Never `pkill -f`, `pgrep | kill`, or kill a PID you
   found by matching a name, path, or worktree string. Your own agent process
   has this worktree's path in its argv, and this machine runs several dev
   servers at once. Kill only a PID you captured at spawn, or the owner of
   your port after confirming its working directory is your worktree.
2. **Writing to live user state.** A daemon in the wild owns its StateDir; a
   dev checkout may sit next to real AgentTunnel data. Never start a server
   against real state, never open it read-write, never clean it up. Reading
   and copying is fine (good test data); data flows one way.

## Working in the codebase

Everything is Go plus one TypeScript frontend. Layout (all under
`agenttunnel-go/`):

- `cmd/atd/` — daemon entrypoint (HTTP + WebSocket server, `tool` subcommand).
- `internal/contracts/` — wire types: roles, harnesses, commands, events.
- `internal/domain/` — event-sourced core: pure `Decider`, `Projector`, `Engine`.
- `internal/providers/` — provider abstraction: `Driver` + `ProcessAdapter` + registry.
- `internal/orchestrator/` — roles, dispatch protocol, dependency scheduler, spawn gating, peer mailbox.
- `internal/contextstore/` — shared notebook, per-thread context, inherited context, mailbox.
- `internal/transport/` — JSON-RPC over WebSocket; streamed thread events.
- `pkg/daemon/` — public assembly (engine + orchestrator + HTTP surface).
- `desktop/` — Wails v3 client (embeds the daemon; `frontend/` is React+Vite).
- Per-thread worktrees are the default workflow (`internal/worktree/`). Each
  project's worktrees live in a sibling `<repo>.worktrees/<slug>-<id8>` on
  branch `agenttunnel/<slug>-<id8>`. Every top-level thread in a git project
  gets one on its first turn; subthreads inherit their parent's. The Worktrees
  aside tab manages them. `thread.delete` keeps one unless `removeWorktree` is
  set. A declared worktree never resolves back to the project checkout.

Taste:

- Complexity belongs at the provider adapter boundary. Orchestration stays
  pure, UI stays dumb.
- Inferred types over annotations; comments describe how a thing is used, not
  every line of behavior.
- The server is event-sourced and its flows are observable. Tests wait on
  typed events and turn completion, never on sleeps. A test that needs a
  timeout to pass is wrong.
- If a rule here fights the task in front of you, say so loudly and get a
  human sign-off before breaking it.

## Verifying

- Build: `go build ./...` from `agenttunnel-go/`. Test: `go test ./... -count=1`.
- Prefer the smallest proof: targeted `go test ./internal/orchestrator/ -run TestName -count=1`,
  `go vet`, `gofmt -l` for the scope you changed.
- The full suite belongs to CI (`.github/workflows/agenttunnel-ci.yml`).
- Backend behavior changes ship with focused tests for that behavior.
- Don't verify with browsers or computer use unless the user explicitly agrees.
- Frontend changes: `desktop/build.sh` builds it; `npx tsc --noEmit` inside
  `desktop/frontend` typechecks.

## Pull requests

- Never make a PR unless the developer explicitly asks.
- Conventional commit titles, plain language: `fix(core): peer mailbox no longer drops lead messages`.
- Body: the problem in a sentence or two, then how you fixed it. End with the
  model and harness that did the work.
- One concern per PR. If the description says "also", split it.
- Releases: a pushed `v*` tag runs `.github/workflows/agenttunnel-release.yml`
  (Go builds for darwin/arm64, darwin/amd64, linux/amd64, windows/amd64 plus
  the install script).
- Plans and scratch files stay outside the worktree. A merged PR is the
  implementation record.
