import type { ThreadPR, ThreadState } from "../components/ThreadBoard";

/** One git worktree a project's threads run in (worktree.list). Git facts are
 * live: dirty/ahead/behind come from the repo at request time. */
export type WorktreeInfo = {
  path: string;
  branch: string;
  base: string;
  threadIds: string[];
  exists: boolean;
  dirty: boolean;
  ahead: number;
  behind: number;
  prunable: boolean;
};

export type WorktreeProject = { id: string; name: string; path?: string };
export type WorktreeThread = { id: string; title: string };

/** What a worktree is worth calling out for. `merged` is the only state that is
 * safe to clear without asking: the branch's pull request already landed. */
export type Tone =
  | "merged"
  | "merged-dirty"
  | "review"
  | "draft"
  | "closed"
  | "dirty"
  | "clean"
  | "missing";

/** Merged worktrees lead their project, then work that needs a human. */
export const TONE_RANK: Record<Tone, number> = {
  merged: 0,
  "merged-dirty": 1,
  dirty: 2,
  review: 3,
  draft: 4,
  closed: 5,
  clean: 6,
  missing: 7,
};

/** A worktree can be owned by more than one top-level thread. A merged PR wins
 * over an open one, which wins over a closed one, so the strongest signal
 * decides whether the checkout can go. */
export function prOf(
  worktree: Pick<WorktreeInfo, "threadIds">,
  states: Record<string, ThreadState>,
): ThreadPR | undefined {
  let open: ThreadPR | undefined;
  let closed: ThreadPR | undefined;
  for (const id of worktree.threadIds) {
    const pr = states[id]?.pr;
    if (!pr) continue;
    if (pr.state === "merged") return pr;
    if (pr.state === "open" && !open) open = pr;
    if (!closed) closed = pr;
  }
  return open ?? closed;
}

export function toneOf(
  worktree: Pick<WorktreeInfo, "exists" | "prunable" | "dirty">,
  pr: ThreadPR | undefined,
): Tone {
  if (!worktree.exists || worktree.prunable) return "missing";
  if (pr?.state === "merged") return worktree.dirty ? "merged-dirty" : "merged";
  if (pr?.state === "open") return pr.draft ? "draft" : "review";
  if (pr?.state === "closed") return "closed";
  return worktree.dirty ? "dirty" : "clean";
}

export function badgeLabel(tone: Tone, pr: ThreadPR | undefined): string {
  switch (tone) {
    case "merged":
      return "Merged · ready to prune";
    case "merged-dirty":
      return "Merged · has changes";
    case "review":
      return pr?.reviewDecision === "approved" ? "PR approved" : "PR in review";
    case "draft":
      return "Draft PR";
    case "closed":
      return "PR closed";
    case "dirty":
      return "Uncommitted changes";
    case "clean":
      return "Clean";
    case "missing":
      return "Directory missing";
  }
}

export type WorktreeRow = {
  project: WorktreeProject;
  worktree: WorktreeInfo;
  pr: ThreadPR | undefined;
  tone: Tone;
};

/** One row per loaded worktree, with its tone from live git facts and the last
 * PR snapshot. Projects keep the order they were given. */
export function worktreeRows(
  projects: WorktreeProject[],
  worktrees: Record<string, WorktreeInfo[] | undefined>,
  states: Record<string, ThreadState>,
): WorktreeRow[] {
  const out: WorktreeRow[] = [];
  for (const project of projects) {
    for (const worktree of worktrees[project.id] ?? []) {
      const pr = prOf(worktree, states);
      out.push({ project, worktree, pr, tone: toneOf(worktree, pr) });
    }
  }
  return out;
}

/** Merged and clean: the daily prompt can remove these without asking again. */
export function pruneReady(rows: WorktreeRow[]): WorktreeRow[] {
  return rows.filter((r) => r.tone === "merged");
}
