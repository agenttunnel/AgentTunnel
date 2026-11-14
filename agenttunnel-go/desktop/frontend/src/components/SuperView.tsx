import { useEffect, useMemo, useRef } from "react";
import type { Project, ThreadSummary } from "../App";

/** One group of the super view: threads sharing a lifecycle state. */
export type SuperGroups = {
  running: ThreadSummary[];
  unseen: ThreadSummary[];
  quiet: ThreadSummary[];
};

/**
 * groupSuperThreads splits the daemon-wide thread list into the super view's
 * three groups: threads with live agents, threads whose newest completed turn
 * ended after the last view, and everything else. Archived threads stay out.
 * Each group sorts by recency (updatedAt desc). Pure, so it is smoke-testable
 * without React.
 */
export function groupSuperThreads(threads: ThreadSummary[]): SuperGroups {
  const out: SuperGroups = { running: [], unseen: [], quiet: [] };
  for (const t of threads) {
    if (t.archived) continue;
    const running = (t.runningAgents ?? 0) > 0;
    if (running) out.running.push(t);
    else if (t.unseen) out.unseen.push(t);
    else out.quiet.push(t);
  }
  const byRecency = (a: ThreadSummary, b: ThreadSummary) =>
    (b.updatedAt ?? "").localeCompare(a.updatedAt ?? "");
  out.running.sort(byRecency);
  out.unseen.sort(byRecency);
  out.quiet.sort(byRecency);
  return out;
}

function fmtDate(at?: string): string {
  if (!at) return "";
  const date = new Date(at);
  if (Number.isNaN(date.getTime()) || date.getFullYear() < 1970) return "";
  return date.toLocaleDateString([], { month: "short", day: "numeric", year: "numeric" });
}

type GroupSpec = {
  key: keyof SuperGroups;
  title: string;
  empty: string;
  dot: string;
};

const GROUPS: GroupSpec[] = [
  { key: "running", title: "Running", empty: "No threads are running.", dot: "running" },
  { key: "unseen", title: "Finished, not seen yet", empty: "You've seen every finished thread.", dot: "succeeded" },
  { key: "quiet", title: "Quiet", empty: "Nothing to show.", dot: "missing" },
];

/**
 * SuperView: every project's threads in one overlay, the answer to "what is
 * running and what finished while I was away". Data comes from App's existing
 * thread.list poll and mark-on-open wiring; this component only groups and
 * renders it. Esc closes, backdrop click closes, a row opens the thread.
 */
export function SuperView({
  threads,
  projects,
  onOpen,
  onClose,
}: {
  threads: ThreadSummary[];
  projects: Project[];
  onOpen: (t: ThreadSummary) => void;
  onClose: () => void;
}) {
  const groups = useMemo(() => groupSuperThreads(threads), [threads]);
  const names = useMemo(() => new Map(projects.map((p) => [p.id, p.name])), [projects]);
  const openedRef = useRef<string | null>(null);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const open = (t: ThreadSummary) => {
    if (openedRef.current === t.id) return; // one navigation per row click
    openedRef.current = t.id;
    onOpen(t);
  };

  return (
    <div className="overlay super-overlay" onClick={onClose}>
      <div
        className="sheet super-sheet"
        role="dialog"
        aria-label="All threads across projects"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="sheet-head">
          <strong>All threads</strong>
          <span className="super-sub">
            {groups.running.length} running · {groups.unseen.length} unseen
          </span>
          <button onClick={onClose}>Close</button>
        </div>
        <div className="super-body">
          {GROUPS.map((g) => {
            const rows = groups[g.key];
            return (
              <section className="super-group" key={g.key}>
                <h3 className="super-group-title">{g.title}</h3>
                {rows.length === 0 && <p className="super-empty">{g.empty}</p>}
                {rows.map((t) => (
                  <button
                    key={t.id}
                    type="button"
                    className="super-row"
                    onClick={() => open(t)}
                    title={t.firstPrompt || t.title || "Untitled thread"}
                  >
                    <span className={`dot ${g.dot}`} aria-hidden="true" />
                    <span className="super-name">{t.title || "Untitled thread"}</span>
                    {t.projectId && (
                      <span className="super-project">{names.get(t.projectId) ?? t.projectId}</span>
                    )}
                    {t.parentThreadId && <span className="super-parent">subthread</span>}
                    <span className="super-meta">
                      {t.messages} messages · {t.agents} agents
                      {fmtDate(t.updatedAt) && ` · ${fmtDate(t.updatedAt)}`}
                    </span>
                  </button>
                ))}
              </section>
            );
          })}
        </div>
      </div>
    </div>
  );
}
