---
layout: default
title: Schedules
description: Start a thread automatically once the threads it depends on have finished.
---

## Schedules

A thread can be scheduled to start after other threads finish. Pick the threads it depends on and write its prompt. When every dependency has finished its turn, the scheduled thread starts. The results of its dependencies are added to its prompt.

- Create a schedule in the thread's setup in the desktop app, or with `thread.schedule` (`threadId`, `dependsOn`, `prompt`).
- Remove it with `thread.unschedule`.
- A schedule survives a daemon restart. If a dependency finished while the daemon was down, the schedule fires once the daemon is back.
- A thread cannot depend on itself, directly or through a chain of schedules. The daemon refuses that schedule.

Scheduling has no `agenttunnel` command. Use the desktop app or the RPC.

