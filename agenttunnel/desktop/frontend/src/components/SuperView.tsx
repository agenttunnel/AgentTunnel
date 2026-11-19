import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Project, ThreadSummary } from "../App";
import { threadSearchScore } from "../lib/threads";

/** One group of the super view: threads sharing a lifecycle state. */
export type SuperGroups = {
  attention: ThreadSummary[];
  running: ThreadSummary[];
  unseen: ThreadSummary[];
  quiet: ThreadSummary[];
};

/**
 * groupSuperThreads splits the daemon-wide thread list into the super view's
 * groups, the ones that need you first: threads with an agent question waiting
 * on you, threads with live agents, threads whose newest completed turn ended
 * after the last view, and everything else. Archived threads stay out.
 * Each group sorts by recency (updatedAt desc) unless the caller already ranked
 * the list for a search. Pure, so it is smoke-testable without React.
 */
export function groupSuperThreads(
  threads: ThreadSummary[],
  preserveOrder = false,
): SuperGroups {
  const out: SuperGroups = { attention: [], running: [], unseen: [], quiet: [] };
  for (const t of threads) {
    if (t.archived) continue;
    const running = (t.runningAgents ?? 0) > 0;
    if ((t.pendingQuestions ?? 0) > 0) out.attention.push(t);
    else if (running) out.running.push(t);
    else if (t.unseen) out.unseen.push(t);
    else out.quiet.push(t);
  }
  if (!preserveOrder) {
    const byRecency = (a: ThreadSummary, b: ThreadSummary) =>
      (b.updatedAt ?? "").localeCompare(a.updatedAt ?? "");
    out.attention.sort(byRecency);
    out.running.sort(byRecency);
    out.unseen.sort(byRecency);
    out.quiet.sort(byRecency);
  }
  return out;
}

function fmtDate(at?: string): string {
  if (!at) return "";
  const date = new Date(at);
  if (Number.isNaN(date.getTime()) || date.getFullYear() < 1970) return "";
  return date.toLocaleDateString([], { month: "short", day: "numeric", year: "numeric" });
}

const IS_MAC = /Mac|iPhone|iPad|iPod/.test(navigator.userAgent);
/** The modifier the super view's shortcut is named after. */
const ALL_THREADS_MODIFIER = IS_MAC ? "Option" : "Alt";

/**
 * Whether a key event is the all-threads shortcut: Option+Tab on macOS,
 * Alt+Tab elsewhere. Some webviews also expose the Fn key as a modifier, so it
 * works there too when the keyboard reports it.
 */
export function isAllThreadsShortcut(e: KeyboardEvent): boolean {
  if (e.key !== "Tab" || e.metaKey || e.ctrlKey || e.shiftKey || e.isComposing) return false;
  return e.altKey || e.getModifierState("Fn");
}

/** A row's live state: questions waiting on you, then running agents. */
function ThreadState({ t }: { t: ThreadSummary }) {
  const asks = t.pendingQuestions ?? 0;
  const running = t.runningAgents ?? 0;
  return (
    <>
      {asks > 0 && (
        <span className="super-state ask" title="Agent questions waiting on you">
          ? {asks} {asks === 1 ? "question" : "questions"}
        </span>
      )}
      {running > 0 && (
        <span className="super-state run" title="Agents running now">
          {running} running
        </span>
      )}
    </>
  );
}

type GroupSpec = {
  key: keyof SuperGroups;
  title: string;
  empty: string;
  dot: string;
};

const GROUPS: GroupSpec[] = [
  { key: "attention", title: "Needs you", empty: "", dot: "attention" },
  { key: "running", title: "Running", empty: "No threads are running.", dot: "running" },
  { key: "unseen", title: "Finished, not seen yet", empty: "You've seen every finished thread.", dot: "succeeded" },
  { key: "quiet", title: "Quiet", empty: "Nothing to show.", dot: "missing" },
];

/**
 * SuperView: every project's threads in one overlay, the answer to "what is
 * running and what finished while I was away". Data comes from App's existing
 * thread.list poll and mark-on-open wiring; this component groups, searches,
 * and renders it. Esc closes, backdrop click closes, a row opens the thread.
 * Option/Alt+Tab steps through the rows without closing the overlay, so
 * releasing the modifier leaves it open.
 */
