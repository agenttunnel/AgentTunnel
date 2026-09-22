import type {
  AgentPanelModel,
  RuntimeSubagent,
} from "@t3tools/client-runtime/state/subagentRuntime";
import { describe, expect, it } from "vite-plus/test";

import {
  agentAttemptLabel,
  agentRunLabel,
  agentControlAction,
  findAgentById,
  liveAgents,
  phaseStatusLabel,
  pruneQueuedRetries,
  subagentRunKey,
  tallySubagents,
  workflowMembers,
  workflowTallyLabel,
} from "./AgentsPanel.logic";

function agent(overrides: Partial<RuntimeSubagent> & Pick<RuntimeSubagent, "id">): RuntimeSubagent {
  return {
    kind: "subagent",
    title: overrides.id,
    role: null,
    model: null,
    harness: null,
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
    firstSeenAt: "2026-01-01T00:00:00.000Z",
    startedAt: "2026-01-01T00:00:00.000Z",
    completedAt: null,
    updatedAt: "2026-01-01T00:00:00.000Z",
    ...overrides,
  };
}

const coordinator = agent({ id: "wf-1", kind: "workflow", title: "Sum workflow" });
const phaseMember = agent({
  id: "wf-1:task_1",
  parentAgentId: "wf-1",
  phaseIndex: 0,
  phaseTitle: "implementation",
});
const unphasedMember = agent({ id: "wf-1:task_2", parentAgentId: "wf-1", phaseIndex: 9 });
const direct = agent({ id: "direct-1" });

const model: AgentPanelModel = {
  workflows: [
    {
      workflow: coordinator,
      phases: [
        {
          index: 0,
          title: "implementation",
          members: [phaseMember],
          state: "running",
          activeCount: 1,
          settledCount: 0,
        },
      ],
      unphasedMembers: [unphasedMember],
    },
  ],
  directAgents: [direct],
  runningCount: 3,
  waitingCount: 0,
  idleCount: 0,
  settledCount: 1,
  totalTokens: 0,
  hasAgents: true,
  liveCount: 3,
};

describe("findAgentById", () => {
  it("resolves workflow coordinators, phase members, unphased members, and direct agents", () => {
    expect(findAgentById(model, "wf-1")?.title).toBe("Sum workflow");
    expect(findAgentById(model, "wf-1:task_1")?.phaseTitle).toBe("implementation");
    expect(findAgentById(model, "wf-1:task_2")?.phaseIndex).toBe(9);
    expect(findAgentById(model, "direct-1")?.id).toBe("direct-1");
  });

  it("returns null when the roster no longer holds the id", () => {
    expect(findAgentById(model, "missing")).toBeNull();
  });
});

describe("liveAgents", () => {
  it("keeps only active rows, most recently updated first", () => {
    const live = liveAgents({
      ...model,
      directAgents: [
        agent({ id: "direct-1", status: "completed" }),
        agent({ id: "direct-2", status: "waiting", updatedAt: "2026-01-01T00:00:05.000Z" }),
      ],
    });
    expect(live.map((entry) => entry.id)).toEqual(["direct-2", "wf-1:task_1", "wf-1:task_2"]);
  });

  it("returns nothing when the fleet is settled", () => {
    expect(
      liveAgents({
        ...model,
        workflows: [],
        directAgents: [agent({ id: "done-1", status: "completed" })],
      }),
    ).toEqual([]);
  });
});

describe("workflowMembers", () => {
  it("flattens phased and unphased members in order", () => {
    expect(workflowMembers(model.workflows[0]!).map((entry) => entry.id)).toEqual([
      "wf-1:task_1",
      "wf-1:task_2",
    ]);
  });
});

describe("agentControlAction", () => {
  it("offers stop while the row can still be interrupted", () => {
    for (const status of ["running", "pending", "waiting"] as const) {
      expect(agentControlAction(agent({ id: `a-${status}`, status }))).toBe("stop");
    }
  });

  it("offers retry once the row settled as failed or stopped", () => {
    expect(agentControlAction(agent({ id: "a-failed", status: "failed" }))).toBe("retry");
    // `interrupted` is what the client fold makes of a provider "stopped" row,
    // and what a user-stopped agent renders as ("Stopped").
    expect(agentControlAction(agent({ id: "a-stopped", status: "interrupted" }))).toBe("retry");
  });

  it("offers nothing on rows with no control", () => {
    for (const status of ["completed", "idle", "cancelled"] as const) {
      expect(agentControlAction(agent({ id: `a-${status}`, status }))).toBeNull();
    }
  });
});

describe("agentAttemptLabel", () => {
  it("names only attempts a user would call a retry", () => {
    expect(agentAttemptLabel(agent({ id: "a-unknown", attempt: null }))).toBeNull();
    expect(agentAttemptLabel(agent({ id: "a-first", attempt: 1 }))).toBeNull();
    expect(agentAttemptLabel(agent({ id: "a-second", attempt: 2 }))).toBe("attempt 2");
    expect(agentAttemptLabel(agent({ id: "a-fourth", attempt: 4 }))).toBe("attempt 4");
  });

  it("labels a retried agent that is running again", () => {
    const retried = agent({ id: "wf-1:task_1", status: "running", attempt: 2 });
    expect(agentControlAction(retried)).toBe("stop");
    expect(agentAttemptLabel(retried)).toBe("attempt 2");
  });
});

