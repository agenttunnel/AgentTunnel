---
layout: default
title: Desktop app
description: The desktop app embeds the daemon and is the main way to use AgentTunnel.
---


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

