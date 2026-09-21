/**
 * Token-usage accounting for the Token usage right-panel surface: whole-thread
 * IN/OUT totals, a decomposition of what each turn cost, and the share spent
 * by subagents.
 *
 * Reads only what the client read model already carries:
 * - `context-window.updated` activities: the main thread's provider-reported
 *   usage snapshot per turn (the server retains the latest snapshot per turn).
 * - The subagent fold (`deriveAgentPanelModel`): each agent's provider-merged
 *   usage, plus its first observation for turn attribution.
 *
 * Nothing here estimates. Providers that report no usage leave the surface
 * empty rather than showing zeros, and an agent without a usage frame reports
 * "—" instead of 0. Agents are attributed to the turn that first observed them,
 * so a turn's subagent column reads as "tokens spent by agents it spawned".
 */
import type {
  AgentPanelModel,
  RuntimeSubagent,
  RuntimeSubagentStatus,
} from "@t3tools/client-runtime/state/subagentRuntime";
import {
  formatSubagentModelLabel,
  formatSubagentTokenCount,
} from "@t3tools/client-runtime/state/subagentRuntime";
import type { OrchestrationAgentLogEntry, OrchestrationThreadActivity } from "@t3tools/contracts";
import type { TimestampFormat } from "@t3tools/contracts/settings";

import type { ContextWindowSnapshot } from "../lib/contextWindow";
import { formatDayAwareTimestamp } from "../timestampFormat";
import { collectDisplayAgents } from "./AgentsPanel.logic";
import { type ContextUsageSummary, deriveContextUsage } from "./ContextUsageBar.logic";

export interface TokenUsageTotals {
  readonly inputTokens: number;
  readonly outputTokens: number;
  readonly cachedInputTokens: number;
  readonly reasoningOutputTokens: number;
  readonly mainTokens: number;
  readonly subagentTokens: number;
  readonly totalTokens: number;
  /** Provider-reported cumulative processed tokens, when the provider sends it. */
  readonly processedTokens: number | null;
  readonly agentCount: number;
}

export interface TokenUsageTurnRow {
  readonly turnId: string;
  /** 1-based position in the thread's turn timeline. */
  readonly ordinal: number;
  readonly at: string;
  readonly timeLabel: string;
  readonly inputTokens: number;
  readonly outputTokens: number;
  readonly cachedInputTokens: number;
  readonly subagentTokens: number;
  readonly totalTokens: number;
  readonly agentCount: number;
  readonly inputLabel: string;
  readonly outputLabel: string;
  readonly cachedLabel: string;
  readonly subagentLabel: string;
  readonly totalLabel: string;
}

export interface TokenUsageAgentRow {
  readonly id: string;
  readonly title: string;
  readonly role: string | null;
  readonly modelLabel: string | null;
  readonly status: RuntimeSubagentStatus;
  readonly totalTokens: number | null;
  readonly totalLabel: string;
  readonly inputLabel: string;
  readonly outputLabel: string;
  readonly toolUses: number | null;
}

/**
 * The thread's orchestrator (main-thread provider snapshots), surfaced as an
 * AGENTS row in workflow threads. `modelLabel`/`status` come from the newest
 * workflow's coordinator when the roster carries one.
 */
export interface TokenUsageOrchestratorRow {
  readonly id: string;
  readonly title: string;
  readonly modelLabel: string | null;
  readonly status: RuntimeSubagentStatus | null;
  readonly inputTokens: number;
  readonly outputTokens: number;
  readonly totalTokens: number;
  readonly inputLabel: string;
  readonly outputLabel: string;
  readonly totalLabel: string;
  readonly processedTokens: number | null;
}

export interface TokenUsageModel {
  readonly totals: TokenUsageTotals;
  readonly turns: ReadonlyArray<TokenUsageTurnRow>;
  readonly agents: ReadonlyArray<TokenUsageAgentRow>;
  readonly orchestrator: TokenUsageOrchestratorRow | null;
  readonly context: ContextUsageSummary | null;
  readonly hasUsage: boolean;
}

/** One subagent model step ("turn"), decomposed from a usage log entry. */
export interface SubagentUsageTurn {
  readonly ordinal: number;
  /** Input fed to the model this step; includes the cached subset. */
  readonly inputTokens: number;
  readonly cachedInputTokens: number;
  /** Output produced this step; includes the reasoning subset. */
  readonly outputTokens: number;
  readonly reasoningOutputTokens: number;
  readonly totalTokens: number;
  /** Context size this step: input + cached tokens. */
  readonly contextTokens: number;
  readonly inputLabel: string;
  readonly cachedLabel: string;
  readonly outputLabel: string;
  readonly reasoningLabel: string;
  readonly totalLabel: string;
}

