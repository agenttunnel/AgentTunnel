import * as NodeAssert from "node:assert/strict";
import { it } from "@effect/vitest";

import {
  addHarnessUsageTotals,
  emptyHarnessUsageTotals,
  runtimeTaskUsageFromTotals,
  threadUsageSnapshotFromStep,
} from "./harnessUsage.ts";

const step = {
  totalTokens: 24_622,
  inputTokens: 10_871,
  cachedInputTokens: 13_056,
  outputTokens: 213,
  reasoningOutputTokens: 482,
};

it("addHarnessUsageTotals accumulates step deltas field-wise", () => {
  const totals = emptyHarnessUsageTotals();
  addHarnessUsageTotals(totals, step);
  const second = {
    ...step,
    totalTokens: 100,
    inputTokens: 80,
    cachedInputTokens: 0,
    outputTokens: 15,
    reasoningOutputTokens: 5,
  };
  addHarnessUsageTotals(totals, second);

  NodeAssert.deepEqual(totals, {
    totalTokens: 24_722,
    inputTokens: 10_951,
    cachedInputTokens: 13_056,
    outputTokens: 228,
    reasoningOutputTokens: 487,
    costUsd: 0,
    lastTokenStep: second,
  });
});

it("addHarnessUsageTotals folds a repeated step once", () => {
  const totals = emptyHarnessUsageTotals();
  // Claude repeats a message's usage on each content-block event of the stream.
  const first = { ...step, stepId: "msg_1" };
  const repeat = { ...step, stepId: "msg_1" };
  const next = { ...step, totalTokens: 10, stepId: "msg_2" };

  NodeAssert.notEqual(addHarnessUsageTotals(totals, first), undefined);
  NodeAssert.equal(addHarnessUsageTotals(totals, repeat), undefined);
  NodeAssert.notEqual(addHarnessUsageTotals(totals, next), undefined);

  NodeAssert.equal(totals.totalTokens, step.totalTokens + 10);
});

it("threadUsageSnapshotFromStep repeats the last token-bearing step for a cost-only frame", () => {
  const totals = emptyHarnessUsageTotals();
  addHarnessUsageTotals(totals, step);
  // Claude prices the run on its closing frame, which carries no tokens.
  const costOnly = {
    totalTokens: 0,
    inputTokens: 0,
    cachedInputTokens: 0,
    outputTokens: 0,
    reasoningOutputTokens: 0,
    costUsd: 0.4,
  };
  addHarnessUsageTotals(totals, costOnly);

  const snapshot = threadUsageSnapshotFromStep(costOnly, totals);

  NodeAssert.equal(snapshot.usedTokens, step.totalTokens);
  NodeAssert.equal(snapshot.inputTokens, step.inputTokens + step.cachedInputTokens);
  NodeAssert.equal(snapshot.outputTokens, step.outputTokens + step.reasoningOutputTokens);
  NodeAssert.equal(snapshot.totalProcessedTokens, step.totalTokens);
  NodeAssert.equal(snapshot.totalCostUsd, 0.4);
});

it("addHarnessUsageTotals accumulates only the costs a harness reported", () => {
  const totals = emptyHarnessUsageTotals();
  addHarnessUsageTotals(totals, { ...step, costUsd: 0.25 });
  // Absent cost means the harness priced nothing for that step; it must not
  // reset or invent a figure.
  addHarnessUsageTotals(totals, { ...step, totalTokens: 10 });
  addHarnessUsageTotals(totals, { ...step, totalTokens: 10, costUsd: 0.75 });

  NodeAssert.equal(totals.costUsd, 1);
});

it("carries the session-cumulative cost on the thread snapshot", () => {
  const cumulative = {
    ...emptyHarnessUsageTotals(),
    totalTokens: 100_000,
    costUsd: 1.75,
  };

  const snapshot = threadUsageSnapshotFromStep({ ...step, costUsd: 0.25 }, cumulative);

  NodeAssert.equal(snapshot.totalCostUsd, 1.75);
  NodeAssert.equal(snapshot.usedTokens, 24_622);
});

it("omits cost entirely when nothing was priced", () => {
  const snapshot = threadUsageSnapshotFromStep(step, emptyHarnessUsageTotals());
  const usage = runtimeTaskUsageFromTotals({ ...emptyHarnessUsageTotals(), totalTokens: 5 });

  NodeAssert.equal("totalCostUsd" in snapshot, false);
  NodeAssert.equal("costUsd" in usage, false);
});

it("reports per-agent cumulative cost on the typed usage", () => {
  const cumulative = {
    ...emptyHarnessUsageTotals(),
    totalTokens: 24_722,
    inputTokens: 10_951,
    cachedInputTokens: 13_056,
    outputTokens: 228,
    reasoningOutputTokens: 487,
    costUsd: 0.42,
  };

  NodeAssert.deepEqual(runtimeTaskUsageFromTotals(cumulative), {
    totalTokens: 24_722,
    inputTokens: 10_951,
    cachedInputTokens: 13_056,
    outputTokens: 715,
    reasoningOutputTokens: 487,
    costUsd: 0.42,
  });
});

it("threadUsageSnapshotFromStep keeps the step in last* fields and the run total in totalProcessedTokens", () => {
  const cumulative = { ...emptyHarnessUsageTotals(), totalTokens: 100_000, inputTokens: 50_000 };

  NodeAssert.deepEqual(threadUsageSnapshotFromStep(step, cumulative), {
    usedTokens: 24_622,
    totalProcessedTokens: 100_000,
    inputTokens: 23_927,
    cachedInputTokens: 13_056,
    outputTokens: 695,
    reasoningOutputTokens: 482,
    lastUsedTokens: 24_622,
    lastInputTokens: 23_927,
    lastCachedInputTokens: 13_056,
    lastOutputTokens: 695,
    lastReasoningOutputTokens: 482,
  });
});

it("runtimeTaskUsageFromTotals folds reasoning into the reported output", () => {
  const cumulative = {
    totalTokens: 24_722,
    inputTokens: 10_951,
    cachedInputTokens: 13_056,
    outputTokens: 228,
    reasoningOutputTokens: 487,
    costUsd: 0,
  };

  NodeAssert.deepEqual(runtimeTaskUsageFromTotals(cumulative), {
    totalTokens: 24_722,
    inputTokens: 10_951,
    cachedInputTokens: 13_056,
    outputTokens: 715,
    reasoningOutputTokens: 487,
  });
});
