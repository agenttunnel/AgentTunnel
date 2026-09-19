import type {
  AgentPanelModel,
  RuntimeSubagent,
} from "@t3tools/client-runtime/state/subagentRuntime";
import { emptyAgentPanelModel } from "@t3tools/client-runtime/state/subagentRuntime";
import { describe, expect, it } from "vite-plus/test";

import type { ContextWindowSnapshot } from "../lib/contextWindow";
import type { AgentTunnelRoleOption } from "./agentTunnelRoles.logic";
import type { ContextAnalysis } from "./ContextPanel.logic";
import { deriveContextAnalysis } from "./ContextPanel.logic";

function agent(overrides: Partial<RuntimeSubagent> & Pick<RuntimeSubagent, "id">): RuntimeSubagent {
  return {
    kind: "subagent",
    title: overrides.id,
    role: null,
    model: null,
    effort: null,
    status: "running",
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

const thread = {
  title: "Ship the context surface",
  createdAt: "2026-01-01T09:00:00.000Z",
  updatedAt: "2026-01-01T10:30:00.000Z",
  messages: [{ role: "user" }, { role: "assistant" }, { role: "user" }, { role: "system" }],
};

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
    updatedAt: "2026-01-01T10:00:00.000Z",
    ...overrides,
  };
}

function stat(analysis: ContextAnalysis, label: string): string {
  const found = analysis.stats.find((entry) => entry.label === label);
  expect(found, `missing stat: ${label}`).toBeDefined();
  return found!.value;
}

function derive(input: {
  model?: AgentPanelModel;
  contextWindow?: ContextWindowSnapshot | null;
  roles?: ReadonlyArray<AgentTunnelRoleOption>;
  modelLabel?: string | null;
}): ContextAnalysis {
  return deriveContextAnalysis({
    model: input.model ?? emptyAgentPanelModel(),
    contextWindow: input.contextWindow ?? null,
    thread,
    providerLabel: "AgentTunnel",
    modelLabel: input.modelLabel ?? "provider/model",
    roles: input.roles ?? [],
    timestampFormat: "24-hour",
  });
}

