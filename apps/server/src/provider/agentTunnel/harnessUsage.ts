/**
 * Harness token frames → contract usage shapes, shared by the AgentTunnel
 * adapter's orchestrator and agent streams.
 *
 * OpenCode reports per-step deltas, so callers accumulate totals per process
 * (agents) or per session (orchestrator) and hand both the latest step and the
 * cumulative totals here. The thread snapshot keeps the step's values in the
 * per-turn fields (latest snapshot wins per turn) and the cumulative total in
 * `totalProcessedTokens`, matching how the Claude and Codex adapters read.
 */
import type { RuntimeTaskUsage, ThreadTokenUsageSnapshot } from "@t3tools/contracts";

import type { OpenCodeStepUsage } from "./opencodeTransport.ts";

export interface HarnessUsageTotals {
  totalTokens: number;
  inputTokens: number;
  cachedInputTokens: number;
  outputTokens: number;
  reasoningOutputTokens: number;
}

export function emptyHarnessUsageTotals(): HarnessUsageTotals {
  return {
    totalTokens: 0,
    inputTokens: 0,
    cachedInputTokens: 0,
    outputTokens: 0,
    reasoningOutputTokens: 0,
  };
}

export function addHarnessUsageTotals(
  current: HarnessUsageTotals,
  step: OpenCodeStepUsage,
): HarnessUsageTotals {
  current.totalTokens += step.totalTokens;
  current.inputTokens += step.inputTokens;
  current.cachedInputTokens += step.cachedInputTokens;
  current.outputTokens += step.outputTokens;
  current.reasoningOutputTokens += step.reasoningOutputTokens;
  return current;
}

export function threadUsageSnapshotFromStep(
  step: OpenCodeStepUsage,
  cumulative: HarnessUsageTotals,
): ThreadTokenUsageSnapshot {
  // Match the Claude and Codex snapshots: inputTokens/outputTokens are the
  // totals, and the cached/reasoning fields are their subsets (OpenCode counts
  // both beside the uncached input and plain output). Reasoning must never be
  // added on top of output twice (UsageTokenTotals contract).
  const inputTokens = step.inputTokens + step.cachedInputTokens;
  const outputTokens = step.outputTokens + step.reasoningOutputTokens;
  return {
    usedTokens: step.totalTokens,
    totalProcessedTokens: cumulative.totalTokens,
    inputTokens,
    cachedInputTokens: step.cachedInputTokens,
    outputTokens,
    reasoningOutputTokens: step.reasoningOutputTokens,
    lastUsedTokens: step.totalTokens,
    lastInputTokens: inputTokens,
    lastCachedInputTokens: step.cachedInputTokens,
    lastOutputTokens: outputTokens,
    lastReasoningOutputTokens: step.reasoningOutputTokens,
  };
}

export function runtimeTaskUsageFromTotals(cumulative: HarnessUsageTotals): RuntimeTaskUsage {
  return {
    totalTokens: cumulative.totalTokens,
    inputTokens: cumulative.inputTokens,
    cachedInputTokens: cumulative.cachedInputTokens,
    outputTokens: cumulative.outputTokens + cumulative.reasoningOutputTokens,
    reasoningOutputTokens: cumulative.reasoningOutputTokens,
  };
}
