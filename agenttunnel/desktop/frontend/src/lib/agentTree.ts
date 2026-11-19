// What the multi-agent views show about each agent, derived from the folded
// thread state alone: an honest phase (a pending agent is blocked or queued,
// a running one with an open question needs you), the dispatch tree, which
// dependencies hold an agent back, and the team's progress. Pure: no React.

import type { AgentQuestion } from "./questions";

/** The agent fields these helpers read; a thread agent satisfies it. */
export type TreeAgent = {
  id: string;
  parentAgentId?: string;
  name?: string;
  role: string;
  task?: string;
  status: string;
  dependsOn?: string[];
  startedAt?: string;
  endedAt?: string;
};

/**
 * Where an agent stands, as a person reads it:
 * running, waiting on you (an open question), blocked on a dependency that has
 * not succeeded, queued (pending with nothing in its way), done, failed, or
 * stopped (cancelled by the user or a stopped turn).
 */
export type AgentPhase = "running" | "waiting" | "blocked" | "queued" | "done" | "failed" | "stopped";

export const PHASE_LABEL: Record<AgentPhase, string> = {
  running: "Running",
  waiting: "Needs you",
  blocked: "Blocked",
  queued: "Queued",
  done: "Done",
  failed: "Failed",
  stopped: "Stopped",
};

/** True while the agent's run can still change: the only phases that tick. */
export const phaseLive = (p: AgentPhase) => p === "running" || p === "waiting";

/** The display name every agent view uses. */
export const agentName = (a: Pick<TreeAgent, "name" | "role">, roleNames: Record<string, string>) =>
  a.name || roleNames[a.role] || a.role;

/**
 * Resolves `dependsOn` names to agents the way the scheduler names tasks: a
 * dependency is another agent's name or task, and the latest match wins.
 * Unresolved names are skipped (the scheduler ignores them too).
 */
export function resolveDeps<A extends TreeAgent>(a: A, agents: A[]): A[] {
  const out: A[] = [];
  for (const dep of a.dependsOn ?? []) {
    let hit: A | null = null;
    for (const c of agents) if (c !== a && (c.name === dep || c.task === dep)) hit = c;
    if (hit && !out.includes(hit)) out.push(hit);
  }
  return out;
}

/** The dependencies still holding a pending agent back: any that has not succeeded. */
export function blockers<A extends TreeAgent>(a: A, agents: A[]): A[] {
  if (a.status !== "pending") return [];
  return resolveDeps(a, agents).filter((d) => d.status !== "succeeded");
}

export function agentPhase(
  a: TreeAgent,
  agents: TreeAgent[],
  askingIds: ReadonlySet<string>,
): AgentPhase {
  switch (a.status) {
    case "running":
      return askingIds.has(a.id) ? "waiting" : "running";
    case "succeeded":
      return "done";
    case "failed":
      return "failed";
    case "cancelled":
      return "stopped";
    default:
      return blockers(a, agents).length > 0 ? "blocked" : "queued";
  }
}

/** Ids of agents with a question still waiting on the human. */
export function askingAgents(questions: AgentQuestion[] | null | undefined): Set<string> {
  const out = new Set<string>();
  for (const q of questions ?? []) if (q.status === "pending") out.add(q.agentId);
  return out;
}

/**
 * When the agent actually started working. Dispatch time is `startedAt`, but
 * an agent with dependencies runs only after they end, so the latest
 * dependency end counts instead when it is later.
 */
export function workStart<A extends TreeAgent>(a: A, agents: A[]): string | undefined {
  let start = a.startedAt;
  for (const d of resolveDeps(a, agents)) {
    if (d.endedAt && (!start || Date.parse(d.endedAt) > Date.parse(start))) start = d.endedAt;
  }
  return start;
}

export type TreeRow<A extends TreeAgent> = {
  agent: A;
  depth: number;
  phase: AgentPhase;
  /** The dependencies blocking it, for the "after X" line. */
  blockedBy: A[];
  /** Start of real work (see workStart); the elapsed timer counts from here. */
  since?: string;
  /** Agents beneath it in the tree, at any depth. */
  descendants: number;
};

/**
 * The dispatch tree flattened depth-first: each lead, then the agents it
 * dispatched, nested, in dispatch order. An agent whose parent is missing (or
 * part of a cycle) is shown as a root so it is never dropped.
 */
export function agentTree<A extends TreeAgent>(
  agents: A[],
  questions?: AgentQuestion[] | null,
): TreeRow<A>[] {
  const asking = askingAgents(questions);
  const byId = new Map(agents.map((a) => [a.id, a]));
  const children = new Map<string, A[]>();
  const roots: A[] = [];
  for (const a of agents) {
    const p = a.parentAgentId && a.parentAgentId !== a.id ? byId.get(a.parentAgentId) : undefined;
    if (p) {
      const list = children.get(p.id);
      if (list) list.push(a);
      else children.set(p.id, [a]);
    } else roots.push(a);
  }
  const out: TreeRow<A>[] = [];
  const seen = new Set<string>();
  const visit = (a: A, depth: number): number => {
    if (seen.has(a.id)) return 0;
    seen.add(a.id);
    const row: TreeRow<A> = {
      agent: a,
      depth,
      phase: agentPhase(a, agents, asking),
      blockedBy: blockers(a, agents),
      since: workStart(a, agents),
      descendants: 0,
    };
    out.push(row);
    for (const c of children.get(a.id) ?? []) row.descendants += 1 + visit(c, depth + 1);
    return row.descendants;
  };
  for (const r of roots) visit(r, 0);
  // Members of a parent cycle are reachable from no root; keep them visible.
  for (const a of agents) if (!seen.has(a.id)) visit(a, 0);
  return out;
}

export type TeamProgress = {
  /** Dispatched work: every agent except the leads. */
  total: number;
  done: number;
  running: number;
  waiting: number;
  blocked: number;
  failed: number;
  /** The leads' agreed plan, when they wrote one. */
  plan: { done: number; total: number } | null;
};

/**
 * The team's progress for the thread header: dispatched agents by phase, plus
 * the plan's checklist when the leads keep one. Leads are not work items, so
 * they are left out of the agent counts.
 */
export function teamProgress(
  agents: TreeAgent[],
  questions: AgentQuestion[] | null | undefined,
  tasks: { status: string }[] | null | undefined,
): TeamProgress {
  const asking = askingAgents(questions);
  const p: TeamProgress = { total: 0, done: 0, running: 0, waiting: 0, blocked: 0, failed: 0, plan: null };
  for (const a of agents) {
    if (!a.parentAgentId) continue;
    p.total++;
    switch (agentPhase(a, agents, asking)) {
      case "done":
        p.done++;
        break;
      case "running":
        p.running++;
        break;
      case "waiting":
        p.waiting++;
        break;
      case "blocked":
        p.blocked++;
        break;
      case "failed":
        p.failed++;
        break;
    }
  }
  if (tasks && tasks.length > 0) {
    p.plan = { done: tasks.filter((t) => t.status === "done").length, total: tasks.length };
  }
  return p;
}

/** Seconds as "45s", "3m 07s" or "1h 02m". */
export function fmtElapsed(secs: number): string {
  const s = Math.max(0, Math.floor(secs));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, "0")}m`;
}
