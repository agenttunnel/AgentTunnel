import type {
  AgentPanelModel,
  RuntimeSubagent,
  RuntimeSubagentStatus,
} from "@t3tools/client-runtime/state/subagentRuntime";
import { formatSubagentTokenCount } from "@t3tools/client-runtime/state/subagentRuntime";
import type { TimestampFormat } from "@t3tools/contracts/settings";

import type { ContextWindowSnapshot } from "../lib/contextWindow";
import { formatContextWindowTokens } from "../lib/contextWindow";
import { formatDayAwareTimestamp } from "../timestampFormat";
import type { AgentTunnelRoleOption } from "./agentTunnelRoles.logic";
import { workflowMembers } from "./AgentsPanel.logic";
import { type ContextUsageSummary, deriveContextUsage } from "./ContextUsageBar";

export type { ContextUsageSummary } from "./ContextUsageBar";

/** The thread fields the analysis reads; narrower than the full read model. */
export interface ContextAnalysisThread {
  readonly title: string;
  readonly createdAt: string;
  readonly updatedAt: string;
  readonly messages: ReadonlyArray<{ readonly role: string }>;
}

export interface ContextStat {
  readonly label: string;
  readonly value: string;
}

export interface ContextAgentRow {
  readonly id: string;
  readonly role: string | null;
  readonly title: string;
  readonly pipeline: string | null;
  readonly status: RuntimeSubagentStatus;
  readonly statusLabel: string;
  readonly duration: string | null;
  readonly tokens: string;
  /** Retained activity entries, one per line in the Agents structured log. */
  readonly outputLines: number;
}

export interface ContextRoleModelRow {
  readonly id: string;
  readonly label: string;
  readonly model: string | null;
  readonly auto: boolean;
  readonly value: string;
}

export interface ContextAnalysis {
  readonly stats: ReadonlyArray<ContextStat>;
  readonly usage: ContextUsageSummary | null;
  readonly roleModels: ReadonlyArray<ContextRoleModelRow>;
  readonly agents: ReadonlyArray<ContextAgentRow>;
  readonly hasAgents: boolean;
}

/** Mirrors the AgentsPanel status vocabulary so both surfaces read the same. */
const STATUS_LABELS: Record<RuntimeSubagentStatus, string> = {
  pending: "Working",
  running: "Working",
  waiting: "Working",
  idle: "Idle · resumable",
  completed: "Completed",
  failed: "Failed",
  cancelled: "Stopped",
  interrupted: "Stopped",
};

/** One row per rendered agents-panel entry, never the coordinator of a populated workflow. */
function collectDisplayAgents(model: AgentPanelModel): ReadonlyArray<RuntimeSubagent> {
  const workflowEntries = model.workflows.flatMap((group) => {
    const members = workflowMembers(group);
    return members.length > 0 ? members : [group.workflow];
  });
  return [...workflowEntries, ...model.directAgents];
}

function formatDurationMs(milliseconds: number): string {
  const seconds = Math.max(0, Math.floor(milliseconds / 1000));
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

/**
 * Elapsed time for the last observed activation: settled agents stop at
 * completedAt, live ones at their newest activity. No ticking clock here —
 * AgentsPanel owns the live timer.
 */
function agentDuration(agent: RuntimeSubagent): string | null {
  if (agent.startedAt === null) {
    return null;
  }
  const start = Date.parse(agent.startedAt);
  const end = Date.parse(agent.completedAt ?? agent.updatedAt);
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) {
    return null;
  }
  return formatDurationMs(end - start);
}

/**
 * Display data for the Context surface. Reads only fields the app tracks:
 * the thread read model, the latest context-window snapshot, the subagent
 * panel model, and the AgentTunnel role overrides. Nothing here invents a
 * cost, per-message, or tool-token number.
 */
export function deriveContextAnalysis(input: {
  readonly model: AgentPanelModel;
  readonly contextWindow: ContextWindowSnapshot | null;
  readonly thread: ContextAnalysisThread;
  readonly providerLabel: string;
  readonly modelLabel: string | null;
  readonly roles: ReadonlyArray<AgentTunnelRoleOption>;
  readonly timestampFormat: TimestampFormat;
}): ContextAnalysis {
  const usage = deriveContextUsage(input.contextWindow);
  const contextWindow = input.contextWindow;

  const userMessages = input.thread.messages.filter((message) => message.role === "user").length;
  const assistantMessages = input.thread.messages.filter(
    (message) => message.role === "assistant",
  ).length;

  const stats: ContextStat[] = [
    { label: "Session", value: input.thread.title },
    { label: "Provider", value: input.providerLabel },
    { label: "Model", value: input.modelLabel ?? "—" },
    { label: "Context limit", value: usage?.limitLabel ?? "Unknown" },
    {
      label: "Context used",
      value:
        usage === null
          ? "Unknown"
          : usage.percentLabel === null
            ? usage.usedLabel
            : `${usage.usedLabel} · ${usage.percentLabel}`,
    },
    { label: "Total tokens", value: formatSubagentTokenCount(input.model.totalTokens) },
  ];
  const inputTokens = contextWindow?.inputTokens ?? null;
  const outputTokens = contextWindow?.outputTokens ?? null;
  const cachedInputTokens = contextWindow?.cachedInputTokens ?? null;
  if (inputTokens !== null) {
    stats.push({ label: "Input tokens", value: formatContextWindowTokens(inputTokens) });
  }
  if (outputTokens !== null) {
    stats.push({ label: "Output tokens", value: formatContextWindowTokens(outputTokens) });
  }
  if (cachedInputTokens !== null) {
    stats.push({ label: "Cache tokens", value: formatContextWindowTokens(cachedInputTokens) });
  }
  stats.push(
    { label: "User messages", value: `${userMessages}` },
    { label: "Assistant messages", value: `${assistantMessages}` },
    {
      label: "Session created",
      value: formatDayAwareTimestamp(input.thread.createdAt, input.timestampFormat) || "—",
    },
    {
      label: "Last activity",
      value: formatDayAwareTimestamp(input.thread.updatedAt, input.timestampFormat) || "—",
    },
  );

  const agents = collectDisplayAgents(input.model).map((agent): ContextAgentRow => {
    return {
      id: agent.id,
      role: agent.role,
      title: agent.title,
      pipeline: agent.workflowName ?? agent.phaseTitle,
      status: agent.status,
      statusLabel: STATUS_LABELS[agent.status],
      duration: agentDuration(agent),
      tokens: agent.usage ? formatSubagentTokenCount(agent.usage.totalTokens) : "—",
      outputLines: agent.recentActivity.length,
    };
  });

  return {
    stats,
    usage,
    roleModels: input.roles.map((role) => ({
      id: role.id,
      label: role.label,
      model: role.model,
      auto: role.auto,
      value: role.auto ? "Auto - chosen by orchestrator" : (role.model ?? "Workflow default"),
    })),
    agents,
    hasAgents: input.model.hasAgents,
  };
}
