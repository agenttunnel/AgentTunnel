import type {
  AgentPanelModel,
  AgentPanelWorkflowGroup,
  RuntimeSubagent,
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
