import { describe, expect, it } from "vite-plus/test";
import type { OrchestrationThreadActivity } from "@t3tools/contracts";
import { foldSubagentActivities } from "@t3tools/client-runtime/state/subagentRuntime";

import { agentRows, agentUsageLines, fleetTotals } from "./agents.ts";

let sequence = 0;

/** Ingestion stamps `agentKind` on every task.* row; the fixtures stamp too. */
function activity(
  kind: string,
  payload: Record<string, unknown>,
  at: string,
): OrchestrationThreadActivity {
  sequence += 1;
  return {
    id: `activity-${sequence}`,
    tone: "info",
    kind,
    summary: kind,
    payload: { agentKind: "agent", ...payload },
    turnId: null,
    createdAt: at,
  } as unknown as OrchestrationThreadActivity;
}

const fold = foldSubagentActivities;

const NOW = "2026-08-01T10:01:54.000Z";

describe("agentRows", () => {
  it("shows a running agent's live line and elapsed time, with no usage yet", () => {
    const agents = fold([
      activity(
        "task.started",
        {
          taskId: "task-run",
          title: "Audit auth flow",
          role: "explorer",
          model: "claude-sonnet-5",
          effort: "high",
          outputFile: "/logs/thread/task-run.log",
        },
        "2026-08-01T10:00:00.000Z",
      ),
      activity(
        "task.progress",
        { taskId: "task-run", summary: "Reading\n  the auth flow" },
        "2026-08-01T10:00:30.000Z",
      ),
    ]);

    expect(agentRows(agents, NOW)).toEqual([
      {
        id: "task-run",
        label: "explorer",
        status: "running",
        activity: "Reading the auth flow",
        model: "sonnet-5 · high",
        tokens: null,
        tools: null,
        elapsed: "1m 54s",
        attempt: null,
        harness: null,
        inputTokens: null,
        cachedTokens: null,
        outputTokens: null,
        costUsd: null,
        outputPath: "/logs/thread/task-run.log",
        parentId: null,
      },
    ]);
  });

  it("measures a live run only against the caller's clock", () => {
    const agents = fold([
      activity("task.started", { taskId: "task-clock", role: "explorer" }, NOW),
    ]);
    expect(agentRows(agents)[0]!.elapsed).toBeNull();
    expect(agentRows(agents, NOW)[0]!.elapsed).toBe("0s");
    expect(agentRows(agents, Date.parse(NOW) + 114_000)[0]!.elapsed).toBe("1m 54s");
  });

  it("reports a completed agent's tokens, tools, model, and second attempt", () => {
    const agents = fold([
      activity(
        "task.started",
        {
          taskId: "task-done",
          title: "Fix the flake",
          role: "fixer",
          model: "claude-opus-4-20250514",
          effort: "high",
          attempt: 1,
        },
        "2026-08-01T10:00:00.000Z",
      ),
      activity(
        "task.completed",
        {
          taskId: "task-done",
          status: "completed",
          summary: "Found 2 issues",
          attempt: 2,
          typedUsage: { totalTokens: 5400, toolUses: 12, durationMs: 114_000 },
        },
        "2026-08-01T10:01:54.000Z",
      ),
    ]);

    expect(agentRows(agents, NOW)[0]).toEqual({
      id: "task-done",
      label: "fixer",
      status: "completed",
      activity: "Found 2 issues",
      model: "opus-4 · high",
      tokens: "5.4k",
      tools: 12,
      elapsed: "1m 54s",
      attempt: 2,
      harness: null,
      inputTokens: null,
      cachedTokens: null,
      outputTokens: null,
      costUsd: null,
      outputPath: null,
      parentId: null,
    });
  });

  it("leads a failed agent's row with its error", () => {
    const agents = fold([
      activity(
        "task.started",
        { taskId: "task-bad", role: "verifier" },
        "2026-08-01T10:00:00.000Z",
      ),
      activity(
        "task.completed",
        {
          taskId: "task-bad",
          status: "failed",
          summary: "ENOENT: no such file",
          typedUsage: { totalTokens: 900 },
        },
        "2026-08-01T10:00:09.000Z",
      ),
    ]);

    const row = agentRows(agents, NOW)[0]!;
    expect(row.status).toBe("failed");
    expect(row.activity).toBe("ENOENT: no such file");
    expect(row.tokens).toBe("900");
  });

  it("orders live rows before settled ones, newest activity first", () => {
    const agents = fold([
      activity(
        "task.started",
        { taskId: "a-settled", role: "explorer" },
        "2026-08-01T10:00:00.000Z",
      ),
      activity(
        "task.completed",
        { taskId: "a-settled", status: "completed", summary: "done" },
        "2026-08-01T10:00:10.000Z",
      ),
      activity(
        "task.started",
        { taskId: "b-live-old", role: "reviewer" },
        "2026-08-01T10:00:05.000Z",
      ),
      activity(
        "task.progress",
        { taskId: "b-live-old", summary: "checking" },
        "2026-08-01T10:00:20.000Z",
      ),
      activity("task.started", { taskId: "c-live-new", role: "fixer" }, "2026-08-01T10:00:06.000Z"),
      activity(
        "task.progress",
        { taskId: "c-live-new", summary: "patching" },
        "2026-08-01T10:00:30.000Z",
      ),
    ]);

    expect(agentRows(agents, NOW).map((row) => row.id)).toEqual([
      "c-live-new",
      "b-live-old",
      "a-settled",
    ]);
  });

  it("renders a workflow's members, and its coordinator only while it has none", () => {
    const coordinator = activity(
      "task.started",
      { taskId: "wf-1", taskType: "local_workflow", title: "Ship the fix", role: null },
      "2026-08-01T10:00:00.000Z",
    );
    const member = activity(
      "task.started",
      {
        taskId: "wf-1-member",
        title: "Member one",
        role: "explorer",
        parentAgentId: "wf-1",
      },
      "2026-08-01T10:00:01.000Z",
    );

    expect(agentRows(fold([coordinator, member])).map((row) => row.id)).toEqual(["wf-1-member"]);
    expect(agentRows(fold([coordinator])).map((row) => row.id)).toEqual(["wf-1"]);
  });

  it("has nothing to show for a thread without agents", () => {
    expect(agentRows([])).toEqual([]);
    expect(agentUsageLines([])).toEqual([]);
  });
});

