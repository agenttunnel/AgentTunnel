/**
 * Harness token frames → contract usage shapes, shared by the AgentTunnel
 * adapter's orchestrator and agent streams.
 *
 * Both supported harnesses report per-step deltas — OpenCode on `step_finish`,
 * pi on `message_end` — so callers accumulate totals per process (agents) or
 * per session (orchestrator) and hand both the latest step and the cumulative
 * totals here. The thread snapshot keeps the step's values in the per-turn
 * fields (latest snapshot wins per turn) and the cumulative total in
 * `totalProcessedTokens`, matching how the Claude and Codex adapters read.
 *
 * Costs ride along when the harness computed one. A harness with no rate for
 * the model reports zero; that is "unpriced", not free, so zero is dropped and
 * the surfaces show nothing rather than a made-up figure.
 */
import type { RuntimeTaskUsage, ThreadTokenUsageSnapshot } from "@t3tools/contracts";

/**
 * One harness-reported step, normalized across transports: `inputTokens` is
 * the *uncached* input, `outputTokens` the plain output with reasoning kept
 * beside it, and `totalTokens` the step's own sum.
 */
export interface HarnessStepUsage {
  readonly totalTokens: number;
  readonly inputTokens: number;
  readonly cachedInputTokens: number;
  readonly outputTokens: number;
  readonly reasoningOutputTokens: number;
  /** Cost the harness computed for this step, when it reported one above zero. */
  readonly costUsd?: number;
}

export interface HarnessUsageTotals {
  totalTokens: number;
  inputTokens: number;
  cachedInputTokens: number;
  outputTokens: number;
  reasoningOutputTokens: number;
  costUsd: number;
}

export function emptyHarnessUsageTotals(): HarnessUsageTotals {
  return {
    totalTokens: 0,
    inputTokens: 0,
    cachedInputTokens: 0,
    outputTokens: 0,
    reasoningOutputTokens: 0,
    costUsd: 0,
  };
}

export function addHarnessUsageTotals(
  current: HarnessUsageTotals,
  step: HarnessStepUsage,
): HarnessUsageTotals {
  current.totalTokens += step.totalTokens;
  current.inputTokens += step.inputTokens;
  current.cachedInputTokens += step.cachedInputTokens;
  current.outputTokens += step.outputTokens;
  current.reasoningOutputTokens += step.reasoningOutputTokens;
  if (step.costUsd !== undefined) current.costUsd += step.costUsd;
  return current;
}

export function threadUsageSnapshotFromStep(
  step: HarnessStepUsage,
  cumulative: HarnessUsageTotals,
): ThreadTokenUsageSnapshot {
  // Match the Claude and Codex snapshots: inputTokens/outputTokens are the
  // totals, and the cached/reasoning fields are their subsets (both harnesses
  // report them beside the uncached input and plain output). Reasoning must
  // never be added on top of output twice (UsageTokenTotals contract).
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
    ...(cumulative.costUsd > 0 ? { totalCostUsd: cumulative.costUsd } : {}),
  };
}

export function runtimeTaskUsageFromTotals(cumulative: HarnessUsageTotals): RuntimeTaskUsage {
  return {
    totalTokens: cumulative.totalTokens,
    inputTokens: cumulative.inputTokens,
    cachedInputTokens: cumulative.cachedInputTokens,
    outputTokens: cumulative.outputTokens + cumulative.reasoningOutputTokens,
    reasoningOutputTokens: cumulative.reasoningOutputTokens,
    ...(cumulative.costUsd > 0 ? { costUsd: cumulative.costUsd } : {}),
  };
}
