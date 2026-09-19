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
  addHarnessUsageTotals(totals, {
    ...step,
    totalTokens: 100,
    inputTokens: 80,
    cachedInputTokens: 0,
    outputTokens: 15,
    reasoningOutputTokens: 5,
  });

  NodeAssert.deepEqual(totals, {
    totalTokens: 24_722,
    inputTokens: 10_951,
    cachedInputTokens: 13_056,
    outputTokens: 228,
    reasoningOutputTokens: 487,
  });
});

it("threadUsageSnapshotFromStep keeps the step in last* fields and the run total in totalProcessedTokens", () => {
  const cumulative = { ...emptyHarnessUsageTotals(), totalTokens: 100_000, inputTokens: 50_000 };

  NodeAssert.deepEqual(threadUsageSnapshotFromStep(step, cumulative), {
    usedTokens: 24_622,
    totalProcessedTokens: 100_000,
    inputTokens: 10_871,
    cachedInputTokens: 13_056,
    outputTokens: 213,
    reasoningOutputTokens: 482,
    lastUsedTokens: 24_622,
    lastInputTokens: 10_871,
    lastCachedInputTokens: 13_056,
    lastOutputTokens: 213,
    lastReasoningOutputTokens: 482,
  });
});

it("runtimeTaskUsageFromTotals reports the cumulative agent rollup", () => {
  const cumulative = {
    totalTokens: 24_722,
    inputTokens: 10_951,
    cachedInputTokens: 13_056,
    outputTokens: 228,
    reasoningOutputTokens: 487,
  };

  NodeAssert.deepEqual(runtimeTaskUsageFromTotals(cumulative), cumulative);
});
