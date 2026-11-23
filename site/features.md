---
layout: default
title: Features
---

# Features

This page covers each part of AgentTunnel and how to use it. For exact flags, see the [CLI reference](cli.html).

## Desktop app

The desktop app embeds the daemon. Its window has three areas:

- **Sidebar**: projects and their threads. `/` jumps to the thread filter.
- **Center**: the open thread. Switch tabs with `Alt`+digit (`Option`+digit on macOS):

  | Key | Tab | Shows |
  | --- | --- | --- |
  | `Alt+1` | Chat | the transcript and composer |
  | `Alt+2` | Review | the worktree diff and review comments |
  | `Alt+3` | Files | the thread's worktree files |
  | `Alt+4` | Graph | agents, spawn links, and messages between them |
  | `Alt+5` | P2P | messages between agents ("Agent messages") |
  | `Alt+6` | Board | the project's threads, in columns |

- **Aside** (right side): pinned **Agents** and **Token usage**, plus optional panels for Context, Inherited context, Diff, Pull request, Harness stats, Models, and Council.

Project-level pages sit above the threads: **Projects**, **Orchestrator** (the master chat), **Worktrees**, and **Stats**. `Ctrl+K` (`Cmd+K` on macOS) or `Ctrl+P` opens the command palette. `Ctrl+S` (`Cmd+S`) toggles compact mode on the thread screen.

## Threads

A thread is one conversation about one project: the task, every agent's output, and the work they produced. Each message you send is a **turn**. A turn runs the orchestrator, which may dispatch subagents, and ends when the orchestrator finishes.

- **Create** from the app, or with `agenttunnel open <dir>` or `agenttunnel-desktop <dir>`.
- **Steer** a running turn with `thread.steer`. The orchestrator reads the steer on its next pass.
- **Stop** a turn with `agenttunnel stop <thread>`. Add `--agents` to stop the subagents too.
- **Side question.** `thread.btw` asks a question answered from a snapshot of the thread. It is not recorded in the transcript and does not steer the turn.
- **Fork** copies a thread so you can branch from its state.
- **Delete** removes the thread. Its worktree stays unless you also remove it.

Threads are saved to disk as they change, so they survive a restart of the daemon. See [Configuration](configuration.html).

## Worktrees

Each top-level thread in a git project gets its own **git worktree**: a separate checkout on a new branch, cut from the repository's default branch. Agents work there, so your own checkout is never switched or edited by an agent.

- Worktrees live in a sibling directory, `<repo>.worktrees/`, on a branch named `agenttunnel/<name>`.
- The worktree is created on the thread's first turn. Subthreads share their parent's worktree.
- Files that git ignores (`node_modules`, `.env`, build output) are not in a new worktree. Run your project's setup step in it before agents need those files.
- Projects that are not git repositories run in the project directory.
- The **Worktrees** page lists them with live git facts. Removing a dirty worktree needs force. A branch is deleted only when git considers it merged, so unmerged work is kept.

## Orchestrator and subagents

The orchestrator is the team lead. On each pass it:

1. reads the task and the thread's shared notebook,
2. dispatches jobs to subagents, with `--depends-on` naming the jobs a job waits for,
3. runs jobs in waves as their dependencies finish, up to the concurrency limit (`atd --concurrency`, default 4), and
4. feeds the results into the next pass.

It runs at most `atd --max-passes` passes per turn (default 3), then writes a final summary.

Subagents can talk back while they run. A subagent sends a message to the lead or a peer with the agent tool command `tool message`. Messages are stored in the thread and shown as arrows in the Graph tab.

- **Peer messaging** is on for new threads. Turn it off per thread in the thread's setup, or with `thread.setPeerMailbox`.
- **Sub-spawns** are gated. A subagent proposes a helper with `tool spawn`. The proposal waits until the lead runs `tool approve` or `tool reject`. An approved helper runs under the agent that proposed it.
- **Questions.** An agent that needs a decision only you can make runs `tool ask`. The app shows the question. Your answer goes back to the agent, and the turn continues.
- **Council.** `thread.council.start` runs a set of roles through parallel rounds of debate and then synthesizes an outcome. Its results show in the Council panel.

The `tool` commands run inside agent sessions. You don't need to run them yourself.

## Roles and harnesses

### Roles

A role is a job in the team: its name, its system prompt, and the harness and model it runs on. The built-in roles are:

| Role | ID | Notes |
| --- | --- | --- |
| Orchestrator | `orchestrator` | The team lead. Required. |
| Planner | `planner` | Researches a sub-problem and returns an ordered plan. Does not edit files. |
| Executor | `executor` | Implements the assigned task, runs checks, and reports what changed. Required. |
| QA Tester | `tester` | Runs tests and edge cases and reports failures. Does not fix code. |
| Reviewer | `reviewer` | Reviews the executors' diff. Posts findings as review threads the executors answer. |
| Facilitator | `facilitator` | Runs council debates: states where members agree and split, and a recommendation. |
| Context Manager | `context-manager` | Writes a short summary of the subagent results so far. Experimental: usable only when `experimentalRoles` is on. |
| Wildcard | `wildcard` | A custom agent the lead defines from a brief. Experimental: usable only when `experimentalRoles` is on. |

Disable a role with the `disabledRoles` setting. The orchestrator and executor are required and cannot be disabled.

### Harnesses

A **harness** is a provider CLI that runs an agent. The six built-in harnesses are OpenCode, Claude, Codex, Cursor, Pi, and Command Code. Each is started in its non-interactive mode, with the prompt passed as an argument. Agents therefore edit files and run commands in the worktree without asking first. Use worktrees, and review the diff before you merge it.

