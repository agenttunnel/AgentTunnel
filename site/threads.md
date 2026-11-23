---
layout: default
title: Threads
description: A thread is one conversation about one project. Each message you send is a turn.
---


A thread is one conversation about one project: the task, every agent's output, and the work they produced. Each message you send is a **turn**. A turn runs the orchestrator, which may dispatch subagents, and ends when the orchestrator finishes.

- **Create** from the app, or with `agenttunnel open <dir>` or `agenttunnel-desktop <dir>`.
- **Steer** a running turn with `thread.steer`. The orchestrator reads the steer on its next pass.
- **Stop** a turn with `agenttunnel stop <thread>`. Add `--agents` to stop the subagents too.
- **Side question.** `thread.btw` asks a question answered from a snapshot of the thread. It is not recorded in the transcript and does not steer the turn.
- **Fork** copies a thread so you can branch from its state.
- **Delete** removes the thread. Its worktree stays unless you also remove it.

Threads are saved to disk as they change, so they survive a restart of the daemon. See [Configuration](configuration.html).

