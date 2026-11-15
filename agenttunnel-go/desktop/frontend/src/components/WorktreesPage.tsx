import { useMemo, useState } from "react";
import type { ThreadState } from "./ThreadBoard";
import {
  TONE_RANK,
  badgeLabel,
  worktreeRows,
  type WorktreeInfo,
  type WorktreeProject,
  type WorktreeThread,
} from "../lib/worktrees";

type Filter = "all" | "ready" | "dirty" | "missing";

/**
 * WorktreesPage is the environment-wide view of every thread checkout. Git
 * facts are live, so they load when the page opens and after each action, never
 * on a timer. Pull request state comes from the daemon's cached pr.states poll:
 * the page asks per owner thread, so merged worktrees surface as prune-ready
 * without re-running git or the forge CLI for every row.
 */
export function WorktreesPage({
  projects,
  worktrees,
  threads,
  states,
  loading,
  error,
  onRefresh,
  onOpenThread,
  onRemove,
  onPrune,
  onPruneMerged,
}: {
  projects: WorktreeProject[];
  /** Keyed by project id; undefined until that project's list has loaded. */
  worktrees: Record<string, WorktreeInfo[] | undefined>;
  threads: WorktreeThread[];
  /** PR and dirty state per thread, from pr.states. */
  states: Record<string, ThreadState>;
  loading: boolean;
  error: string | null;
  onRefresh: () => void;
  onOpenThread: (projectId: string, threadId: string) => void;
  onRemove: (projectId: string, path: string, force: boolean) => Promise<void>;
  onPrune: (projectId: string) => Promise<void>;
  onPruneMerged: (targets: { projectId: string; path: string }[]) => Promise<unknown>;
}) {
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState<string | null>(null);
  const [bulkConfirm, setBulkConfirm] = useState(false);
  const [filter, setFilter] = useState<Filter>("all");
  const titles = useMemo(() => new Map(threads.map((t) => [t.id, t.title])), [threads]);

  // One row per loaded worktree, with its tone derived from live git facts and
  // the last PR snapshot. Prune-ready rows lead each project.
  const rows = useMemo(
    () => worktreeRows(projects, worktrees, states),
    [projects, worktrees, states],
  );

  const total = rows.length;
  const ready = rows.filter((r) => r.tone === "merged");
  const mergedDirty = rows.filter((r) => r.tone === "merged-dirty");
  const dirty = rows.filter((r) => r.worktree.exists && r.worktree.dirty);
  const missing = rows.filter((r) => r.tone === "missing");

  const visible = rows
    .filter((r) => {
      if (filter === "ready") return r.tone === "merged";
      if (filter === "dirty") return r.worktree.exists && r.worktree.dirty;
      if (filter === "missing") return r.tone === "missing";
      return true;
    })
    .sort((a, b) => TONE_RANK[a.tone] - TONE_RANK[b.tone] || a.worktree.branch.localeCompare(b.worktree.branch));

  // Keep project order from the grid, and only show groups with something in
  // them. A filter that hides a project should not leave a bare heading.
  const groups = projects
    .map((project) => ({ project, rows: visible.filter((r) => r.project.id === project.id) }))
    .filter((g) => g.rows.length > 0);

  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
    } finally {
      setBusy(false);
      setConfirming(null);
      setBulkConfirm(false);
    }
  };

  const pruneAll = () =>
    run(() => onPruneMerged(ready.map((r) => ({ projectId: r.project.id, path: r.worktree.path }))));

  const stats: { id: Filter; label: string; count: number; ready?: boolean }[] = [
    { id: "all", label: "Worktrees", count: total },
    { id: "ready", label: "Ready to prune", count: ready.length, ready: true },
    { id: "dirty", label: "With changes", count: dirty.length },
    { id: "missing", label: "Missing", count: missing.length },
  ];

  return (
    <div className="wt-page">
      <div className="wt-top">
        <div className="wt-stats">
          {stats.map((s) => (
            <button
              key={s.id}
              type="button"
              className={`wt-stat${filter === s.id ? " on" : ""}${s.ready && s.count > 0 ? " is-ready" : ""}`}
              aria-pressed={filter === s.id}
              onClick={() => setFilter(s.id)}
            >
              <span className="wt-stat-n">{s.count}</span>
              <span className="wt-stat-label">{s.label}</span>
            </button>
          ))}
        </div>
        <button className="wt-refresh" onClick={onRefresh} disabled={loading || busy}>
          {loading ? "Refreshing…" : "Refresh"}
        </button>
      </div>

      {ready.length > 0 && (
        <div className="wt-callout">
          <span className="wt-callout-icon" aria-hidden="true">
            ✓
          </span>
          <div className="wt-callout-main">
            <strong>
              {ready.length} merged worktree{ready.length === 1 ? "" : "s"} ready to prune
            </strong>
            <span>
              Their pull requests are merged, so their checkouts can be removed.
              {mergedDirty.length > 0 &&
                ` ${mergedDirty.length} more are merged but have uncommitted changes.`}
            </span>
          </div>
          <div className="wt-callout-actions">
            {bulkConfirm ? (
              <>
                <button className="primary" disabled={busy} onClick={pruneAll}>
                  Confirm prune {ready.length}
                </button>
                <button disabled={busy} onClick={() => setBulkConfirm(false)}>
                  Cancel
                </button>
              </>
            ) : (
              <button className="primary" disabled={busy} onClick={() => setBulkConfirm(true)}>
                Prune {ready.length}
              </button>
            )}
          </div>
        </div>
      )}

      {error && <p className="hint err-text">{error}</p>}

      {projects.length === 0 && <p className="hint">Add a project to see its worktrees.</p>}
      {projects.length > 0 && total === 0 && (
        <div className="wt-empty">
          {loading
            ? "Loading worktrees…"
            : error
              ? "Worktrees could not be loaded."
              : "No worktrees yet. A thread in a git project gets its own checkout on its first turn."}
        </div>
      )}
      {total > 0 && visible.length === 0 && (
        <div className="wt-empty">No worktrees match this filter.</div>
      )}

      {groups.map(({ project, rows: groupRows }) => {
        const projectRows = rows.filter((r) => r.project.id === project.id);
        const groupTotal = worktrees[project.id]?.length ?? 0;
        const groupReady = projectRows.filter((r) => r.tone === "merged").length;
        const groupMissing = projectRows.filter((r) => r.tone === "missing").length;
        return (
          <section className="wt-project-group" key={project.id}>
            <header className="wt-group-head">
              <div className="wt-group-title">
                <span className="wt-project" title={project.path || project.name}>
                  {project.name}
                </span>
                <span className="wt-group-meta">
                  {groupTotal} worktree{groupTotal === 1 ? "" : "s"}
                  {groupReady > 0 && <span className="ok"> · {groupReady} ready</span>}
                </span>
              </div>
              {groupMissing > 0 && (
                <button
                  className="wt-group-prune"
                  disabled={busy}
                  onClick={() => run(() => onPrune(project.id))}
                  title="Drop git's records for worktrees whose directories are gone"
                >
                  Forget {groupMissing} missing record{groupMissing === 1 ? "" : "s"}
                </button>
              )}
            </header>
            <div className="wt-grid">
              {groupRows.map(({ worktree: w, pr, tone }) => {
                const isConfirming = confirming === w.path;
                const owners = w.threadIds.map((id) => ({ id, title: titles.get(id) ?? id }));
                const first = owners[0];
                const changes = w.ahead > 0 || w.behind > 0;
                return (
                  <article className={`wt-card ${tone}${isConfirming ? " is-confirming" : ""}`} key={w.path}>
                    <div className="wt-card-head">
                      <span className={`wt-badge ${tone}`}>{badgeLabel(tone, pr)}</span>
                      {pr && (
                        <a className="wt-pr" href={pr.url} title={`PR #${pr.number}`}>
                          #{pr.number}
                        </a>
                      )}
                      {changes && (
                        <span className="wt-ab" title={`${w.ahead} ahead, ${w.behind} behind ${w.base}`}>
                          ↑{w.ahead} ↓{w.behind}
                        </span>
                      )}
                    </div>
                    <div className="wt-branch" title={w.path}>
                      {w.branch || "(detached)"}
                    </div>
                    <div className="wt-base">from {w.base || "?"}</div>
                    {owners.length > 0 ? (
                      <div className="wt-owners">
                        {owners.map((o) => (
                          <button
                            key={o.id}
                            type="button"
                            className="wt-owner"
                            title={`Open ${o.title}`}
                            onClick={() => onOpenThread(project.id, o.id)}
                          >
                            {o.title}
                          </button>
                        ))}
                      </div>
                    ) : (
                      <span className="wt-owner-empty">No thread owns this worktree</span>
                    )}
                    <div className="wt-path" title={w.path}>
                      {w.path}
                    </div>
                    <div className="wt-actions">
                      {first && (
                        <button disabled={busy} onClick={() => onOpenThread(project.id, first.id)}>
                          Open thread
                        </button>
                      )}
                      {tone === "missing" ? (
                        <button disabled={busy} onClick={() => run(() => onPrune(project.id))}>
                          Forget record
                        </button>
                      ) : isConfirming ? (
                        <>
                          {w.dirty ? (
                            <button
                              className="danger"
                              disabled={busy}
                              onClick={() => run(() => onRemove(project.id, w.path, true))}
                            >
                              Discard changes and prune
                            </button>
                          ) : (
                            <button
                              className="primary"
                              disabled={busy}
                              onClick={() => run(() => onRemove(project.id, w.path, false))}
                            >
                              Confirm remove
                            </button>
                          )}
                          <button disabled={busy} onClick={() => setConfirming(null)}>
                            Cancel
                          </button>
                        </>
                      ) : tone === "merged" ? (
                        <button
                          className="primary"
                          disabled={busy}
                          onClick={() => run(() => onRemove(project.id, w.path, false))}
                        >
                          Prune
                        </button>
                      ) : (
                        <button disabled={busy} onClick={() => setConfirming(w.path)}>
                          {pr?.state === "merged" ? "Prune…" : "Remove…"}
                        </button>
                      )}
                    </div>
                  </article>
                );
              })}
            </div>
          </section>
        );
      })}
    </div>
  );
}