Set a harness and model per role in the app, or per job in a job template ([Getting started](getting-started.html#run-a-job-from-a-file)). Harnesses that the daemon can't find are marked unavailable. Disable one with `disabledHarnesses`.

## Models

Each harness reports the models it can use. OpenCode, Pi, Command Code, and Cursor list their models through their own CLIs. Claude and Codex have no usable listing command, so add their models yourself:

```sh
agenttunnel models ls                   # every known model, with its variants and role defaults
agenttunnel models ls --harness claude
```

Add a model in the app's Models panel, or through `models.add`. Refresh discovered lists with `models.refresh`.

**Auto mode** lets the orchestrator choose each task's model. It only picks from models you have approved for that harness (`approvedByHarness` in settings). Turn it on per thread with `thread.setAutoMode`.

Variants (for example a reasoning level) are chosen per role or per job, where the harness supports them.

## Schedules

A thread can be scheduled to start after other threads finish. Pick the threads it depends on and write its prompt. When every dependency has finished its turn, the scheduled thread starts. The results of its dependencies are added to its prompt.

- Create a schedule in the thread's setup in the desktop app, or with `thread.schedule` (`threadId`, `dependsOn`, `prompt`).
- Remove it with `thread.unschedule`.
- A schedule survives a daemon restart. If a dependency finished while the daemon was down, the schedule fires once the daemon is back.
- A thread cannot depend on itself, directly or through a chain of schedules. The daemon refuses that schedule.

Scheduling has no `agenttunnel` command. Use the desktop app or the RPC.

## Job templates

A **job template** is a Markdown file. The body is the task. Optional YAML at the top sets the thread name, the project, and model overrides per role:

```markdown
---
name: Fix flaky login test
project: my-project
modelConfig:
  executor:
    harness: codex
    model: gpt-5-codex
    variant: high
---

Find out why the login test fails intermittently and fix it.
```

Create a starter file with `agenttunnel template`, then start it with `agenttunnel run`. Unknown YAML keys are an error, so a typo does not pass silently.

## Usage and cost

The **Token usage** panel shows tokens and cost for the thread, split between the orchestrator and subagents. `agenttunnel usage` prints the same figures from a terminal. Add `--limits` to show your subscription limits where the provider exposes them (Claude and Command Code).

## Terminal UI

`agenttunnel tui` opens a full-screen terminal interface on the same daemon. `agenttunnel tui <thread>` opens straight into a thread.

| Key | Does |
| --- | --- |
| `enter` | send the prompt |
| `alt+enter` | new line |
| `↑` `↓` | prompt history, or the options of a question an agent asked |
| `ctrl+g` | edit the prompt in `$VISUAL` or `$EDITOR` |
| `@` | search the thread's files, then `tab` to attach a path to the turn |
| `tab` `shift+tab` | move between views (Files, Review, PR, and so on) |
| `pgup` `pgdn` | scroll the transcript |
| `ctrl+k` | command palette |
| `ctrl+b` | show or hide the sidebar |
| `esc` | back to the thread list |

On the thread list, `/` filters, `n` starts a new thread, and `r` refreshes. `q` quits.

## Web client

`agenttunnel web [dir]` serves the web UI on `127.0.0.1`, and opens it in your browser. It prefers port 7878 and uses another free port if that one is taken. If a daemon is already running, the web UI attaches to it. Otherwise it starts one for the session. The web UI listens on loopback only.

## Pull requests and issues

The **Pull request** panel and the `pr.*` and `issues.list` RPCs use your installed `gh` (GitHub) or `glab` (GitLab) CLI. They are not available unless that CLI is installed and signed in.

## WhatsApp

AgentTunnel can post workflow updates to WhatsApp. Link it with:

```sh
agenttunnel whatsapp connect
```

This shows a QR code in the terminal to scan from WhatsApp. Without configuration, the daemon links as a companion device and keeps its session in the state directory. To use the WhatsApp Cloud API instead, set `AT_WHATSAPP_TOKEN`, `AT_WHATSAPP_PHONE_NUMBER_ID`, and `AT_WHATSAPP_TO`. See [Configuration](configuration.html#environment-variables).

## Remote access

By default `atd` listens on `127.0.0.1` only, and its pairing URL carries the token. There are two ways to reach a daemon from elsewhere.

### Bind to the network

`atd --host 0.0.0.0 --token <secret>` listens on the network. When the daemon is not on loopback, it doesn't print the token in its log. Clients send the token as an `Authorization: Bearer` header. Use this only on a network you trust, or behind TLS.

### Relay

The relay lets a phone reach a daemon that has no inbound port:

- `atrelay` is a self-hosted relay. Run it on a host you control. The relay operator can read the traffic it routes, so don't use someone else's relay.
- The daemon only dials out, and the relay is **off by default**. In the desktop app, open **Remote** in the sidebar footer and enter your relay's URL. Or start `atd` with `--relay wss://your-relay.example`.
- Plain `ws://` is accepted only for loopback and private addresses. For the internet, put `atrelay` behind a TLS proxy and use `wss://`.

```sh
atrelay --listen 127.0.0.1:7777                # loopback, for testing
atrelay --listen 0.0.0.0:7777                  # the LAN; prints the URL to use
```

The phone app lives in `agenttunnel/mobile`. It is an Expo app that you build from source. There are no published store builds yet.

See [`docs/relay-protocol.md`](https://github.com/agenttunnel/AgentTunnel/blob/main/agenttunnel/docs/relay-protocol.md) for the wire protocol.
