---
layout: default
title: Orchestrator
description: The orchestrator plans a turn and dispatches subagents to carry it out.
---


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

