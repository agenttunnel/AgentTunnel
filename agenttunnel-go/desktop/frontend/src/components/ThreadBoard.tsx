import { memo, useMemo } from "react";

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
  /** Who started this thread; absent (older threads) means user. */
  owner?: "user" | "orchestrator";
  updatedAt?: string;
  runningAgents?: number;
  worktree?: { branch: string } | null;
};

export type ThreadColumnId = "running" | "uncommitted" | "archived" | "ready" | "review" | "idle";

/** Columns in workflow order. Every thread lands in exactly one column. */
export const THREAD_COLUMNS: { id: ThreadColumnId; title: string }[] = [
  { id: "running", title: "Running" },
  { id: "uncommitted", title: "Uncommitted" },
  { id: "archived", title: "Archived" },
  { id: "ready", title: "Ready to merge" },
  { id: "review", title: "In review" },
  { id: "idle", title: "Idle" },
];

/**
 * Running wins over everything: a thread with live agents is never shown as
 * archived, waiting, or uncommitted. Then uncommitted changes win over PR state.
 * Otherwise an open PR is ready to merge only when it is not a draft and its
 * review decision is approved; any other open PR is in review. A merged PR is
 * archived. No PR, or a closed one, is idle.
 */
export function threadColumn(t: BoardThread, state: ThreadState | undefined): ThreadColumnId {
  if ((t.runningAgents ?? 0) > 0) return "running";
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
    archived: [],
    ready: [],
    review: [],
    idle: [],
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

/**
 * One card. Memoized on its own props so a poll that changes one thread's PR
 * (or one thread's title) does not re-render the whole board. onOpen must be
 * stable, which is why the parent passes a useCallback.
 */
export const ThreadBoardCard = memo(function ThreadBoardCard({
  thread,
  pr,
  column,
  updated,
  onOpen,
}: {
  thread: BoardThread;
  pr: ThreadPR | undefined;
  column: ThreadColumnId;
  updated: string;
  onOpen: (id: string) => void;
}) {
  const branch = thread.worktree?.branch;
  return (
    <div
      className="thread-card tb-card"
      role="button"
      tabIndex={0}
      onClick={() => onOpen(thread.id)}
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
        {thread.owner === "orchestrator" && (
          <span className="tc-owner" title="Started by the orchestrator">
            Orchestrator
          </span>
        )}
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
            onClick={(e) => e.stopPropagation()}
          >
            #{pr.number} {pr.draft ? "draft" : pr.state}
          </a>
        )}
      </div>
      <div className="tc-foot">
        {updated && <span className="tc-updated">Updated {updated}</span>}
      </div>
    </div>
  );
});

/**
 * The board: one column per thread state, in workflow order. Presentation only; the
 * parent supplies threads, PR state, and the open handler.
 */
export function ThreadBoard<T extends BoardThread>({
  threads,
  states,
  formatUpdated,
  onOpen,
}: {
  threads: T[];
  states: Record<string, ThreadState>;
  formatUpdated: (iso?: string) => string;
  onOpen: (id: string) => void;
}) {
  const columns = useMemo(() => groupThreads(threads, states), [threads, states]);
  return (
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
                  onOpen={onOpen}
                />
              ))}
            </div>
          </section>
        );
      })}
    </div>
  );
}
