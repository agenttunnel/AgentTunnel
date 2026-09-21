import type {
  AgentPanelModel,
  RuntimeSubagent,
} from "@t3tools/client-runtime/state/subagentRuntime";
import { emptyAgentPanelModel } from "@t3tools/client-runtime/state/subagentRuntime";
import {
  EventId,
  type OrchestrationAgentLogEntry,
  type OrchestrationThreadActivity,
  TurnId,
} from "@t3tools/contracts";
import { describe, expect, it } from "vite-plus/test";

import type { ContextWindowSnapshot } from "../lib/contextWindow";
import {
  deriveSubagentUsageStats,
  deriveTokenUsage,
  type TokenUsageModel,
} from "./TokenUsagePanel.logic";

function activity(input: {
  id: string;
  kind?: string;
  payload?: unknown;
  turnId?: string | null;
  createdAt?: string;
}): OrchestrationThreadActivity {
  return {
    id: EventId.make(input.id),
    tone: "info",
    kind: input.kind ?? "context-window.updated",
    summary: input.kind ?? "context-window.updated",
    payload: input.payload ?? {},
    turnId: input.turnId === null ? null : TurnId.make(input.turnId ?? "turn-1"),
    createdAt: input.createdAt ?? "2026-01-01T09:00:00.000Z",
  };
}

function contextPayload(input: {
  inputTokens?: number;
  outputTokens?: number;
  cachedInputTokens?: number;
  reasoningOutputTokens?: number;
  totalProcessedTokens?: number;
}): Record<string, number> {
  return {
    usedTokens: 1_000,
    ...(input.inputTokens !== undefined ? { inputTokens: input.inputTokens } : {}),
    ...(input.outputTokens !== undefined ? { outputTokens: input.outputTokens } : {}),
    ...(input.cachedInputTokens !== undefined
      ? { cachedInputTokens: input.cachedInputTokens }
      : {}),
    ...(input.reasoningOutputTokens !== undefined
      ? { reasoningOutputTokens: input.reasoningOutputTokens }
      : {}),
    ...(input.totalProcessedTokens !== undefined
      ? { totalProcessedTokens: input.totalProcessedTokens }
      : {}),
  };
}

function agent(overrides: Partial<RuntimeSubagent> & Pick<RuntimeSubagent, "id">): RuntimeSubagent {
  return {
    kind: "subagent",
    title: overrides.id,
    role: null,
    model: null,
    effort: null,
    status: "completed",
    activationCount: 1,
    usage: null,
    progress: null,
    lastToolName: null,
    result: null,
    error: null,
    outputFile: null,
    parentAgentId: null,
    agentIndex: null,
    phaseIndex: null,
    phaseTitle: null,
    attempt: null,
    workflowName: null,
    phases: [],
    runHandles: null,
    recentActivity: [],
    firstSeenAt: "2026-01-01T09:00:00.000Z",
    startedAt: "2026-01-01T09:00:00.000Z",
    completedAt: null,
    updatedAt: "2026-01-01T09:00:05.000Z",
    ...overrides,
  };
}

function contextWindow(overrides: Partial<ContextWindowSnapshot> = {}): ContextWindowSnapshot {
  return {
    usedTokens: 0,
    totalProcessedTokens: null,
    maxTokens: null,
    remainingTokens: null,
    usedPercentage: null,
    remainingPercentage: null,
    inputTokens: null,
    cachedInputTokens: null,
    outputTokens: null,
    reasoningOutputTokens: null,
    lastUsedTokens: null,
    lastInputTokens: null,
    lastCachedInputTokens: null,
    lastOutputTokens: null,
    lastReasoningOutputTokens: null,
    toolUses: null,
    durationMs: null,
    compactsAutomatically: false,
    autoCompactThreshold: null,
    updatedAt: "2026-01-01T09:30:00.000Z",
    ...overrides,
  };
}

