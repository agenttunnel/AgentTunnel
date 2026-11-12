import { memo, useCallback, useMemo, useRef, useState, type DragEvent } from "react";

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
  runningAgents?: number;
  worktree?: { branch: string } | null;
  /** A draft has not had its first turn yet: no worktree and no agents. */
  draft?: boolean;
};

/** A GitLab issue assigned to the user, as issues.list reports it. */
export type BoardIssue = {
  iid: number;
  title: string;
  description: string;
  webUrl: string;
  updatedAt?: string;
};

/** What issues.list reports for a project. Issues is empty unless status is ready. */
export type BoardIssues = {
  status: string;
  message?: string;
  issues: BoardIssue[];
};

export type ThreadColumnId =
  | "draft"
  | "running"
  | "uncommitted"
  | "archived"
  | "ready"
  | "review"
  | "idle";

/** Columns in workflow order. Every thread lands in exactly one column. */
export const THREAD_COLUMNS: { id: ThreadColumnId; title: string }[] = [
  { id: "draft", title: "Draft" },
  { id: "running", title: "Running" },
  { id: "uncommitted", title: "Uncommitted" },
  { id: "archived", title: "Archived" },
  { id: "ready", title: "Ready to merge" },
  { id: "review", title: "In review" },
  { id: "idle", title: "Idle" },
];

/**
 * A draft is waiting for its first turn, so it is in Draft before anything else:
 * it has no agents, no worktree, and no PR. Otherwise running wins over
 * everything: a thread with live agents is never shown as archived, waiting, or
 * uncommitted. Then uncommitted changes win over PR state. Otherwise an open PR
 * is ready to merge only when it is not a draft and its review decision is
 * approved; any other open PR is in review. A merged PR is archived. No PR, or a
 * closed one, is idle.
 */
