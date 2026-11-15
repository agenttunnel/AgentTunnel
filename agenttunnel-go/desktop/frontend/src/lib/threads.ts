// Thread-list layout and search: pure data, no React. A subthread sits directly
// under the thread that started it, one level deeper. The daemon already sorts
// the list by submission time, newest first; this keeps that order among
// siblings and only moves the subthreads.

import { fuzzyScore } from "./fuzzy";

/** A thread row with its depth in the subthread tree (0 at the top level) and
 * how many subthreads it started directly. */
export type ThreadRow<T> = T & { depth: number; subthreads: number };

/**
 * The rows the sidebar shows: a collapsed thread's subthreads, at any depth,
 * are left out. Rows arrive depth-first, so a collapsed row hides everything
 * after it that is deeper, up to the next row at its own level.
 */
export function visibleThreadRows<T>(rows: ThreadRow<T>[], collapsed: ReadonlySet<string>, idOf: (t: T) => string): ThreadRow<T>[] {
  const out: ThreadRow<T>[] = [];
  let hideBelow: number | null = null;
  for (const row of rows) {
    if (hideBelow !== null) {
      if (row.depth > hideBelow) continue;
      hideBelow = null;
    }
    out.push(row);
    if (row.subthreads > 0 && collapsed.has(idOf(row))) hideBelow = row.depth;
  }
  return out;
}

/**
 * Orders threads so each subthread follows its parent, indented. A subthread
 * whose parent is not in the list (filtered out, or deleted) shows at the top
 * level, so no thread is ever hidden by the tree.
 */
export function arrangeThreads<T extends { id: string; parentThreadId?: string }>(
  list: T[],
): ThreadRow<T>[] {
  const ids = new Set(list.map((t) => t.id));
  const children = new Map<string, T[]>();
  const roots: T[] = [];
  for (const t of list) {
    const parent = t.parentThreadId;
    if (parent && parent !== t.id && ids.has(parent)) {
      const siblings = children.get(parent) ?? [];
      siblings.push(t);
      children.set(parent, siblings);
    } else {
      roots.push(t);
    }
  }

  const out: ThreadRow<T>[] = [];
  const placed = new Set<string>();
  const place = (t: T, depth: number) => {
    if (placed.has(t.id)) return;
    placed.add(t.id);
    out.push({ ...t, depth, subthreads: children.get(t.id)?.length ?? 0 });
    for (const child of children.get(t.id) ?? []) place(child, depth + 1);
  };
  for (const t of roots) place(t, 0);
  // A parent cycle could leave threads unreached from any root; surface them at
  // the top level rather than dropping them.
  for (const t of list) place(t, 0);
  return out;
}

/** The thread fields search reads. The project name is passed separately so a
 * single-project list (the sidebar) can leave it out and avoid repeating it on
 * every row. */
export type SearchableThread = {
  title: string;
  firstPrompt?: string;
};

// A prompt hit must be stronger than a title hit to win, so a thread whose
// title matches leads a thread that only mentions the query in its prompt. The
// project name is a looser signal: useful across projects, never enough on its
// own to outrank a title.
const PROMPT_WEIGHT = 0.7;
const PROJECT_WEIGHT = 0.6;

/**
 * The best fuzzy match of `query` in a thread's title, first prompt, or project
 * name. Null means the thread does not match.
 */
export function threadSearchScore(
  t: SearchableThread,
  query: string,
  projectName?: string,
): number | null {
  const title = fuzzyScore(query, t.title);
  const prompt = t.firstPrompt ? fuzzyScore(query, t.firstPrompt) : null;
  const project = projectName ? fuzzyScore(query, projectName) : null;
  if (title === null && prompt === null && project === null) return null;
  return Math.max(
    title ?? -Infinity,
    (prompt ?? -Infinity) * PROMPT_WEIGHT,
    (project ?? -Infinity) * PROJECT_WEIGHT,
  );
}