function derive(input: {
  activities?: ReadonlyArray<OrchestrationThreadActivity>;
  model?: AgentPanelModel;
  contextWindow?: ContextWindowSnapshot | null;
}): TokenUsageModel {
  return deriveTokenUsage({
    activities: input.activities ?? [],
    model: input.model ?? emptyAgentPanelModel(),
    contextWindow: input.contextWindow ?? null,
    timestampFormat: "24-hour",
  });
}

describe("deriveTokenUsage", () => {
  it("reports no usage for an empty thread", () => {
    const model = derive({});

    expect(model.hasUsage).toBe(false);
    expect(model.turns).toEqual([]);
    expect(model.agents).toEqual([]);
    expect(model.totals).toEqual({
      inputTokens: 0,
      outputTokens: 0,
      cachedInputTokens: 0,
      reasoningOutputTokens: 0,
      mainTokens: 0,
      subagentTokens: 0,
      totalTokens: 0,
      processedTokens: null,
      agentCount: 0,
    });
  });

  it("decomposes main-thread usage by turn and keeps the latest snapshot per turn", () => {
    const model = derive({
      activities: [
        activity({
          id: "a-1",
          turnId: "turn-1",
          createdAt: "2026-01-01T09:00:00.000Z",
          payload: contextPayload({ inputTokens: 8_000, outputTokens: 500 }),
        }),
        activity({
          id: "a-2",
          turnId: "turn-1",
          createdAt: "2026-01-01T09:00:10.000Z",
          payload: contextPayload({ inputTokens: 9_000, outputTokens: 600 }),
        }),
        activity({
          id: "a-3",
          turnId: "turn-2",
          createdAt: "2026-01-01T09:10:00.000Z",
          payload: contextPayload({
            inputTokens: 15_000,
            outputTokens: 1_000,
            totalProcessedTokens: 25_000,
          }),
        }),
      ],
    });

    expect(model.turns).toHaveLength(2);
    expect(model.turns[0]).toMatchObject({
      turnId: "turn-1",
      ordinal: 1,
      inputTokens: 9_000,
      outputTokens: 600,
      totalTokens: 9_600,
      inputLabel: "9.0k",
      outputLabel: "600",
      totalLabel: "9.6k",
    });
    expect(model.turns[1]).toMatchObject({
      turnId: "turn-2",
      ordinal: 2,
      inputTokens: 15_000,
      outputTokens: 1_000,
      totalTokens: 16_000,
    });
    expect(model.totals).toMatchObject({
      inputTokens: 24_000,
      outputTokens: 1_600,
      mainTokens: 25_600,
      totalTokens: 25_600,
      processedTokens: 25_000,
    });
  });

  it("attributes each agent's tokens to the turn that first observed it", () => {
    const member = agent({
      id: "wf-1:task_1",
      parentAgentId: "wf-1",
      firstSeenAt: "2026-01-01T09:11:00.000Z",
      usage: { totalTokens: 500, inputTokens: 400, outputTokens: 100, toolUses: 3 },
    });
    const coordinator = agent({
      id: "wf-1",
      kind: "workflow",
      usage: { totalTokens: 9_999 },
    });
    const model = derive({
      activities: [
        activity({
          id: "a-1",
          turnId: "turn-1",
          createdAt: "2026-01-01T09:00:00.000Z",
          payload: contextPayload({ inputTokens: 1_000, outputTokens: 100 }),
        }),
        activity({
          id: "a-2",
          turnId: "turn-2",
          createdAt: "2026-01-01T09:10:00.000Z",
          payload: contextPayload({ inputTokens: 2_000, outputTokens: 200 }),
        }),
      ],
      model: {
        ...emptyAgentPanelModel(),
        hasAgents: true,
        totalTokens: 1_500,
        directAgents: [
          agent({
            id: "agent-early",
            firstSeenAt: "2026-01-01T09:05:00.000Z",
            usage: { totalTokens: 1_000, inputTokens: 800, outputTokens: 200 },
          }),
        ],
        workflows: [
          {
            workflow: coordinator,
            phases: [],
            unphasedMembers: [member],
          },
        ],
      },
    });

    expect(model.turns[0]).toMatchObject({ turnId: "turn-1", subagentTokens: 1_000 });
    expect(model.turns[1]).toMatchObject({ turnId: "turn-2", subagentTokens: 500 });
    expect(model.totals.subagentTokens).toBe(1_500);
    expect(model.totals.totalTokens).toBe(1_500 + 3_300);
    expect(model.agents.map((row) => row.id)).toEqual(["wf-1:task_1", "agent-early"]);
    expect(model.agents[0]).toMatchObject({
      inputLabel: "400",
      outputLabel: "100",
      totalLabel: "500",
      toolUses: 3,
    });
  });

  it("surfaces the orchestrator row for AgentTunnel workflow threads", () => {
    const member = agent({
      id: "wf-1:task_1",
      parentAgentId: "wf-1",
      workflowName: "AgentTunnel",
      firstSeenAt: "2026-01-01T09:11:00.000Z",
      usage: { totalTokens: 500, inputTokens: 400, outputTokens: 100, toolUses: 3 },
    });
    const coordinator = agent({
      id: "wf-1",
      kind: "workflow",
      model: "claude-sonnet-5",
      effort: "high",
      status: "running",
    });
    const model = derive({
      activities: [
        activity({
          id: "a-1",
          turnId: "turn-1",
          createdAt: "2026-01-01T09:00:00.000Z",
          payload: contextPayload({
            inputTokens: 43_000,
            outputTokens: 8_900,
            totalProcessedTokens: 70_100,
          }),
        }),
      ],
      model: {
        ...emptyAgentPanelModel(),
        hasAgents: true,
        totalTokens: 500,
        workflows: [{ workflow: coordinator, phases: [], unphasedMembers: [member] }],
      },
    });

    expect(model.orchestrator).toMatchObject({
      id: "wf-1",
      title: "Orchestrator",
      modelLabel: "sonnet-5 · high",
      status: "running",
      inputTokens: 43_000,
      outputTokens: 8_900,
      totalTokens: 51_900,
      inputLabel: "43.0k",
      outputLabel: "8.9k",
      totalLabel: "51.9k",
      processedTokens: 70_100,
    });
    expect(model.totals.agentCount).toBe(2);
    expect(model.totals.subagentTokens).toBe(500);
    expect(model.totals.mainTokens).toBe(51_900);
    expect(model.totals.totalTokens).toBe(model.totals.mainTokens + model.totals.subagentTokens);
    expect(model.agents.map((row) => row.id)).toEqual(["wf-1:task_1"]);
  });

  it("renders a member-less workflow coordinator once, as the orchestrator row", () => {
    const coordinator = agent({
      id: "wf-1",
      kind: "workflow",
      model: "claude-sonnet-5",
      effort: "high",
      status: "running",
    });
    const model = derive({
      activities: [
        activity({
          id: "a-1",
          turnId: "turn-1",
          payload: contextPayload({ inputTokens: 43_000, outputTokens: 8_900 }),
        }),
      ],
      model: {
        ...emptyAgentPanelModel(),
        hasAgents: true,
        totalTokens: 0,
        workflows: [{ workflow: coordinator, phases: [], unphasedMembers: [] }],
      },
    });

    // collectDisplayAgents falls back to the coordinator for a member-less
    // workflow; the pane must not also render it as a subagent roster row.
    expect(model.orchestrator).toMatchObject({ id: "wf-1" });
    expect(model.agents).toEqual([]);
    expect(model.totals.agentCount).toBe(1);
    expect(model.totals.subagentTokens).toBe(0);
  });

  it("keeps the hidden coordinator's usage out of the subagent totals", () => {
    const coordinator = agent({
      id: "wf-1",
      kind: "workflow",
      model: "claude-sonnet-5",
      effort: "high",
      status: "running",
      usage: { totalTokens: 9_999, inputTokens: 9_000, outputTokens: 999, toolUses: 5 },
    });
    const model = derive({
      activities: [
        activity({
          id: "a-1",
          turnId: "turn-1",
          payload: contextPayload({
            inputTokens: 43_000,
            outputTokens: 8_900,
            totalProcessedTokens: 70_100,
          }),
        }),
      ],
      model: {
        ...emptyAgentPanelModel(),
        hasAgents: true,
        totalTokens: 9_999,
        workflows: [{ workflow: coordinator, phases: [], unphasedMembers: [] }],
      },
    });

    // collectDisplayAgents falls back to the coordinator, but the orchestrator
    // row names it, so it must not render again or leak into the subagent sums.
    expect(model.orchestrator).toMatchObject({ id: "wf-1", totalTokens: 51_900 });
    expect(model.agents).toEqual([]);
    expect(model.totals.subagentTokens).toBe(0);
    expect(model.totals.mainTokens).toBe(51_900);
    expect(model.totals.totalTokens).toBe(model.totals.mainTokens + model.totals.subagentTokens);
    expect(model.totals.agentCount).toBe(1);
  });

  it("names an orchestrator for a workflowName-only thread with no workflow groups", () => {
    const member = agent({
      id: "wf-1:task_1",
      parentAgentId: "wf-1",
      workflowName: "AgentTunnel",
      usage: { totalTokens: 500 },
    });
    const model = derive({
      activities: [
        activity({
          id: "a-1",
          turnId: "turn-1",
          payload: contextPayload({ inputTokens: 1_000, outputTokens: 100 }),
        }),
      ],
      model: {
        ...emptyAgentPanelModel(),
        hasAgents: true,
        totalTokens: 500,
        workflows: [],
        directAgents: [member],
      },
    });

    expect(model.orchestrator).toMatchObject({
      id: "orchestrator",
      title: "Orchestrator",
      modelLabel: null,
      status: null,
      inputTokens: 1_000,
      outputTokens: 100,
      totalTokens: 1_100,
    });
    expect(model.agents.map((row) => row.id)).toEqual(["wf-1:task_1"]);
    expect(model.totals.agentCount).toBe(2);
  });

  it("leaves the orchestrator unnamed on non-workflow threads", () => {
    const model = derive({
      activities: [
        activity({
          id: "a-1",
          turnId: "turn-1",
          payload: contextPayload({ inputTokens: 1_000, outputTokens: 100 }),
        }),
      ],
      model: {
        ...emptyAgentPanelModel(),
        hasAgents: true,
        totalTokens: 0,
        directAgents: [agent({ id: "direct-1", usage: null })],
      },
    });

    expect(model.orchestrator).toBeNull();
    expect(model.totals.agentCount).toBe(1);
  });

  it("leaves the orchestrator unnamed when a workflow thread has no main usage", () => {
    const member = agent({
      id: "wf-1:task_1",
      parentAgentId: "wf-1",
      firstSeenAt: "2026-01-01T09:00:00.000Z",
      usage: { totalTokens: 500 },
    });
    const model = derive({
      model: {
        ...emptyAgentPanelModel(),
        hasAgents: true,
        totalTokens: 500,
        workflows: [
          {
            workflow: agent({ id: "wf-1", kind: "workflow" }),
            phases: [],
            unphasedMembers: [member],
          },
        ],
      },
    });

    expect(model.orchestrator).toBeNull();
    expect(model.totals.agentCount).toBe(1);
  });

  it("keeps a turnless snapshot in totals without inventing a turn", () => {
    const model = derive({
      activities: [
        activity({
          id: "a-1",
          turnId: null,
          payload: contextPayload({ inputTokens: 7_000, outputTokens: 300 }),
        }),
      ],
    });

    expect(model.turns).toEqual([]);
    expect(model.totals).toMatchObject({ inputTokens: 7_000, outputTokens: 300 });
    expect(model.hasUsage).toBe(true);
  });

  it("counts a processed-only snapshot instead of hiding the surface", () => {
    const model = derive({
      activities: [
        activity({
          id: "a-1",
          turnId: "turn-1",
          payload: { usedTokens: 1_000, totalProcessedTokens: 42_000 },
        }),
      ],
    });

    expect(model.hasUsage).toBe(true);
    expect(model.totals.processedTokens).toBe(42_000);
    expect(model.turns).toHaveLength(1);
    expect(model.turns[0]).toMatchObject({ turnId: "turn-1", totalTokens: 0 });
  });

  it("reports missing agent usage as an em dash instead of zero", () => {
    const model = derive({
      model: {
        ...emptyAgentPanelModel(),
        hasAgents: true,
        totalTokens: 0,
        directAgents: [agent({ id: "a-1", usage: null })],
      },
    });

    expect(model.hasUsage).toBe(false);
    expect(model.agents[0]).toMatchObject({ totalLabel: "—", inputLabel: "—", toolUses: null });
    expect(model.totals.subagentTokens).toBe(0);
  });

  it("carries the context usage summary for the header meter", () => {
    const model = derive({
      contextWindow: contextWindow({
        usedTokens: 40_000,
        maxTokens: 200_000,
        usedPercentage: 20,
      }),
    });

    expect(model.context).toMatchObject({
      usedLabel: "40k",
      limitLabel: "200k",
      percentLabel: "20%",
    });
  });
});