/**
 * Per-subagent stats for the agents drill-in: each provider-reported model step
 * as a turn, with the context size fed into it. Derived from the usage entries
 * the adapter persists in the agent's log; providers that report no per-step
 * usage leave the surface empty rather than estimating.
 */
export interface SubagentUsageStats {
  readonly turns: ReadonlyArray<SubagentUsageTurn>;
  readonly turnCount: number;
  readonly totalTokens: number;
  readonly inputTokens: number;
  readonly cachedInputTokens: number;
  readonly outputTokens: number;
  readonly reasoningOutputTokens: number;
  /** Latest step's context size, or null when no step was reported. */
  readonly contextTokens: number | null;
  readonly totalLabel: string;
  readonly inputLabel: string;
  readonly cachedLabel: string;
  readonly outputLabel: string;
  readonly reasoningLabel: string;
  readonly contextLabel: string | null;
  readonly hasUsage: boolean;
}

export function deriveSubagentUsageStats(
  entries: ReadonlyArray<OrchestrationAgentLogEntry>,
): SubagentUsageStats {
  const turns: SubagentUsageTurn[] = [];
  let inputTokens = 0;
  let cachedInputTokens = 0;
  let outputTokens = 0;
  let reasoningOutputTokens = 0;
  let totalTokens = 0;

  for (const entry of entries) {
    if (entry.kind !== "usage") {
      continue;
    }
    const cached = entry.cachedInputTokens ?? 0;
    const reasoning = entry.reasoningOutputTokens ?? 0;
    // Match the thread snapshot convention: input/output include their cached
    // and reasoning subsets, so the subsets render beside the sums, never added.
    const input = (entry.inputTokens ?? 0) + cached;
    const output = (entry.outputTokens ?? 0) + reasoning;
    const context = input;
    turns.push({
      ordinal: turns.length + 1,
      inputTokens: input,
      cachedInputTokens: cached,
      outputTokens: output,
      reasoningOutputTokens: reasoning,
      totalTokens: entry.totalTokens,
      contextTokens: context,
      inputLabel: formatSubagentTokenCount(input),
      cachedLabel: formatSubagentTokenCount(cached),
      outputLabel: formatSubagentTokenCount(output),
      reasoningLabel: formatSubagentTokenCount(reasoning),
      totalLabel: formatSubagentTokenCount(entry.totalTokens),
    });
    inputTokens += input;
    cachedInputTokens += cached;
    outputTokens += output;
    reasoningOutputTokens += reasoning;
    totalTokens += entry.totalTokens;
  }

  const contextTokens = turns.length > 0 ? turns[turns.length - 1]!.contextTokens : null;
  return {
    turns,
    turnCount: turns.length,
    totalTokens,
    inputTokens,
    cachedInputTokens,
    outputTokens,
    reasoningOutputTokens,
    contextTokens,
    totalLabel: formatSubagentTokenCount(totalTokens),
    inputLabel: formatSubagentTokenCount(inputTokens),
    cachedLabel: formatSubagentTokenCount(cachedInputTokens),
    outputLabel: formatSubagentTokenCount(outputTokens),
    reasoningLabel: formatSubagentTokenCount(reasoningOutputTokens),
    contextLabel: contextTokens === null ? null : formatSubagentTokenCount(contextTokens),
    hasUsage: turns.length > 0,
  };
}

interface MutableTurn {
  turnId: string;
  at: string;
  ordinal: number;
  inputTokens: number;
  outputTokens: number;
  cachedInputTokens: number;
  reasoningOutputTokens: number;
  processedTokens: number | null;
  hasMainUsage: boolean;
  subagentTokens: number;
  agentCount: number;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" ? (value as Record<string, unknown>) : null;
}

function asCount(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : null;
}

function agentTotalTokens(agent: RuntimeSubagent): number | null {
  const total = agent.usage?.totalTokens;
  return total !== undefined && Number.isFinite(total) ? total : null;
}

function countLabel(value: number | null): string {
  return value === null ? "—" : formatSubagentTokenCount(value);
}

/**
 * The latest snapshot within a turn wins; earlier frames are superseded by the
 * read model (server projection and client reducer keep one per turn), so
 * there is nothing to accumulate here. The provider's cumulative session total
 * arrives on each snapshot as `totalProcessedTokens`.
 */
