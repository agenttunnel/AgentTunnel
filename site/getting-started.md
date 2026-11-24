---
layout: default
title: Quickstart
description: Run one task from install to review, then stop it when you need to.
---

This walks through one task from install to review. It assumes you have [installed AgentTunnel](install.html).

## 1. Check your provider CLIs

AgentTunnel drives provider CLIs that you install and sign in to yourself. Check what the daemon can see:

```sh
agenttunnel harness ls
```

Each harness shows whether its binary is available and whether it is enabled. The built-in harnesses and their binaries are:

| Harness | Binary |
| --- | --- |
| OpenCode | `opencode` |
| Claude | `claude` |
| Codex | `codex` |
| Pi | `pi` |
| Command Code | `commandcode` |

The default harness for the orchestrator and executor roles is OpenCode, so install it first, or point those roles at a harness you have (see [Roles and harnesses](roles-and-harnesses.html)).

## 2. Open a project

A project is a directory on your machine. Any of these registers it:

```sh
agenttunnel-desktop ~/code/my-project     # opens the desktop app on a new thread in that directory
agenttunnel desktop ~/code/my-project     # the same, through the CLI
agenttunnel projects add ~/code/my-project --name my-project
```

An unknown directory becomes a new project, named after the directory. A known one (including through a symlink) reuses its existing project. If the desktop app is already running, the request is forwarded to it.

To list or remove projects:

```sh
agenttunnel projects ls
agenttunnel projects rm <id-or-path>
```

## 3. Start a thread

Create a thread in the project and send a task in the composer. The first turn starts the **orchestrator**, which plans the work and dispatches subagents.

If the project is a git repository, the thread gets its own worktree on a new branch, so the agents never edit your checkout directly. See [Worktrees](worktrees.html).

A thread is saved only once something happens in it. Creating one and leaving it alone leaves nothing behind.

## 4. Watch it run

- The **Agents** panel lists every agent in the thread, its role, and its status.
- The **Graph** tab shows who spawned whom and the messages passed between agents. Press `Alt+4` (`Option+4` on macOS).
- From a terminal, `agenttunnel ps` lists threads and what their agents are doing, and `agenttunnel logs <thread>` prints the output. Add `-f` to follow it.

## 5. Review the diff

When the agents finish, open the **Review** tab (`Alt+2`). It shows the worktree's diff. You can leave line comments on it and submit them. Submitting sends your comments back to the agents as a new turn, so the agents fix what you flagged.

## 6. Stop a run

Stop the turn from the app, or from a terminal:

```sh
agenttunnel stop <thread>            # stop the running turn
agenttunnel stop <thread> --agents   # stop the agents as well
```

## Run a job from a file

A job is a Markdown file: the body is the task, and optional YAML at the top pins models per role. Create a starter file:

```sh
agenttunnel template refactor-auth.md
agenttunnel run refactor-auth.md          # starts the job as a background thread and returns
agenttunnel run refactor-auth.md -f       # starts it and follows the output
```

`agenttunnel run` also accepts a name without the extension, and looks in the current directory. For scripts, `--wait --json` blocks until the turn ends and prints one JSON result. `--timeout` bounds the wait (default 30m, `0` for no limit).

## Next

- [Threads](threads.html) and [Worktrees](worktrees.html): how a turn runs and where its files live.
- [Orchestrator](orchestrator.html): how subagents are planned and dispatched.
- [CLI](cli.html): every command and flag.
- [Configuration](configuration.html): settings and the state directory.
