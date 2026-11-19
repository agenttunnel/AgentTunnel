import { memo, useMemo } from "react";
import { teamProgress, type TreeAgent } from "../lib/agentTree";
import type { AgentQuestion } from "../lib/questions";

/**
 * TeamProgress is the team's state in one line, for the thread header:
 * "3 of 7 done · 2 running · 1 blocked", with a segmented bar that shares the
 * same counts. It counts dispatched agents (never the leads) and adds the
 * plan's checklist when the leads keep one. Renders nothing for a thread that
 * never dispatched work, so a solo or fresh thread stays quiet.
 *
 * Mount it anywhere with the thread's agents, questions, and tasks; it derives
 * everything from them and holds no state.
 */
export const TeamProgress = memo(function TeamProgress({
  agents,
  questions,
  tasks,
  onClick,
}: {
  agents: TreeAgent[];
  questions?: AgentQuestion[] | null;
  tasks?: { status: string }[] | null;
  /** Opens the team view (the Agents panel). */
  onClick?: () => void;
}) {
  const p = useMemo(() => teamProgress(agents, questions, tasks), [agents, questions, tasks]);
  if (p.total === 0 && !p.plan) return null;

  const parts: { n: number; label: string; cls: string }[] = [
    { n: p.running, label: "running", cls: "running" },
    { n: p.waiting, label: p.waiting === 1 ? "needs you" : "need you", cls: "waiting" },
    { n: p.blocked, label: "blocked", cls: "blocked" },
    { n: p.failed, label: "failed", cls: "failed" },
  ].filter((x) => x.n > 0);
  const summary = [
    p.total > 0 ? `${p.done} of ${p.total} done` : "",
    ...parts.map((x) => `${x.n} ${x.label}`),
    p.plan ? `plan ${p.plan.done}/${p.plan.total}` : "",
  ]
    .filter(Boolean)
    .join(" · ");

  const Tag = onClick ? "button" : "div";
  return (
    <Tag
      {...(onClick ? { type: "button" as const, onClick } : {})}
      className="team-progress"
      title={onClick ? `${summary} — open Agents` : summary}
      aria-label={`Team progress: ${summary}`}
    >
      {p.total > 0 && (
        <span className="tp-bar" aria-hidden="true">
          <i className="seg-done" style={{ flexGrow: p.done }} />
          <i className="seg-running" style={{ flexGrow: p.running + p.waiting }} />
          <i className="seg-failed" style={{ flexGrow: p.failed }} />
          <i className="seg-rest" style={{ flexGrow: p.total - p.done - p.running - p.waiting - p.failed }} />
        </span>
      )}
      {p.total > 0 && (
        <span className="tp-main">
          {p.done} of {p.total} done
        </span>
      )}
      {parts.map((x) => (
        <span key={x.cls} className={`tp-part tp-${x.cls}`}>
          {x.n} {x.label}
        </span>
      ))}
      {p.plan && (
        <span className="tp-part tp-plan">
          plan {p.plan.done}/{p.plan.total}
        </span>
      )}
    </Tag>
  );
});
