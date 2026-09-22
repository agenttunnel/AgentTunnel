import { describe, expect, it } from "vite-plus/test";
import type { OrchestrationThreadDetailSnapshot } from "@t3tools/contracts";

import { threadRows, threadView } from "./format.ts";

const shell = {
  snapshotSequence: 1,
  updatedAt: "2026-01-01T00:00:00.000Z",
  projects: [
    {
      id: "p1",
      title: "AgentTunnel",
      workspaceRoot: "/code/agenttunnel",
      createdAt: "2026-01-01T00:00:00.000Z",
      updatedAt: "2026-01-01T00:00:00.000Z",
    },
  ],
  threads: [
    {
      id: "t-old",
      projectId: "p1",
      title: "Older thread",
      branch: null,
      updatedAt: "2026-01-01T00:00:01.000Z",
    },
    {
      id: "t-new",
      projectId: "p1",
      title: "Newer thread",
      branch: "main",
      updatedAt: "2026-01-01T00:00:09.000Z",
    },
  ],
} as unknown as Parameters<typeof threadRows>[0];

const detail = {
  snapshotSequence: 1,
  thread: {
    id: "t-new",
    title: "Newer thread",
    session: { status: "running" },
    messages: [
      { role: "user", text: "Ship the terminal client." },
      { role: "assistant", text: "Splitting into recon and build." },
    ],
    activities: [
      {
        kind: "task.progress",
        createdAt: "2026-01-01T00:00:05.000Z",
        payload: {
          taskId: "agent-1",
          taskType: "local_agent",
          agentKind: "agent",
          role: "executor",
          title: "Wire the prompt",
          status: "running",
          summary: "wiring the row",
        },
      },
    ],
  },
} as unknown as OrchestrationThreadDetailSnapshot;

describe("threadRows", () => {
  it("lists threads newest first with their project title", () => {
    expect(threadRows(shell).map((row) => `${row.id}:${row.projectTitle}`)).toEqual([
      "t-new:AgentTunnel",
      "t-old:AgentTunnel",
    ]);
  });
});

describe("threadView", () => {
  it("reads the transcript and the fleet out of the nested thread snapshot", () => {
    const view = threadView(detail);
    expect(view.title).toBe("Newer thread");
    expect(view.lines.map((line) => `${line.kind}:${line.text}`)).toEqual([
      "user:Ship the terminal client.",
      "assistant:Splitting into recon and build.",
      "agent:executor · running — wiring the row",
    ]);
    // The recap comes from the same derivation the web client uses.
    expect(view.recap).toContain("Goal: Ship the terminal client.");
    expect(view.recap).toContain("1 agent working");
  });

  it("says nothing about agents when activities are excluded", () => {
    const view = threadView(detail, { includeActivities: false });
    expect(view.lines.map((line) => line.kind)).toEqual(["user", "assistant"]);
  });
});
