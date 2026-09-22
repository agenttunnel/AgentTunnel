import {
  isTerminalSubagentStatus,
  type AgentPanelModel,
  type AgentPanelWorkflowGroup,
  type RuntimeSubagent,
} from "@t3tools/client-runtime/state/subagentRuntime";

export function workflowMembers(group: AgentPanelWorkflowGroup): ReadonlyArray<RuntimeSubagent> {
  return [...group.phases.flatMap((phase) => phase.members), ...group.unphasedMembers];
}

/**
 * One row per rendered agents-panel entry, never the coordinator of a populated
 * workflow (the coordinator stands in only when it has no members).
 */
export function collectDisplayAgents(model: AgentPanelModel): ReadonlyArray<RuntimeSubagent> {
  const workflowEntries = model.workflows.flatMap((group) => {
    const members = workflowMembers(group);
    return members.length > 0 ? members : [group.workflow];
  });
  return [...workflowEntries, ...model.directAgents];
}

/** Resolves the roster row behind a drill-in selection, across every section. */
export function findAgentById(model: AgentPanelModel, agentId: string): RuntimeSubagent | null {
  for (const group of model.workflows) {
    if (group.workflow.id === agentId) {
      return group.workflow;
    }
    for (const phase of group.phases) {
      const member = phase.members.find((entry) => entry.id === agentId);
      if (member) {
        return member;
      }
    }
    const unphased = group.unphasedMembers.find((entry) => entry.id === agentId);
    if (unphased) {
      return unphased;
    }
  }
  return model.directAgents.find((entry) => entry.id === agentId) ?? null;
}

/**
 * Which control an agent row offers:
 * - `stop` while the row is in flight (the same states the panel paints as
 *   Working) — interrupting is only meaningful before the agent settles;
 * - `retry` once it settled as Failed or Stopped (`interrupted` is what the
 *   client fold makes of a provider "stopped" row);
 * - `null` for rows with nothing to control (completed, idle, still unknown).
 */
export type AgentControlAction = "stop" | "retry";

export function agentControlAction(agent: RuntimeSubagent): AgentControlAction | null {
  if (agent.status === "running" || agent.status === "pending" || agent.status === "waiting") {
    return "stop";
  }
  return agent.status === "failed" || agent.status === "interrupted" ? "retry" : null;
}

/**
 * Chip text for a reactivated slot: the same agentId comes back on a higher
 * attempt, and the row must say so instead of looking like a fresh spawn.
 */
export function agentAttemptLabel(agent: RuntimeSubagent): string | null {
  return agent.attempt !== null && agent.attempt > 1 ? `attempt ${agent.attempt}` : null;
}

/**
 * Fallback run counter for providers that report reactivations without an
 * attempt number. When a provider marks attempts, that chip already names the
 * run, so the fallback stays quiet instead of numbering it twice.
 */
export function agentRunLabel(agent: RuntimeSubagent): string | null {
  return agentAttemptLabel(agent) === null && agent.activationCount > 1
    ? `run ${agent.activationCount}`
    : null;
}

/** Active roster rows, most recently updated first — the "Running now" list. */
export function liveAgents(model: AgentPanelModel): ReadonlyArray<RuntimeSubagent> {
  const members = [
    ...model.workflows.flatMap((group) => workflowMembers(group)),
    ...model.directAgents,
  ];
  return members
    .filter(
      (agent) =>
        agent.status === "running" || agent.status === "pending" || agent.status === "waiting",
    )
    .sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
}

/** Settled and failed counts for a header that summarizes a member roster. */
export interface SubagentTally {
  readonly total: number;
  readonly settled: number;
  readonly failed: number;
}

export function tallySubagents(members: ReadonlyArray<RuntimeSubagent>): SubagentTally {
  let settled = 0;
  let failed = 0;
  for (const member of members) {
    if (member.status === "failed") failed += 1;
    if (isTerminalSubagentStatus(member.status)) settled += 1;
  }
  return { total: members.length, settled, failed };
}

/**
 * Workflow header counter. Failures lead: a run that lost an agent is never
 * summarized as merely unfinished.
 */
export function workflowTallyLabel(tally: SubagentTally): string {
  const settled = `${tally.settled}/${tally.total} settled`;
  return tally.failed > 0 ? `${tally.failed} failed · ${settled}` : settled;
}

/**
 * Phase header text. A phase whose members failed is not "done" to the reader
 * however settled it is, so the failure count leads and whatever finished or is
 * still running follows.
 */
export function phaseStatusLabel(phase: {
  readonly state: "pending" | "running" | "done";
  readonly members: ReadonlyArray<RuntimeSubagent>;
  readonly activeCount: number;
}): string {
  const tally = tallySubagents(phase.members);
  if (tally.failed > 0) {
    const parts = [`${tally.failed} failed`];
    if (phase.activeCount > 0) parts.push(`${phase.activeCount} active`);
    const done = tally.settled - tally.failed;
    if (done > 0) parts.push(`${done} done`);
    return parts.join(" · ");
  }
  if (phase.state === "done") return `${tally.settled} done`;
  if (tally.total === 0 || phase.activeCount === 0) return "pending";
  return `${phase.activeCount} active · ${tally.settled} done`;
}

/**
 * One run of a slot as this panel can see it: what a queued retry is waiting to
 * change. Status covers the terminal → running reopen; attempt and startedAt
 * cover a retry that came back on a new attempt before any status row arrived.
 */
export function subagentRunKey(agent: RuntimeSubagent): string {
  return `${agent.status}\u0000${agent.attempt ?? ""}\u0000${agent.startedAt ?? ""}`;
}

/** Retries the user has asked for, keyed by agent id → the run key at dispatch. */
export type QueuedRetries = ReadonlyMap<string, string>;

/**
 * Drops queued retries whose slot moved on: the retry has been applied (the row
 * reopened, or came back on a new attempt) or the row left the roster. Returns
 * the input map when nothing changed, so callers can hold it in state without
 * re-rendering on every fold.
 */
export function pruneQueuedRetries(
  queued: QueuedRetries,
  agents: ReadonlyMap<string, RuntimeSubagent>,
): QueuedRetries {
  if (queued.size === 0) return queued;
  let remaining: Map<string, string> | null = null;
  for (const [agentId, runKey] of queued) {
    const agent = agents.get(agentId);
    if (agent !== undefined && subagentRunKey(agent) === runKey) continue;
    remaining ??= new Map(queued);
    remaining.delete(agentId);
  }
  return remaining ?? queued;
}
