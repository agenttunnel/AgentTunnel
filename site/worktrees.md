---
layout: default
title: Worktrees
description: Each thread in a git project runs in its own worktree, so agents never edit your checkout.
---


Each top-level thread in a git project gets its own **git worktree**: a separate checkout on a new branch, cut from the repository's default branch. Agents work there, so your own checkout is never switched or edited by an agent.

- Worktrees live in a sibling directory, `<repo>.worktrees/`, on a branch named `agenttunnel/<name>`.
- The worktree is created on the thread's first turn. Subthreads share their parent's worktree.
- Files that git ignores (`node_modules`, `.env`, build output) are not in a new worktree. Run your project's setup step in it before agents need those files.
- Projects that are not git repositories run in the project directory.
- The **Worktrees** page lists them with live git facts. Removing a dirty worktree needs force. A branch is deleted only when git considers it merged, so unmerged work is kept.