function applyMainUsage(turn: MutableTurn, activity: OrchestrationThreadActivity): void {
  if (activity.kind !== "context-window.updated") {
    return;
  }
  const payload = asRecord(activity.payload);
  if (payload === null) {
    return;
  }
  const input = asCount(payload.lastInputTokens) ?? asCount(payload.inputTokens);
  const output = asCount(payload.lastOutputTokens) ?? asCount(payload.outputTokens);
  const cached = asCount(payload.lastCachedInputTokens) ?? asCount(payload.cachedInputTokens);
  const reasoning =
    asCount(payload.lastReasoningOutputTokens) ?? asCount(payload.reasoningOutputTokens);
  const processed = asCount(payload.totalProcessedTokens);
  if (
    input === null &&
    output === null &&
    cached === null &&
    reasoning === null &&
    processed === null
  ) {
    return;
  }
  turn.inputTokens = input ?? 0;
  turn.outputTokens = output ?? 0;
  turn.cachedInputTokens = cached ?? 0;
  turn.reasoningOutputTokens = reasoning ?? 0;
  if (processed !== null) {
    turn.processedTokens = Math.max(turn.processedTokens ?? 0, processed);
  }
  turn.hasMainUsage = true;
}

/**
 * Display data for the Token usage surface. Totals cover every retained
 * activity in the thread; per-turn rows decompose them by the turn recorded on
 * each activity. Snapshots without a turn (provider resume handshakes) still
 * count toward totals but have no row to land in.
 */
