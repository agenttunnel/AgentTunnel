import {
  deriveAgentPanelModel,
  formatSubagentModelLabel,
  formatSubagentTokenCount,
  isActiveSubagentStatus,
  type AgentPanelModel,
  type RuntimeSubagent,
} from "@t3tools/client-runtime/state/subagentRuntime";

/**
 * The Agents screen of the terminal client: one row per sub-agent, plus the
 * token-usage summary under the list. Everything here is pure — the fold and
 * the panel model come from `client-runtime`, and the clock is a parameter —
 * so a TUI session and a browser session describe the same work the same way
 * and both can be exercised without a terminal or a server.
 */

export interface TuiAgentRow {
  readonly id: string;
  readonly label: string; // role or title
  readonly status: string; // RuntimeSubagentStatus
  readonly activity: string; // latest progress/result/error line, trimmed to one line
  readonly model: string | null; // formatSubagentModelLabel output
  readonly tokens: string | null; // formatSubagentTokenCount output
  readonly tools: number | null;
  readonly elapsed: string | null; // "1m 54s" style, from startedAt/completedAt/durationMs
  readonly attempt: number | null; // activation/attempt count when > 1
  readonly outputPath: string | null; // the agent's log file, for the log view
}

/** The web panel's elapsed format, so both clients spell a run the same way. */
function formatElapsedSeconds(totalSeconds: number): string {
  const seconds = Math.max(0, Math.floor(totalSeconds));
  const minutes = Math.floor(seconds / 60);
  if (minutes === 0) {
    return `${seconds}s`;
  }
  const hours = Math.floor(minutes / 60);
  if (hours === 0) {
    return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`;
  }
  return `${hours}h ${String(minutes % 60).padStart(2, "0")}m`;
}

/** The caller's clock, as an ISO string or an epoch millisecond count; a value
 * that is not a time at all reads as "unknown". */
function toMillis(now: string | number): number | null {
  if (typeof now === "number") {
    return Number.isFinite(now) ? now : null;
  }
  const parsed = Date.parse(now);
  return Number.isNaN(parsed) ? null : parsed;
}

/**
 * How long the current run took: the provider's own duration when it reported
 * one, otherwise `startedAt` up to the completion (or, for a run that is still
 * going, up to the caller's `now`). Without a clock there is no end to measure
 * to, so a live run simply has no elapsed yet.
 */
function elapsedFor(agent: RuntimeSubagent, now: string | number | undefined): string | null {
  const durationMs = agent.usage?.durationMs;
  if (durationMs !== undefined) {
    return formatElapsedSeconds(durationMs / 1000);
  }
  if (agent.startedAt === null) {
    return null;
  }
  const start = Date.parse(agent.startedAt);
  if (Number.isNaN(start)) {
    return null;
  }
  const completed = agent.completedAt === null ? null : Date.parse(agent.completedAt);
  const end =
    completed !== null && !Number.isNaN(completed)
      ? completed
      : now === undefined
        ? null
        : toMillis(now);
  if (end === null) {
    return null;
  }
  return formatElapsedSeconds((end - start) / 1000);
}

/**
 * The roster the panel actually renders: workflow members, a coordinator only
 * while it has no members (a populated coordinator is a container, not work of
 * its own), and direct spawns.
 */
function displayAgents(model: AgentPanelModel): ReadonlyArray<RuntimeSubagent> {
  const workflowRows = model.workflows.flatMap((group) => {
    const members = [...group.phases.flatMap((phase) => phase.members), ...group.unphasedMembers];
    return members.length > 0 ? members : [group.workflow];
  });
  return [...workflowRows, ...model.directAgents];
}

/**
 * Live-first, the panel's own order: everything in flight leads, most recently
 * active first; settled rows follow in the roster's stable first-seen order so
 * a fold never reshuffles finished work.
 */
function orderedRoster(model: AgentPanelModel): ReadonlyArray<RuntimeSubagent> {
  const roster = displayAgents(model);
  const live = roster
    .filter((agent) => isActiveSubagentStatus(agent.status))
    .slice()
    .sort(
      (left, right) =>
        right.updatedAt.localeCompare(left.updatedAt) || left.id.localeCompare(right.id),
    );
  const settled = roster
    .filter((agent) => !isActiveSubagentStatus(agent.status))
    .slice()
    .sort(
      (left, right) =>
        left.firstSeenAt.localeCompare(right.firstSeenAt) || left.id.localeCompare(right.id),
    );
  return [...live, ...settled];
}

/**
 * One row per sub-agent. `now` is the caller's clock (ISO string or epoch
 * milliseconds): a running agent's elapsed time is measured against it — never
 * against a clock read in here — so the screen re-renders the same rows for the
 * same instant, and a live row without a clock simply has no elapsed yet.
 */
export function agentRows(
  agents: ReadonlyArray<RuntimeSubagent>,
  now?: string | number,
): ReadonlyArray<TuiAgentRow> {
  return orderedRoster(deriveAgentPanelModel({ agents })).map((agent) => ({
    id: agent.id,
    label: agent.role ?? agent.title,
    status: agent.status,
    // What is happening now, or how it ended; the fold already bounds the
    // length, so collapsing the whitespace is all the row needs.
    activity: (agent.progress ?? agent.result ?? agent.error ?? "").replace(/\s+/g, " ").trim(),
    model: formatSubagentModelLabel(agent.model, agent.effort),
    tokens: agent.usage === null ? null : formatSubagentTokenCount(agent.usage.totalTokens),
    tools: agent.usage?.toolUses ?? null,
    elapsed: elapsedFor(agent, now),
    attempt: agent.attempt !== null && agent.attempt > 1 ? agent.attempt : null,
    outputPath: agent.outputFile,
  }));
}

/**
 * What each agent has spent, in row order, closed by the fleet total the panel
 * footer shows. A thread with no agents has nothing to summarize.
 */
/** What the fleet has spent and how it is doing, for the status line. */
export function fleetTotals(agents: ReadonlyArray<RuntimeSubagent>): {
  readonly tokens: number;
  readonly running: number;
  readonly settled: number;
  readonly failed: number;
} {
  let tokens = 0;
  let running = 0;
  let settled = 0;
  let failed = 0;
  for (const agent of agents) {
    tokens += agent.usage?.totalTokens ?? 0;
    if (isActiveSubagentStatus(agent.status)) running += 1;
    else if (agent.status === "failed" || agent.status === "cancelled") failed += 1;
    else settled += 1;
  }
  return { tokens, running, settled, failed };
}

export function agentUsageLines(agents: ReadonlyArray<RuntimeSubagent>): ReadonlyArray<string> {
  if (agents.length === 0) {
    return [];
  }
  const model = deriveAgentPanelModel({ agents });
  const lines = orderedRoster(model).flatMap((agent) => {
    const usage = agent.usage;
    if (usage === null) {
      return [];
    }
    const parts = [
      `${agent.role ?? agent.title} · ${formatSubagentTokenCount(usage.totalTokens)} tok`,
    ];
    if (usage.toolUses !== undefined) {
      parts.push(`${usage.toolUses} tools`);
    }
    return [parts.join(" · ")];
  });
  lines.push(`Σ ${formatSubagentTokenCount(model.totalTokens)} tok`);
  return lines;
}
