import { useEffect, useState } from "react";

/** fmtUpdated renders a thread's last activity as a short local date. */
export function fmtUpdated(at?: string): string {
  if (!at) return "";
  const date = new Date(at);
  if (Number.isNaN(date.getTime()) || date.getFullYear() < 1970) return "";
  return date.toLocaleDateString([], { month: "short", day: "numeric", year: "numeric" });
}

/** fmtIdleDuration renders an age as "45s" / "12m" / "3h 05m" / "4d". */
function fmtIdleDuration(ms: number): string {
  const mins = Math.floor(ms / 60_000);
  if (mins < 1) return `${Math.max(0, Math.round(ms / 1000))}s`;
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ${String(mins % 60).padStart(2, "0")}m`;
  return `${Math.floor(hours / 24)}d`;
}

/**
 * ThreadClock shows a thread's activity state as a live pill: "Working" while
 * agents run, "Stale" once the thread has been idle past the configured
 * threshold, or a countdown to staleness otherwise. It owns a coarse interval
 * (30s) and re-renders only itself, so a full board of timers costs almost
 * nothing.
 */
export function ThreadClock({
  t,
}: {
  t: { runningAgents?: number; updatedAt?: string; staleAfterSeconds?: number; messages?: number };
}) {
  const [, bump] = useState(0);
  useEffect(() => {
    const id = setInterval(() => bump((n) => n + 1), 30_000);
    return () => clearInterval(id);
  }, []);

  if ((t.runningAgents ?? 0) > 0) {
    return <span className="pill run">● Working</span>;
  }
  // A thread with no history has nothing to go stale.
  if (t.messages === 0) return null;
  const at = t.updatedAt ? Date.parse(t.updatedAt) : NaN;
  if (Number.isNaN(at)) return null;
  const idleMs = Date.now() - at;
  const afterMs = (t.staleAfterSeconds ?? 0) * 1000;
  if (afterMs <= 0) return null;
  if (idleMs >= afterMs) {
    return <span className="pill stale">◌ Stale · {fmtIdleDuration(idleMs)}</span>;
  }
  return <span className="pill live">○ Stale in {fmtIdleDuration(afterMs - idleMs)}</span>;
}