export function threadColumn(t: BoardThread, state: ThreadState | undefined): ThreadColumnId {
  if (t.draft) return "draft";
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
    draft: [],
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

/** What can be dragged onto the board: a draft card, or a GitLab issue. */
export type DragKind = "draft" | "issue";

/**
 * A draft may only be dragged to Running, which starts its first turn. An issue
 * may only be dropped on Draft, which creates a draft from it. Nothing else is a
 * drop target.
 */
export function canDrop(kind: DragKind, to: ThreadColumnId): boolean {
  return kind === "draft" ? to === "running" : to === "draft";
}

const DOT_CLASS: Record<ThreadColumnId, string> = {
  draft: "pending",
  running: "running",
  uncommitted: "pending",
  archived: "succeeded",
  ready: "succeeded",
  review: "pending",
  idle: "pending",
};

type DragPayload = { kind: "draft"; id: string } | { kind: "issue"; issue: BoardIssue };

/**
 * One card. Memoized on its own props so a poll that changes one thread's PR
 * (or one thread's title) does not re-render the whole board. Every handler must
 * be stable, which is why the parent passes useCallbacks.
 */
export const ThreadBoardCard = memo(function ThreadBoardCard({
  thread,
  pr,
  column,
  updated,
  onOpen,
  onDragStart,
  onDragEnd,
}: {
  thread: BoardThread;
  pr: ThreadPR | undefined;
  column: ThreadColumnId;
  updated: string;
  onOpen: (id: string, draft: boolean) => void;
  onDragStart: (id: string, e: DragEvent) => void;
  onDragEnd: () => void;
}) {
  const branch = thread.worktree?.branch;
  const draft = !!thread.draft;
  return (
    <div
      className="thread-card tb-card"
      role="button"
      tabIndex={0}
      draggable={draft}
      onDragStart={draft ? (e) => onDragStart(thread.id, e) : undefined}
      onDragEnd={draft ? onDragEnd : undefined}
      onClick={() => onOpen(thread.id, draft)}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onOpen(thread.id, draft);
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

/** One GitLab issue. Dragging it onto Draft creates a draft from it. */
const IssueBoardCard = memo(function IssueBoardCard({
  issue,
  onDragStart,
  onDragEnd,
}: {
  issue: BoardIssue;
  onDragStart: (issue: BoardIssue, e: DragEvent) => void;
  onDragEnd: () => void;
}) {
  return (
    <div
      className="thread-card tb-card tb-issue"
      draggable
      onDragStart={(e) => onDragStart(issue, e)}
      onDragEnd={onDragEnd}
      title={issue.title}
    >
      <div className="tc-head">
        <span className="tb-issue-iid">#{issue.iid}</span>
        <div className="tc-title">{issue.title}</div>
      </div>
      <div className="tb-badges">
        <a
          className="tb-issue-link"
          href={issue.webUrl}
          title={issue.webUrl}
          draggable={false}
          onClick={(e) => e.stopPropagation()}
        >
          Open in GitLab
        </a>
      </div>
    </div>
  );
});

/** What the Issues column says when there are no issues to show. */
const ISSUE_FALLBACK: Record<string, string> = {
  unavailable: "glab is not installed.",
  unauthenticated: "Log in to GitLab with glab.",
  unsupported: "Issues are read from GitLab only.",
  error: "Could not load issues.",
};

/**
 * The board: an Issues source column, then one column per thread state in
 * workflow order. Presentation only: the parent supplies threads, PR state, the
 * issues, and every handler. Drags carry their payload in a ref, not in
 * dataTransfer, because dragover cannot read dataTransfer data.
 */
export function ThreadBoard<T extends BoardThread>({
  threads,
  states,
  formatUpdated,
  issues,
  issuesLoading,
  onOpen,
  onStartDraft,
  onDropIssue,
  onRefreshIssues,
}: {
  threads: T[];
  states: Record<string, ThreadState>;
  formatUpdated: (iso?: string) => string;
  /** null until the first issues.list reply for this project. */
  issues: BoardIssues | null;
  issuesLoading: boolean;
  onOpen: (id: string, draft: boolean) => void;
  onStartDraft: (id: string) => void;
  onDropIssue: (issue: BoardIssue) => void;
  onRefreshIssues: () => void;
}) {
  const columns = useMemo(() => groupThreads(threads, states), [threads, states]);
  const dragRef = useRef<DragPayload | null>(null);
  const [over, setOver] = useState<ThreadColumnId | null>(null);

  const startThreadDrag = useCallback((id: string, e: DragEvent) => {
    dragRef.current = { kind: "draft", id };
    e.dataTransfer.effectAllowed = "move";
    // Firefox and WebKit start no drag without some data set.
    e.dataTransfer.setData("text/plain", id);
  }, []);
  const startIssueDrag = useCallback((issue: BoardIssue, e: DragEvent) => {
    dragRef.current = { kind: "issue", issue };
    e.dataTransfer.effectAllowed = "copy";
    e.dataTransfer.setData("text/plain", issue.webUrl);
  }, []);
  const endDrag = useCallback(() => {
    dragRef.current = null;
    setOver(null);
  }, []);

  const colHandlers = (col: ThreadColumnId) => ({
    onDragOver: (e: DragEvent) => {
      const payload = dragRef.current;
      if (!payload || !canDrop(payload.kind, col)) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = payload.kind === "issue" ? "copy" : "move";
      setOver(col);
    },
    onDragLeave: (e: DragEvent) => {
      if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOver(null);
    },
    onDrop: (e: DragEvent) => {
      const payload = dragRef.current;
      dragRef.current = null;
      setOver(null);
      if (!payload || !canDrop(payload.kind, col)) return;
      e.preventDefault();
      if (payload.kind === "draft") onStartDraft(payload.id);
      else onDropIssue(payload.issue);
    },
  });

  const issueList = issues?.status === "ready" ? issues.issues : [];
  return (
    <div className="thread-board">
      <section className="tb-col tb-col-issues" aria-label="GitLab issues">
        <header className="tb-col-head">
          Issues <span className="tb-count">{issues?.status === "ready" ? issueList.length : 0}</span>
          <button
            className="tb-refresh"
            onClick={onRefreshIssues}
            disabled={issuesLoading}
            aria-label="Refresh issues"
            title="Refresh issues"
          >
            ↻
          </button>
        </header>
        <div className="tb-cards">
          {issues === null && <div className="empty">Loading issues…</div>}
          {issues !== null && issues.status !== "ready" && (
            <div className="empty">{issues.message || ISSUE_FALLBACK[issues.status] || "No issues."}</div>
          )}
          {issues?.status === "ready" && issueList.length === 0 && (
            <div className="empty">No issues assigned to you</div>
          )}
          {issueList.map((issue) => (
            <IssueBoardCard
              key={issue.webUrl}
              issue={issue}
              onDragStart={startIssueDrag}
              onDragEnd={endDrag}
            />
          ))}
        </div>
      </section>
      {THREAD_COLUMNS.map((col) => {
        const items = columns[col.id];
        return (
          <section
            className={`tb-col tb-col-${col.id}${over === col.id ? " tb-col-over" : ""}`}
            key={col.id}
            aria-label={col.title}
            {...colHandlers(col.id)}
          >
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
                  onDragStart={startThreadDrag}
                  onDragEnd={endDrag}
                />
              ))}
            </div>
          </section>
        );
      })}
    </div>
  );
}
