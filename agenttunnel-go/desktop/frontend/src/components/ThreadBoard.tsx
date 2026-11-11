import { memo, useCallback, useMemo, useRef, useState } from "react";

/** The PR a thread's worktree branch has, from the daemon's pr.states. */
export type ThreadPR = {
  number: number;
  url: string;
  state: string;
  draft: boolean;
  /** approved | changes_requested | review_required | none (no decision yet). */
  reviewDecision: string;
};

/** What pr.states reports for a thread with a worktree. pr is absent without a PR. */
export type ThreadState = {
  pr?: ThreadPR;
  /** Uncommitted changes in the worktree, tracked or untracked. */
  dirty: boolean;
};

/** The thread fields the board reads. */
export type BoardThread = {
  id: string;
  title: string;
  updatedAt?: string;
  /** Filed by the user; lands the thread in the Archived column. */
  archived?: boolean;
  runningAgents?: number;
  worktree?: { branch: string } | null;
};

export type ThreadColumnId = "running" | "uncommitted" | "ready" | "review" | "idle" | "archived";

/** Columns in workflow order, with Archived last. Every thread lands in exactly one column. */
export const THREAD_COLUMNS: { id: ThreadColumnId; title: string }[] = [
  { id: "running", title: "Running" },
  { id: "uncommitted", title: "Uncommitted" },
  { id: "ready", title: "Ready to merge" },
  { id: "review", title: "In review" },
  { id: "idle", title: "Idle" },
  { id: "archived", title: "Archived" },
];

/**
 * Running wins over everything: a thread with live agents is never shown as
 * archived, waiting, or uncommitted. A thread the user archived is archived next.
 * Then uncommitted changes win over PR state. Otherwise an open PR is ready to
 * merge only when it is not a draft and its review decision is approved; any
 * other open PR is in review. A merged PR is archived. No PR, or a closed one, is idle.
 */
export function threadColumn(t: BoardThread, state: ThreadState | undefined): ThreadColumnId {
  if ((t.runningAgents ?? 0) > 0) return "running";
  if (t.archived) return "archived";
  if (state?.dirty) return "uncommitted";
  const pr = state?.pr;
  if (pr?.state === "merged") return "archived";
  if (pr?.state === "open") {
    return !pr.draft && pr.reviewDecision === "approved" ? "ready" : "review";
  }
  return "idle";
}

/** Groups threads into board columns, keeping the incoming (recency) order. */
export function groupThreads<T extends BoardThread>(
  threads: T[],
  states: Record<string, ThreadState>,
): Record<ThreadColumnId, T[]> {
  const out: Record<ThreadColumnId, T[]> = {
    running: [],
    uncommitted: [],
    ready: [],
    review: [],
    idle: [],
    archived: [],
  };
  for (const t of threads) out[threadColumn(t, states[t.id])].push(t);
  return out;
}

const DOT_CLASS: Record<ThreadColumnId, string> = {
  running: "running",
  uncommitted: "pending",
  archived: "succeeded",
  ready: "succeeded",
  review: "pending",
  idle: "pending",
};

/** Stops a click on a card control from also opening the thread. */
const stop = (e: { stopPropagation: () => void }) => e.stopPropagation();

/**
 * One card. Memoized on its own props so a poll that changes one thread's PR
 * (or one thread's title) does not re-render the whole board. Every callback
 * must be stable, which is why the board passes handlers that never change.
 */
export const ThreadBoardCard = memo(function ThreadBoardCard({
  thread,
  pr,
  column,
  updated,
  selected,
  onOpen,
  onToggle,
  onArchive,
  onDelete,
}: {
  thread: BoardThread;
  pr: ThreadPR | undefined;
  column: ThreadColumnId;
  updated: string;
  selected: boolean;
  onOpen: (id: string) => void;
  onToggle: (id: string) => void;
  onArchive: (id: string, archived: boolean) => void;
  onDelete: (id: string) => void;
}) {
  const branch = thread.worktree?.branch;
  return (
    <div
      className={`thread-card tb-card${selected ? " selected" : ""}`}
      role="button"
      tabIndex={0}
      aria-pressed={selected}
      onClick={(e) => {
        // Cmd/Ctrl- or Shift-click selects for a bulk action; a plain click opens.
        if (e.metaKey || e.ctrlKey || e.shiftKey) onToggle(thread.id);
        else onOpen(thread.id);
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onOpen(thread.id);
        }
      }}
    >
      <div className="tc-head">
        <span className={`dot ${DOT_CLASS[column]}`} aria-hidden="true" />
        <div className="tc-title" title={thread.title}>
          {thread.title}
        </div>
      </div>
      <div className="tb-badges">
        {branch && (
          <span className="tb-branch" title={branch}>
            {branch}
          </span>
        )}
        {pr && (
          <a
            className={`tb-pr ${pr.state}${pr.draft ? " draft" : ""}`}
            href={pr.url}
            title={`PR #${pr.number} · ${pr.state}`}
            onClick={stop}
          >
            #{pr.number} {pr.draft ? "draft" : pr.state}
          </a>
        )}
      </div>
      <div className="tc-foot">
        {updated && <span className="tc-updated">Updated {updated}</span>}
        <span className="tb-card-actions">
          <button
            type="button"
            onClick={(e) => {
              stop(e);
              onArchive(thread.id, column !== "archived");
            }}
          >
            {column === "archived" ? "Restore" : "Archive"}
          </button>
          <button
            type="button"
            className="danger-text"
            onClick={(e) => {
              stop(e);
              onDelete(thread.id);
            }}
          >
            Delete
          </button>
        </span>
      </div>
    </div>
  );
});