function usageEntry(input: {
  totalTokens: number;
  inputTokens?: number;
  cachedInputTokens?: number;
  outputTokens?: number;
  reasoningOutputTokens?: number;
}): OrchestrationAgentLogEntry {
  return { kind: "usage", ...input };
}

describe("deriveSubagentUsageStats", () => {
  it("decomposes each usage entry into a turn with its context size", () => {
    const stats = deriveSubagentUsageStats([
      usageEntry({
        totalTokens: 24_622,
        inputTokens: 10_871,
        cachedInputTokens: 13_056,
        outputTokens: 213,
        reasoningOutputTokens: 482,
      }),
      usageEntry({
        totalTokens: 5_000,
        inputTokens: 4_000,
        cachedInputTokens: 1_000,
        outputTokens: 100,
      }),
    ]);

    expect(stats.turnCount).toBe(2);
    expect(stats.turns[0]).toMatchObject({
      ordinal: 1,
      inputTokens: 10_871 + 13_056,
      cachedInputTokens: 13_056,
      outputTokens: 213 + 482,
      reasoningOutputTokens: 482,
      contextTokens: 10_871 + 13_056,
    });
    // Context size is the latest step's input, not the sum.
    expect(stats.contextTokens).toBe(5_000);
    expect(stats.totalTokens).toBe(29_622);
    expect(stats.totalLabel).toBe("29.6k");
  });

  it("ignores non-usage log entries and reports no usage", () => {
    const stats = deriveSubagentUsageStats([
      { kind: "text", text: "working" },
      { kind: "tool", tool: "bash", title: "echo hi", status: "completed" },
    ]);

    expect(stats.hasUsage).toBe(false);
    expect(stats.turns).toEqual([]);
    expect(stats.contextTokens).toBeNull();
    expect(stats.contextLabel).toBeNull();
  });

  it("defaults missing breakdown fields to zero", () => {
    const stats = deriveSubagentUsageStats([usageEntry({ totalTokens: 120 })]);

    expect(stats.turns[0]).toMatchObject({
      inputTokens: 0,
      cachedInputTokens: 0,
      outputTokens: 0,
      reasoningOutputTokens: 0,
      contextTokens: 0,
    });
    expect(stats.totalLabel).toBe("120");
  });
});