export function deriveTokenUsage(input: {
  readonly activities: ReadonlyArray<OrchestrationThreadActivity>;
  readonly model: AgentPanelModel;
  readonly contextWindow: ContextWindowSnapshot | null;
  readonly timestampFormat: TimestampFormat;
}): TokenUsageModel {
  const turns = new Map<string, MutableTurn>();
  let detachedInputTokens = 0;
  let detachedOutputTokens = 0;
  let detachedCachedInputTokens = 0;
  let detachedReasoningOutputTokens = 0;
  let detachedProcessedTokens: number | null = null;

  for (const activity of input.activities) {
    const turnId = activity.turnId;
    if (turnId === null) {
      if (activity.kind === "context-window.updated") {
        const detached: MutableTurn = {
          turnId: "",
          at: activity.createdAt,
          ordinal: 0,
          inputTokens: 0,
          outputTokens: 0,
          cachedInputTokens: 0,
          reasoningOutputTokens: 0,
          processedTokens: null,
          hasMainUsage: false,
          subagentTokens: 0,
          agentCount: 0,
        };
        applyMainUsage(detached, activity);
        detachedInputTokens += detached.inputTokens;
        detachedOutputTokens += detached.outputTokens;
        detachedCachedInputTokens += detached.cachedInputTokens;
        detachedReasoningOutputTokens += detached.reasoningOutputTokens;
        if (detached.processedTokens !== null) {
          detachedProcessedTokens = Math.max(
            detachedProcessedTokens ?? 0,
            detached.processedTokens,
          );
        }
      }
      continue;
    }

    let turn = turns.get(turnId);
    if (turn === undefined) {
      turn = {
        turnId,
        at: activity.createdAt,
        ordinal: turns.size + 1,
        inputTokens: 0,
        outputTokens: 0,
        cachedInputTokens: 0,
        reasoningOutputTokens: 0,
        processedTokens: null,
        hasMainUsage: false,
        subagentTokens: 0,
        agentCount: 0,
      };
      turns.set(turnId, turn);
    }
    applyMainUsage(turn, activity);
  }

  const turnList = Array.from(turns.values());
  const agents = collectDisplayAgents(input.model);

  let inputTokens = detachedInputTokens;
  let outputTokens = detachedOutputTokens;
  let cachedInputTokens = detachedCachedInputTokens;
  let reasoningOutputTokens = detachedReasoningOutputTokens;
  let processedTokens = detachedProcessedTokens;
  for (const turn of turnList) {
    inputTokens += turn.inputTokens;
    outputTokens += turn.outputTokens;
    cachedInputTokens += turn.cachedInputTokens;
    reasoningOutputTokens += turn.reasoningOutputTokens;
    if (turn.processedTokens !== null) {
      processedTokens = Math.max(processedTokens ?? 0, turn.processedTokens);
    }
  }
  // Snapshot input/output already include the cached and reasoning subsets
  // (UsageTokenTotals contract), so those categories are displayed beside the
  // sums, never added on top.
  const mainTokens = inputTokens + outputTokens;

  // Workflow threads run an orchestrator whose provider frames land as this
  // thread's own snapshots, so the main-thread sums ARE the orchestrator's.
  // Only name it for workflow threads, and only once it reported something.
  const isWorkflowThread =
    input.model.workflows.length > 0 ||
    agents.some((agent) => agent.workflowName === "AgentTunnel");
  const coordinator = input.model.workflows.at(-1)?.workflow ?? null;
  const orchestrator: TokenUsageOrchestratorRow | null =
    isWorkflowThread && (inputTokens > 0 || outputTokens > 0 || processedTokens !== null)
      ? {
          id: coordinator?.id ?? "orchestrator",
          title: "Orchestrator",
          modelLabel:
            coordinator === null
              ? null
              : formatSubagentModelLabel(coordinator.model, coordinator.effort),
          status: coordinator?.status ?? null,
          inputTokens,
          outputTokens,
          totalTokens: mainTokens,
          inputLabel: formatSubagentTokenCount(inputTokens),
          outputLabel: formatSubagentTokenCount(outputTokens),
          totalLabel: formatSubagentTokenCount(mainTokens),
          processedTokens,
        }
      : null;

  // A member-less workflow's coordinator stands in as its roster row
  // (collectDisplayAgents), but the orchestrator row already names it, so drop
  // the duplicate and render that entity once. Subagent sums come from these
  // same rows, so a hidden coordinator can never inflate the Subagent tile.
  const visibleAgents =
    orchestrator === null ? agents : agents.filter((agent) => agent.id !== orchestrator.id);

  // Subagents are attributed to the turn that first observed them: the turn
  // "bought" that agent's work, even when the agent outlives the pass.
  const turnForTime = (at: string): MutableTurn | null => {
    let match: MutableTurn | null = null;
    for (const turn of turnList) {
      if (turn.at <= at) {
        match = turn;
      } else {
        break;
      }
    }
    return match ?? turnList[0] ?? null;
  };
  for (const agent of visibleAgents) {
    const total = agentTotalTokens(agent);
    if (total === null || total <= 0) {
      continue;
    }
    const turn = turnForTime(agent.firstSeenAt);
    if (turn === null) {
      continue;
    }
    turn.subagentTokens += total;
    turn.agentCount += 1;
  }

  const rows = turnList
    .filter((turn) => turn.hasMainUsage || turn.subagentTokens > 0)
    .map((turn): TokenUsageTurnRow => {
      const totalTokens = turn.inputTokens + turn.outputTokens + turn.subagentTokens;
      return {
        turnId: turn.turnId,
        ordinal: turn.ordinal,
        at: turn.at,
        timeLabel: formatDayAwareTimestamp(turn.at, input.timestampFormat) || "—",
        inputTokens: turn.inputTokens,
        outputTokens: turn.outputTokens,
        cachedInputTokens: turn.cachedInputTokens,
        subagentTokens: turn.subagentTokens,
        totalTokens,
        agentCount: turn.agentCount,
        inputLabel: formatSubagentTokenCount(turn.inputTokens),
        outputLabel: formatSubagentTokenCount(turn.outputTokens),
        cachedLabel: formatSubagentTokenCount(turn.cachedInputTokens),
        subagentLabel: formatSubagentTokenCount(turn.subagentTokens),
        totalLabel: formatSubagentTokenCount(totalTokens),
      };
    });

  const subagentTokens = visibleAgents.reduce(
    (sum, agent) => sum + (agentTotalTokens(agent) ?? 0),
    0,
  );

  const agentRows = visibleAgents.map((agent): TokenUsageAgentRow => {
    const usage = agent.usage;
    return {
      id: agent.id,
      title: agent.title,
      role: agent.role,
      modelLabel: formatSubagentModelLabel(agent.model, agent.effort),
      status: agent.status,
      totalTokens: agentTotalTokens(agent),
      totalLabel: usage === null ? "—" : formatSubagentTokenCount(usage.totalTokens),
      inputLabel: countLabel(usage?.inputTokens ?? null),
      outputLabel: countLabel(usage?.outputTokens ?? null),
      toolUses: usage?.toolUses ?? null,
    };
  });

  return {
    totals: {
      inputTokens,
      outputTokens,
      cachedInputTokens,
      reasoningOutputTokens,
      mainTokens,
      subagentTokens,
      totalTokens: mainTokens + subagentTokens,
      processedTokens,
      // Counts every row the AGENTS table renders, orchestrator included.
      agentCount: agentRows.length + (orchestrator === null ? 0 : 1),
    },
    turns: rows,
    agents: agentRows,
    orchestrator,
    context: deriveContextUsage(input.contextWindow),
    hasUsage:
      rows.length > 0 ||
      agentRows.some((row) => row.totalTokens !== null && row.totalTokens > 0) ||
      inputTokens > 0 ||
      outputTokens > 0 ||
      cachedInputTokens > 0 ||
      reasoningOutputTokens > 0 ||
      processedTokens !== null,
  };
}