export function SuperView({
  threads,
  projects,
  initialThreadId,
  onOpen,
  onClose,
}: {
  threads: ThreadSummary[];
  projects: Project[];
  /** The row to highlight first, when a thread was open behind the overlay. */
  initialThreadId?: string | null;
  onOpen: (t: ThreadSummary) => void;
  onClose: () => void;
}) {
  const [search, setSearch] = useState("");
  const [cursor, setCursor] = useState<string | null>(initialThreadId ?? null);
  const searchRef = useRef<HTMLInputElement>(null);
  const names = useMemo(() => new Map(projects.map((p) => [p.id, p.name])), [projects]);
  const query = search.trim();

  const matches = useMemo(() => {
    if (!query) return threads;
    const ranked: { thread: ThreadSummary; score: number }[] = [];
    for (const t of threads) {
      const project = t.projectId ? names.get(t.projectId) : undefined;
      const score = threadSearchScore(t, query, project);
      if (score !== null) ranked.push({ thread: t, score });
    }
    ranked.sort((a, b) => b.score - a.score);
    return ranked.map((r) => r.thread);
  }, [threads, names, query]);

  const groups = useMemo(
    () => groupSuperThreads(matches, query !== ""),
    [matches, query],
  );
  const rows = useMemo(
    () => [...groups.attention, ...groups.running, ...groups.unseen, ...groups.quiet],
    [groups],
  );
  // A search can hide the thread the cursor was on; fall back to the first row.
  const cursorId = cursor && rows.some((t) => t.id === cursor) ? cursor : rows[0]?.id ?? null;
  // Native key repeat can deliver several Tab presses before React re-renders;
  // read the latest cursor synchronously so every press advances one row.
  const cursorRef = useRef(cursorId);
  cursorRef.current = cursorId;

  useEffect(() => {
    searchRef.current?.focus();
  }, []);

  const cycle = useCallback(
    (dir: 1 | -1) => {
      if (rows.length === 0) return;
      const at = rows.findIndex((t) => t.id === cursorRef.current);
      const next = rows[(at + dir + rows.length) % rows.length];
      cursorRef.current = next.id;
      setCursor(next.id);
      // The next row is already rendered, so move focus with the highlight and
      // Enter/Space opens it once the shortcut is released.
      const el = document.getElementById(`super-row-${next.id}`);
      el?.focus();
      el?.scrollIntoView({ block: "nearest" });
    },
    [rows],
  );

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !e.isComposing) {
        e.preventDefault();
        onClose();
        return;
      }
      if (!isAllThreadsShortcut(e)) return;
      e.preventDefault();
      cycle(1);
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [cycle, onClose]);

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
            {groups.attention.length > 0 && `${groups.attention.length} ${groups.attention.length === 1 ? "needs" : "need"} you · `}
            {groups.running.length} running · {groups.unseen.length} unseen
          </span>
          <button onClick={onClose}>Close</button>
        </div>
        <div className="super-search">
          <input
            ref={searchRef}
            className="thread-filter"
            type="search"
            placeholder="Search threads…"
            aria-label="Search all threads"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          <span className="super-search-hint" aria-hidden="true">
            <kbd>{ALL_THREADS_MODIFIER}</kbd>+<kbd>Tab</kbd> cycles
          </span>
        </div>
        <div className="super-body">
          {query !== "" && matches.length === 0 && (
            <p className="super-empty super-no-match">No threads match that search.</p>
          )}
          {GROUPS.map((g) => {
            const groupRows = groups[g.key];
            // "Needs you" shows only when something does.
            if ((query !== "" || g.key === "attention") && groupRows.length === 0) return null;
            return (
              <section className="super-group" key={g.key}>
                <h3 className="super-group-title">{g.title}</h3>
                {groupRows.length === 0 && <p className="super-empty">{g.empty}</p>}
                {groupRows.map((t) => {
                  const active = t.id === cursorId;
                  return (
                    <button
                      key={t.id}
                      id={`super-row-${t.id}`}
                      type="button"
                      className={`super-row${active ? " is-cursor" : ""}`}
                      aria-current={active ? "true" : undefined}
                      onClick={() => onOpen(t)}
                      title={t.firstPrompt || t.title || "Untitled thread"}
                    >
                      <span className={`dot ${g.dot}`} aria-hidden="true" />
                      <span className="super-name">{t.title || "Untitled thread"}</span>
                      {t.projectId && (
                        <span className="super-project">{names.get(t.projectId) ?? t.projectId}</span>
                      )}
                      {t.parentThreadId && <span className="super-parent">subthread</span>}
                      <ThreadState t={t} />
                      <span className="super-meta">
                        {t.messages} messages · {t.agents} agents
                        {fmtDate(t.updatedAt) && ` · ${fmtDate(t.updatedAt)}`}
                      </span>
                    </button>
                  );
                })}
              </section>
            );
          })}
        </div>
      </div>
    </div>
  );
}
