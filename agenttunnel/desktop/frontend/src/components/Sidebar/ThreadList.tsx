// The thread sidebar: the search box and the project's thread rows. The
// search text lives in threadSearch, shared with the threads screen, so a
// keystroke re-renders the lists that filter by it and nothing else.

import { memo, useMemo, useState, type MouseEvent } from "react";
import type { ThreadSummary } from "../../App";
import { soloHue } from "../../lib/soloIdentity";
import { createStore, useStoreField } from "../../lib/store";
import { clickable } from "../../lib/switchProps";
import { arrangeThreads, threadSearchScore, visibleThreadRows } from "../../lib/threads";
import { ThreadClock, fmtUpdated } from "../ThreadClock";
import { needsAttention, STATUS_LABEL, threadStatus } from "../../lib/threadStatus";

/** The thread search text, one for every screen that lists threads. */
export const threadSearch = createStore({ query: "" });
const setQuery = threadSearch.setter("query");

/** The search box. Reads and writes threadSearch only. */
export function ThreadSearch() {
  const threadFilter = useStoreField(threadSearch, "query");
  return (
    <input
      className="thread-filter"
      type="search"
      aria-label="Search threads"
      title="Search threads (/)"
      placeholder="Search threads…  /"
      onKeyDown={(e) => {
        if (e.key === "Escape" && threadFilter) {
          e.stopPropagation();
          setQuery("");
        }
      }}
      value={threadFilter}
      onChange={(e) => setQuery(e.target.value)}
    />
  );
}

/** A project's threads as the lists show them: the tree order, or ranked by
 * the search when there is one. `query` is the trimmed search text. */
export function useProjectThreads(threads: ThreadSummary[], projectId: string | null) {
  const query = useStoreField(threadSearch, "query").trim();
  const projectThreads = useMemo(() => {
    const scoped = threads.filter((t) => t.projectId === projectId);
    if (query === "") return arrangeThreads(scoped);
    const ranked: { thread: ThreadSummary; score: number }[] = [];
    for (const thread of scoped) {
      const score = threadSearchScore(thread, query);
      if (score !== null) ranked.push({ thread, score });
    }
    // Array.sort is stable, so equal scores keep the daemon's submission order.
    ranked.sort((a, b) => b.score - a.score);
    return arrangeThreads(ranked.map((r) => r.thread));
  }, [threads, projectId, query]);
  return { projectThreads, query };
}

export const ThreadList = memo(function ThreadList({
  threads,
  projectId,
  activeId,
  onSelect,
  onMenu,
}: {
  threads: ThreadSummary[];
  projectId: string | null;
  activeId: string | null;
  onSelect: (id: string) => void;
  onMenu: (t: ThreadSummary, e: MouseEvent) => void;
}) {
  const { projectThreads, query } = useProjectThreads(threads, projectId);
  // Threads whose subthreads are hidden in the sidebar. Clicking a thread that
  // has subthreads toggles this, so a long tree can be folded away.
  const [collapsedThreads, setCollapsedThreads] = useState<ReadonlySet<string>>(() => new Set());
  const toggleSubthreads = (id: string) =>
    setCollapsedThreads((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  return (
    <>
      <div className="sidebar-search">
        <ThreadSearch />
      </div>
      <div className="col-body">
        {projectThreads.length === 0 && (
          <div className="empty">
            {query !== "" ? "No threads match that search." : "No threads yet"}
          </div>
        )}
        {visibleThreadRows(projectThreads, collapsedThreads, (t) => t.id).map((t) => {
          const status = threadStatus(t);
          const label = STATUS_LABEL[status];
          const attn = needsAttention(status);
          return (
          <div
            key={t.id}
            className={`thread-row ${t.id === activeId ? "active" : ""}${t.stale ? " stale" : ""}${t.waiting ? " scheduled" : ""}${attn ? " attn" : ""}${t.harness ? ` solo-identity ${soloHue(t.id)}` : ""}`}
            style={t.depth ? { marginLeft: t.depth * 16 } : undefined}
            {...clickable(() => {
              if (t.subthreads > 0) toggleSubthreads(t.id);
              onSelect(t.id);
            })}
            aria-current={t.id === activeId ? "true" : undefined}
            onContextMenu={(e) => onMenu(t, e)}
          >
            {t.waiting && (
              <svg className="row-sched-ring" aria-hidden="true" focusable="false">
                <rect x="1" y="1" rx="9" pathLength="100" />
              </svg>
            )}
            <div className="t" title={t.title}>
              {attn && <span className="attn-dot" aria-label={label} />}
              {t.title}
              {t.waiting && (
                <span className="row-scheduled" title={`Scheduled — waiting on ${t.waiting.dependsOn.length} thread${t.waiting.dependsOn.length === 1 ? "" : "s"}`}>
                  ◷ {t.waiting.dependsOn.length}
                </span>
              )}
              {t.subthreads > 0 && (
                <span className="sub-caret" aria-label={collapsedThreads.has(t.id) ? "Show subthreads" : "Hide subthreads"}>
                  {collapsedThreads.has(t.id) ? "▸" : "▾"} {t.subthreads}
                </span>
              )}
            </div>
            {t.messages > 0 && (
              <div className="m">
                {t.messages} msg{t.messages === 1 ? "" : "s"}
                {t.agents > 0 ? ` · ${t.agents} agent${t.agents === 1 ? "" : "s"}` : ""}
                {t.autoMode ? " · auto" : ""}
              </div>
            )}
            <div className="thread-row-foot">
              {status !== "running" && label && <span className={`status-chip ${status}`}>{label}</span>}
              <ThreadClock t={t} />
              {t.controlledBy && (
                <button
                  type="button"
                  className="controlled-chip"
                  title={`Relaunched by ${threads.find((c) => c.id === t.controlledBy)?.title ?? t.controlledBy}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    if (t.controlledBy) onSelect(t.controlledBy);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      e.stopPropagation();
                      if (t.controlledBy) onSelect(t.controlledBy);
                    }
                  }}
                >
                  ⌁ controlled
                </button>
              )}
              {fmtUpdated(t.updatedAt) && <span className="u">{fmtUpdated(t.updatedAt)}</span>}
            </div>
          </div>
          );
        })}
      </div>
    </>
  );
});