describe("deriveContextAnalysis", () => {
  it("counts user and assistant messages and ignores system rows", () => {
    const analysis = derive({});
    expect(stat(analysis, "User messages")).toBe("2");
    expect(stat(analysis, "Assistant messages")).toBe("1");
    expect(stat(analysis, "Session")).toBe("Ship the context surface");
    expect(stat(analysis, "Provider")).toBe("AgentTunnel");
    expect(stat(analysis, "Model")).toBe("provider/model");
  });

  it("derives context usage and the token breakdown from the snapshot", () => {
    const analysis = derive({
      contextWindow: contextWindow({
        usedTokens: 40_000,
        maxTokens: 200_000,
        usedPercentage: 20,
        inputTokens: 12_000,
        outputTokens: 3_000,
        cachedInputTokens: 5_000,
      }),
    });

    expect(analysis.usage).toEqual({
      usedTokens: 40_000,
      maxTokens: 200_000,
      usedPercentage: 20,
      usedLabel: "40k",
      limitLabel: "200k",
      percentLabel: "20%",
    });
    expect(stat(analysis, "Context limit")).toBe("200k");
    expect(stat(analysis, "Context used")).toBe("40k · 20%");
    expect(stat(analysis, "Input tokens")).toBe("12k");
    expect(stat(analysis, "Output tokens")).toBe("3k");
    expect(stat(analysis, "Cache tokens")).toBe("5k");
  });

  it("omits the breakdown and the percentage when the snapshot lacks them", () => {
    const analysis = derive({ contextWindow: contextWindow({ usedTokens: 500 }) });

    expect(analysis.usage?.maxTokens).toBeNull();
    expect(analysis.usage?.percentLabel).toBeNull();
    expect(stat(analysis, "Context limit")).toBe("Unknown");
    expect(stat(analysis, "Context used")).toBe("500");
    expect(analysis.stats.some((entry) => entry.label === "Input tokens")).toBe(false);
    expect(analysis.stats.some((entry) => entry.label === "Cache tokens")).toBe(false);
  });

  it("reads the agent token total from the panel model", () => {
    const analysis = derive({
      model: { ...emptyAgentPanelModel(), hasAgents: true, totalTokens: 12_345 },
    });
    expect(stat(analysis, "Total tokens")).toBe("12.3k");
  });

  it("marks missing agent usage instead of reporting zero", () => {
    const analysis = derive({
      model: {
        ...emptyAgentPanelModel(),
        hasAgents: true,
        totalTokens: 1_500,
        directAgents: [
          agent({ id: "a-1", usage: { totalTokens: 1_500 } }),
          agent({ id: "a-2", usage: null }),
        ],
      },
    });

    expect(analysis.agents.map((row) => row.tokens)).toEqual(["1.5k", "—"]);
    expect(analysis.agents.map((row) => row.id)).toEqual(["a-1", "a-2"]);
  });

  it("turns an empty model into an empty table with zero totals", () => {
    const analysis = derive({});

    expect(analysis.hasAgents).toBe(false);
    expect(analysis.agents).toEqual([]);
    expect(stat(analysis, "Total tokens")).toBe("0");
    expect(analysis.usage).toBeNull();
    expect(stat(analysis, "Context used")).toBe("Unknown");
  });

  it("resolves role models with a workflow-default fallback", () => {
    const analysis = derive({
      roles: [
        {
          id: "orchestrator",
          label: "Orchestrator",
          model: "anthropic/claude-opus",
          harness: "opencode",
          variant: null,
          auto: false,
        },
        {
          id: "executor",
          label: "Executor",
          model: null,
          harness: "opencode",
          variant: null,
          auto: false,
        },
        {
          id: "reviewer",
          label: "Reviewer",
          model: "openai/gpt-5",
          harness: "claude",
          variant: "high",
          auto: false,
        },
      ],
    });

    expect(analysis.roleModels).toEqual([
      {
        id: "orchestrator",
        label: "Orchestrator",
        model: "anthropic/claude-opus",
        auto: false,
        value: "anthropic/claude-opus",
      },
      { id: "executor", label: "Executor", model: null, auto: false, value: "Workflow default" },
      {
        id: "reviewer",
        label: "Reviewer",
        model: "openai/gpt-5",
        auto: false,
        value: "openai/gpt-5",
      },
    ]);
  });

  it("labels auto roles as orchestrator-chosen while keeping their fallback model", () => {
    const analysis = derive({
      roles: [
        {
          id: "orchestrator",
          label: "Orchestrator",
          model: null,
          harness: "opencode",
          variant: null,
          auto: false,
        },
        {
          id: "executor",
          label: "Executor",
          model: "anthropic/claude-sonnet",
          harness: "opencode",
          variant: null,
          auto: true,
        },
        {
          id: "reviewer",
          label: "Reviewer",
          model: null,
          harness: "opencode",
          variant: null,
          auto: true,
        },
      ],
    });

    expect(analysis.roleModels).toEqual([
      {
        id: "orchestrator",
        label: "Orchestrator",
        model: null,
        auto: false,
        value: "Workflow default",
      },
      {
        id: "executor",
        label: "Executor",
        model: "anthropic/claude-sonnet",
        auto: true,
        value: "Auto - chosen by orchestrator",
      },
      {
        id: "reviewer",
        label: "Reviewer",
        model: null,
        auto: true,
        value: "Auto - chosen by orchestrator",
      },
    ]);
  });

  it("lists workflow members instead of their container and derives row fields", () => {
    const coordinator = agent({ id: "wf-1", kind: "workflow", title: "Sum workflow" });
    const member = agent({
      id: "wf-1:task_1",
      parentAgentId: "wf-1",
      role: "executor",
      title: "Implement the table",
      phaseTitle: "implementation",
      startedAt: "2026-01-01T09:00:00.000Z",
      completedAt: "2026-01-01T09:01:05.000Z",
      recentActivity: [
        { at: "2026-01-01T09:00:10.000Z", summary: "read files" },
        { at: "2026-01-01T09:00:20.000Z", summary: "wrote tests" },
      ],
    });
    const model: AgentPanelModel = {
      ...emptyAgentPanelModel(),
      hasAgents: true,
      totalTokens: 2_000,
      workflows: [
        {
          workflow: coordinator,
          phases: [
            {
              index: 0,
              title: "implementation",
              members: [member],
              state: "done",
              activeCount: 0,
              settledCount: 1,
            },
          ],
          unphasedMembers: [],
        },
      ],
    };

    const analysis = derive({ model });
    expect(analysis.agents).toHaveLength(1);
    expect(analysis.agents[0]).toMatchObject({
      id: "wf-1:task_1",
      role: "executor",
      title: "Implement the table",
      pipeline: "implementation",
      status: "running",
      statusLabel: "Working",
      duration: "1m 05s",
      outputLines: 2,
    });
  });
});