describe("agentRunLabel", () => {
  it("stays quiet until the row has run more than once", () => {
    expect(agentRunLabel(agent({ id: "a-first", activationCount: 1 }))).toBeNull();
    expect(agentRunLabel(agent({ id: "a-resumed", activationCount: 2 }))).toBe("run 2");
  });

  it("leaves the numbering to the attempt chip when the provider marks attempts", () => {
    expect(agentRunLabel(agent({ id: "a-retried", activationCount: 2, attempt: 2 }))).toBeNull();
  });
});

describe("tallySubagents", () => {
  it("counts settled rows and the failures among them", () => {
    expect(
      tallySubagents([
        agent({ id: "a-completed", status: "completed" }),
        agent({ id: "a-failed", status: "failed" }),
        agent({ id: "a-stopped", status: "interrupted" }),
        agent({ id: "a-running", status: "running" }),
        agent({ id: "a-idle", status: "idle" }),
      ]),
    ).toEqual({ total: 5, settled: 3, failed: 1 });
  });
});

describe("workflowTallyLabel", () => {
  it("leads with failures", () => {
    expect(workflowTallyLabel({ total: 7, settled: 7, failed: 0 })).toBe("7/7 settled");
    expect(workflowTallyLabel({ total: 7, settled: 7, failed: 2 })).toBe("2 failed · 7/7 settled");
    expect(workflowTallyLabel({ total: 3, settled: 1, failed: 1 })).toBe("1 failed · 1/3 settled");
  });
});

describe("phaseStatusLabel", () => {
  it("reads a clean phase by its state", () => {
    expect(phaseStatusLabel({ state: "pending", members: [], activeCount: 0 })).toBe("pending");
    expect(
      phaseStatusLabel({
        state: "done",
        members: [agent({ id: "p-1", status: "completed" })],
        activeCount: 0,
      }),
    ).toBe("1 done");
    expect(
      phaseStatusLabel({
        state: "running",
        members: [
          agent({ id: "p-1", status: "completed" }),
          agent({ id: "p-2", status: "running" }),
        ],
        activeCount: 1,
      }),
    ).toBe("1 active · 1 done");
  });

  it("never calls a phase with failures done", () => {
    expect(
      phaseStatusLabel({
        state: "done",
        members: [agent({ id: "p-1", status: "failed" }), agent({ id: "p-2", status: "failed" })],
        activeCount: 0,
      }),
    ).toBe("2 failed");
    expect(
      phaseStatusLabel({
        state: "done",
        members: [
          agent({ id: "p-1", status: "failed" }),
          agent({ id: "p-2", status: "completed" }),
        ],
        activeCount: 0,
      }),
    ).toBe("1 failed · 1 done");
    expect(
      phaseStatusLabel({
        state: "running",
        members: [
          agent({ id: "p-1", status: "failed" }),
          agent({ id: "p-2", status: "completed" }),
          agent({ id: "p-3", status: "running" }),
        ],
        activeCount: 1,
      }),
    ).toBe("1 failed · 1 active · 1 done");
  });
});

describe("pruneQueuedRetries", () => {
  const roster = (agents: ReadonlyArray<RuntimeSubagent>) =>
    new Map(agents.map((entry) => [entry.id, entry]));
  const failed = agent({ id: "wf-1:task_1", status: "failed", attempt: 1 });

  it("keeps a queued retry until its slot moves", () => {
    const queued = new Map([[failed.id, subagentRunKey(failed)]]);
    // Progress rows on the dead run must not clear the acknowledgement.
    const stillFailed = agent({
      id: failed.id,
      status: "failed",
      attempt: 1,
      updatedAt: "2026-01-01T00:00:30.000Z",
      error: "still the old failure",
    });
    expect(pruneQueuedRetries(queued, roster([stillFailed]))).toBe(queued);
  });

  it("clears the acknowledgement once the retry is running", () => {
    const queued = new Map([[failed.id, subagentRunKey(failed)]]);
    const reopened = agent({ id: failed.id, status: "running", attempt: 2 });
    expect(pruneQueuedRetries(queued, roster([reopened])).size).toBe(0);
  });

  it("clears it when the slot came back on a new attempt", () => {
    const queued = new Map([[failed.id, subagentRunKey(failed)]]);
    const retried = agent({ id: failed.id, status: "running", attempt: 2, startedAt: null });
    expect(pruneQueuedRetries(queued, roster([retried])).size).toBe(0);
  });

  it("clears it when the row left the roster", () => {
    const queued = new Map([[failed.id, subagentRunKey(failed)]]);
    expect(pruneQueuedRetries(queued, roster([])).size).toBe(0);
  });

  it("returns the same map when there is nothing to do", () => {
    const empty: ReadonlyMap<string, string> = new Map();
    expect(pruneQueuedRetries(empty, roster([failed]))).toBe(empty);
  });
});
