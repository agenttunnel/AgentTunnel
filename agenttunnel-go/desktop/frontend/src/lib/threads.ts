// Thread-list layout: pure ordering, no React. A subthread sits directly under
// the thread that started it, one level deeper. The daemon already sorts the
// list by recency; this keeps that order among siblings and only moves the
// subthreads.

/** A thread row with its depth in the subthread tree (0 at the top level). */
export type ThreadRow<T> = T & { depth: number };

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
    out.push({ ...t, depth });
    for (const child of children.get(t.id) ?? []) place(child, depth + 1);
  };
  for (const t of roots) place(t, 0);
  // A parent cycle could leave threads unreached from any root; surface them at
  // the top level rather than dropping them.
  for (const t of list) place(t, 0);
  return out;
}
