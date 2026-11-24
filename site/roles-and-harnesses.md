---
layout: default
title: Roles and harnesses
description: Roles are the jobs in a team. Harnesses are the provider CLIs that run them.
---


## Roles

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

## Harnesses

A **harness** is a provider CLI that runs an agent. The six built-in harnesses are OpenCode, Claude, Codex, Pi, and Command Code. Each is started in its non-interactive mode, with the prompt passed as an argument. Agents therefore edit files and run commands in the worktree without asking first. Use worktrees, and review the diff before you merge it.

Set a harness and model per role in the app, or per job in a job template ([job templates](job-templates.html)). Harnesses that the daemon can't find are marked unavailable. Disable one with `disabledHarnesses`.

