import { describe, expect, it } from "vite-plus/test";

import { emptyAgentPanelModel } from "./subagentRuntime.ts";
import type { AgentPanelModel, RuntimeSubagent } from "./subagentRuntime.ts";
import {
  deriveThreadRecap,
  formatThreadRecap,
  latestLiveActivity,
  parseReportNextSteps,
} from "./threadRecap.ts";

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

function model(overrides: Partial<AgentPanelModel>): AgentPanelModel {
  return {
    ...emptyAgentPanelModel(),
    hasAgents: true,
    workflows: [],
    directAgents: [],
    ...overrides,
  };
}

describe("latestLiveActivity", () => {
  it("reports the newest live agent's progress, labelled with its role", () => {
    const activity = latestLiveActivity([
      agent({
        id: "a-old",
        role: "executor",
        progress: "reading the parser",
        updatedAt: "2026-01-01T00:00:01.000Z",
      }),
      agent({
        id: "a-new",
        role: "tester",
        progress: "crunching the numbers",
        updatedAt: "2026-01-01T00:00:09.000Z",
      }),
      agent({ id: "a-done", status: "completed", role: "executor", progress: "finished" }),
    ]);
    expect(activity).toBe("tester: crunching the numbers");
  });

  it("falls back to the tool name, then to starting", () => {
    expect(latestLiveActivity([agent({ id: "a", role: "executor", lastToolName: "bash" })])).toBe(
      "executor: ▸ bash",
    );
    expect(latestLiveActivity([agent({ id: "a", role: "planner" })])).toBe("planner: starting");
    expect(latestLiveActivity([])).toBeNull();
    expect(latestLiveActivity([agent({ id: "a", status: "completed" })])).toBeNull();
  });
});

describe("parseReportNextSteps", () => {
  it("reads the bullets under the report's Next steps heading", () => {
    const report = [
      "### What happened",
      "",
      "Shipped the recap row.",
      "",
      "### Next steps",
      "",
      "- Attach the screenshots",
      "- Merge the PR",
      "* Watch the release",
      "- A fourth step is dropped",
      "",
      "### Something else",
      "",
      "- not a step",
    ].join("\n");
    expect(parseReportNextSteps(report)).toEqual([
      "Attach the screenshots",
      "Merge the PR",
      "Watch the release",
    ]);
  });

  it("returns nothing when the message is not a report", () => {
    expect(parseReportNextSteps("just an answer")).toEqual([]);
    expect(parseReportNextSteps("## Next steps")).toEqual([]);
  });
});

describe("deriveThreadRecap", () => {
  it("says nothing for a thread with no goal, no agents, and no report", () => {
    expect(
      deriveThreadRecap({
        goal: null,
        model: emptyAgentPanelModel(),
        latestAssistantText: null,
      }),
    ).toBeNull();
  });

  it("reports the goal, the live fleet, and the next steps", () => {
    const recap = deriveThreadRecap({
      goal: "Add the sticky recap",
      model: model({
        liveCount: 2,
        settledCount: 3,
        directAgents: [
          agent({ id: "a", role: "executor", progress: "wiring the row" }),
          agent({ id: "b", status: "completed", role: "reviewer" }),
        ],
      }),
      latestAssistantText: "### Next steps\n\n- Run the app once\n",
    });
    expect(recap).toEqual({
      goal: "Add the sticky recap",
      working: 2,
      settled: 3,
      latestActivity: "executor: wiring the row",
      nextSteps: ["Run the app once"],
    });
  });

  it("clips a long goal to one line", () => {
    const recap = deriveThreadRecap({
      goal: `long\n\n${"x".repeat(400)}`,
      model: model({ liveCount: 1 }),
      latestAssistantText: null,
    });
    expect(recap?.goal?.length).toBe(140);
    expect(recap?.goal?.includes("\n")).toBe(false);
  });
});

describe("formatThreadRecap", () => {
  it("reads goal, work, then next steps", () => {
    expect(
      formatThreadRecap({
        goal: "Ship the recap",
        working: 1,
        settled: 2,
        latestActivity: "executor: wiring the row",
        nextSteps: ["Run the app once", "Merge"],
      }),
    ).toBe(
      "Goal: Ship the recap  ·  1 agent working · executor: wiring the row  ·  Next: Run the app once · Merge",
    );
  });

  it("falls back to the settled count once nothing is running", () => {
    expect(
      formatThreadRecap({
        goal: null,
        working: 0,
        settled: 3,
        latestActivity: null,
        nextSteps: [],
      }),
    ).toBe("3 settled");
  });
});