/**
 * The board: one column per thread state, in workflow order, plus a bulk bar
 * for selected threads. Presentation only; the parent supplies threads, PR
 * state, and the actions. Deleting asks for a second click, like the rename
 * and delete dialogs elsewhere in the app.
 */
export function ThreadBoard<T extends BoardThread>({
  threads,
  states,
  formatUpdated,
  onOpen,
  onArchive,
  onDelete,
}: {
  threads: T[];
  states: Record<string, ThreadState>;
  formatUpdated: (iso?: string) => string;
  onOpen: (id: string) => void;
  onArchive: (ids: string[], archived: boolean) => void;
  onDelete: (ids: string[]) => void;
}) {
  const columns = useMemo(() => groupThreads(threads, states), [threads, states]);
  const [selected, setSelected] = useState<ReadonlySet<string>>(() => new Set());
  const [pendingDelete, setPendingDelete] = useState<string[] | null>(null);

  // Card callbacks read the latest handlers from a ref so they never change
  // identity, which keeps memoized cards from re-rendering on every parent update.
  const handlers = useRef({ onArchive, onDelete });
  handlers.current = { onArchive, onDelete };

  const toggle = useCallback((id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);
  const archiveCard = useCallback((id: string, archived: boolean) => {
    handlers.current.onArchive([id], archived);
  }, []);
  const askDeleteCard = useCallback((id: string) => setPendingDelete([id]), []);

  const selectedIds = [...selected];
  const clearSelection = () => {
    setSelected(new Set());
    setPendingDelete(null);
  };
  const archiveSelected = () => {
    onArchive(selectedIds, true);
    clearSelection();
  };
  const confirmDelete = () => {
    if (!pendingDelete) return;
    onDelete(pendingDelete);
    setSelected((prev) => new Set([...prev].filter((id) => !pendingDelete.includes(id))));
    setPendingDelete(null);
  };

  const count = pendingDelete?.length ?? 0;
  return (
    <>
      {(selected.size > 0 || pendingDelete) && (
        <div className="tb-bar" role="toolbar" aria-label="Selected threads">
          {pendingDelete ? (
            <>
              <span>
                Delete {count} thread{count === 1 ? "" : "s"}? They leave the board. Worktrees are kept.
              </span>
              <button type="button" onClick={() => setPendingDelete(null)}>
                Cancel
              </button>
              <button type="button" className="danger" onClick={confirmDelete}>
                Delete
              </button>
            </>
          ) : (
            <>
              <span>{selected.size} selected · Cmd- or Shift-click cards to change the selection</span>
              <button type="button" onClick={archiveSelected}>
                Archive
              </button>
              <button type="button" className="danger-text" onClick={() => setPendingDelete(selectedIds)}>
                Delete…
              </button>
              <button type="button" onClick={clearSelection}>
                Clear
              </button>
            </>
          )}
        </div>
      )}
      <div className="thread-board">
        {THREAD_COLUMNS.map((col) => {
          const items = columns[col.id];
          return (
            <section className={`tb-col tb-col-${col.id}`} key={col.id} aria-label={col.title}>
              <header className="tb-col-head">
                {col.title} <span className="tb-count">{items.length}</span>
              </header>
              <div className="tb-cards">
                {items.length === 0 && <div className="empty">No threads</div>}
                {items.map((t) => (
                  <ThreadBoardCard
                    key={t.id}
                    thread={t}
                    pr={states[t.id]?.pr}
                    column={col.id}
                    updated={formatUpdated(t.updatedAt)}
                    selected={selected.has(t.id)}
                    onOpen={onOpen}
                    onToggle={toggle}
                    onArchive={archiveCard}
                    onDelete={askDeleteCard}
                  />
                ))}
              </div>
            </section>
          );
        })}
      </div>
    </>
  );
}