describe("agentUsageLines", () => {
  it("lists what each agent spent, then the fleet total", () => {
    const agents = fold([
      activity(
        "task.started",
        { taskId: "u-explorer", role: "explorer", title: "Audit" },
        "2026-08-01T10:00:00.000Z",
      ),
      activity(
        "task.completed",
        {
          taskId: "u-explorer",
          status: "completed",
          summary: "done",
          typedUsage: { totalTokens: 1200, toolUses: 3 },
        },
        "2026-08-01T10:00:10.000Z",
      ),
      activity(
        "task.started",
        { taskId: "u-verifier", role: "verifier", title: "Verify" },
        "2026-08-01T10:00:01.000Z",
      ),
      activity(
        "task.completed",
        {
          taskId: "u-verifier",
          status: "completed",
          summary: "verified",
          typedUsage: { totalTokens: 3400 },
        },
        "2026-08-01T10:00:12.000Z",
      ),
    ]);

    // Each agent states who ran it and what it spent, split into input, output,
    // cache hits and cost; the fleet total closes the report.
    expect(agentUsageLines(agents)).toEqual([
      "explorer · completed",
      "  in 1.2k · total 1.2k · 3 tools",
      "verifier · completed",
      "  in 3.4k · total 3.4k",
      "",
      "Σ 4.6k tok",
    ]);
  });
});

describe("fleetTotals", () => {
  it("adds up what the fleet spent and how it is doing", () => {
    const agents = [
      { status: "running", usage: { totalTokens: 1_000 } },
      { status: "completed", usage: { totalTokens: 2_500 } },
      { status: "failed", usage: { totalTokens: 400 } },
    ] as unknown as Parameters<typeof fleetTotals>[0];
    expect(fleetTotals(agents)).toEqual({ tokens: 3_900, running: 1, settled: 1, failed: 1 });
  });

  it("reports nothing for an empty fleet", () => {
    expect(fleetTotals([])).toEqual({ tokens: 0, running: 0, settled: 0, failed: 0 });
  });
});
