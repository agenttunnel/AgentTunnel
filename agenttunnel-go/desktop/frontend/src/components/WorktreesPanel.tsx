import { useState } from "react";

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

export type WorktreeProject = { id: string; name: string };
export type WorktreeThread = { id: string; title: string };

type Status = "clean" | "dirty" | "missing";

/** A missing directory is the only state that needs a prune; a dirty tree is
 * still live work, so it gets the warning dot rather than the grey one. */
function statusOf(w: WorktreeInfo): Status {
  if (!w.exists || w.prunable) return "missing";
  return w.dirty ? "dirty" : "clean";
}

const STATUS_LABEL: Record<Status, string> = {
  clean: "clean",
  dirty: "uncommitted changes",
  missing: "directory missing",
};

/**
 * WorktreesPanel lists the git worktrees of each open project. Refresh happens
 * when the tab is shown and after each action, never on a timer: every refresh
 * runs git in each worktree.
 */
export function WorktreesPanel({
  projects,
  worktrees,
  threads,
  newThreadsUseWorktree,
  onToggleNewThreads,
  loading,
  error,
  onRefresh,
  onOpenThread,
  onRemove,
  onPrune,
}: {
  projects: WorktreeProject[];
  /** Keyed by project id; undefined until that project's list has loaded. */
  worktrees: Record<string, WorktreeInfo[] | undefined>;
  threads: WorktreeThread[];
  newThreadsUseWorktree: boolean;
  onToggleNewThreads: (on: boolean) => void;
  loading: boolean;
  error: string | null;
  onRefresh: () => void;
  onOpenThread: (projectId: string, threadId: string) => void;
  onRemove: (projectId: string, path: string, force: boolean) => Promise<void>;
  onPrune: (projectId: string) => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState<string | null>(null);
  const titles = new Map(threads.map((t) => [t.id, t.title]));

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    try {
      await fn();
    } finally {
      setBusy(false);
      setConfirming(null);
    }
  };

  return (
    <div className="worktrees-panel">
      <div className="panel-bar">
        <span className="chat-title">Worktrees</span>
        <button onClick={onRefresh} disabled={loading || busy}>
          {loading ? "Loading…" : "Refresh"}
        </button>
      </div>
      <label className="wt-toggle">
        <input
          type="checkbox"
          checked={newThreadsUseWorktree}
          onChange={(e) => onToggleNewThreads(e.target.checked)}
        />
        New threads get their own worktree
      </label>
      {error && <p className="hint err-text">{error}</p>}
      {projects.length === 0 && <p className="hint">Open a project to see its worktrees.</p>}
      {projects.map((p) => {
        const list = worktrees[p.id];
        const prunable = list?.filter((w) => statusOf(w) === "missing").length ?? 0;
        return (
          <section className="wt-group" key={p.id}>
            <div className="wt-group-head">
              <span className="wt-project">{p.name}</span>
              {prunable > 0 && (
                <button
                  className="wt-prune"
                  disabled={busy}
                  onClick={() => run(() => onPrune(p.id))}
                >
                  Prune {prunable} missing
                </button>
              )}
            </div>
            {list && list.length === 0 && <p className="hint">No worktrees yet.</p>}
            {list?.map((w) => {
              const status = statusOf(w);
              const linked = w.threadIds.map((id) => titles.get(id)).filter(Boolean) as string[];
              const first = w.threadIds[0];
              const isConfirming = confirming === w.path;
              return (
                <div className="wt-row" key={w.path}>
                  <div className="wt-line">
                    <span
                      className={`dot ${status}`}
                      title={STATUS_LABEL[status]}
                      aria-label={STATUS_LABEL[status]}
                    />
                    <span className="wt-branch" title={w.path}>
                      {w.branch || "(detached)"}
                    </span>
                    <span className="wt-base">from {w.base || "?"}</span>
                  </div>
                  <div className="wt-meta">
                    <span>{STATUS_LABEL[status]}</span>
                    {(w.ahead > 0 || w.behind > 0) && (
                      <span>
                        ↑{w.ahead} ↓{w.behind}
                      </span>
                    )}
                    {linked.length > 0 && <span className="wt-threads">{linked.join(", ")}</span>}
                  </div>
                  <div className="wt-actions">
                    <button disabled={!first || busy} onClick={() => first && onOpenThread(p.id, first)}>
                      Open thread
                    </button>
                    {w.exists && !isConfirming && (
                      <button disabled={busy} onClick={() => setConfirming(w.path)}>
                        Remove
                      </button>
                    )}
                    {isConfirming && (
                      <>
                        {w.dirty ? (
                          <button
                            className="danger"
                            disabled={busy}
                            onClick={() => run(() => onRemove(p.id, w.path, true))}
                          >
                            Force remove (discards changes)
                          </button>
                        ) : (
                          <button
                            className="primary"
                            disabled={busy}
                            onClick={() => run(() => onRemove(p.id, w.path, false))}
                          >
                            Confirm remove
                          </button>
                        )}
                        <button disabled={busy} onClick={() => setConfirming(null)}>
                          Cancel
                        </button>
                      </>
                    )}
                  </div>
                </div>
              );
            })}
          </section>
        );
      })}
    </div>
  );
}
