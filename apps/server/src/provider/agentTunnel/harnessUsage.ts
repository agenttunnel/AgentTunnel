/**
 * Harness token frames → contract usage shapes, shared by the AgentTunnel
 * adapter's orchestrator and agent streams.
 *
 * Every transport reports per-step deltas — OpenCode on `step_finish`, pi on
 * `message_end`, Claude Code per assistant message plus the run's cost on
 * `result`, Codex on `turn.completed` — so callers accumulate totals per
 * process (agents) or per session (orchestrator) and hand both the latest step
 * and the cumulative totals here. The thread snapshot keeps the step's values
 * in the per-turn fields (latest snapshot wins per turn) and the cumulative
 * total in `totalProcessedTokens`, matching how the Claude and Codex adapters
 * read.
 *
 * Costs ride along when the harness computed one. A harness with no rate for
 * the model reports zero; that is "unpriced", not free, so zero is dropped and
 * the surfaces show nothing rather than a made-up figure.
 */
import type { RuntimeTaskUsage, ThreadTokenUsageSnapshot } from "@t3tools/contracts";

/**
 * One harness-reported step, normalized across transports: `inputTokens` is
 * the *uncached* input, `outputTokens` the plain output with reasoning kept
 * beside it, and `totalTokens` the step's own sum. A cost-only frame (Claude
 * Code prices the whole run, not the message) reports zero tokens and the
 * run's cost.
 */
export interface HarnessStepUsage {
  readonly totalTokens: number;
  readonly inputTokens: number;
  readonly cachedInputTokens: number;
  readonly outputTokens: number;
  readonly reasoningOutputTokens: number;
  /** Cost the harness computed for this step, when it reported one above zero. */
  readonly costUsd?: number;
  /**
   * The harness's own id for the step. Claude Code emits one `assistant` event
   * per content block with the message's usage repeated on each, so callers
   * use this to fold a step once.
   */
  readonly stepId?: string;
}

export interface HarnessUsageTotals {
  totalTokens: number;
  inputTokens: number;
  cachedInputTokens: number;
  outputTokens: number;
  reasoningOutputTokens: number;
  costUsd: number;
  /** Newest step that reported tokens, for cost-only frames to lean on. */
  lastTokenStep?: HarnessStepUsage;
  /** Newest step id folded, so a repeated frame is not counted twice. */
  lastStepId?: string;
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

/**
 * Folds one step into the running totals. Returns undefined when the step
 * repeats the newest one — a harness that emits several frames per step (Claude
 * Code repeats a message's usage on every content-block event) must count it
 * once, and the caller uses this answer to log the step once as well.
 */
export function addHarnessUsageTotals(
  current: HarnessUsageTotals,
  step: HarnessStepUsage,
): HarnessUsageTotals | undefined {
  if (step.stepId !== undefined && step.stepId === current.lastStepId) {
    return undefined;
  }
  if (step.stepId !== undefined) current.lastStepId = step.stepId;
  current.totalTokens += step.totalTokens;
  current.inputTokens += step.inputTokens;
  current.cachedInputTokens += step.cachedInputTokens;
  current.outputTokens += step.outputTokens;
  current.reasoningOutputTokens += step.reasoningOutputTokens;
  if (step.costUsd !== undefined) current.costUsd += step.costUsd;
  // Claude prices the run on its closing frame, which carries no tokens of its
  // own; keeping the newest token-bearing step lets that cost reach the
  // surfaces without inventing a second set of per-turn numbers.
  if (step.totalTokens > 0) current.lastTokenStep = step;
  return current;
}

export function threadUsageSnapshotFromStep(
  step: HarnessStepUsage,
  cumulative: HarnessUsageTotals,
): ThreadTokenUsageSnapshot {
  // A cost-only frame repeats the last token-bearing step, so the snapshot's
  // per-turn figures stay the ones a provider actually reported.
  const tokens = step.totalTokens > 0 ? step : (cumulative.lastTokenStep ?? step);
  // Match the Claude and Codex snapshots: inputTokens/outputTokens are the
  // totals, and the cached/reasoning fields are their subsets (both harnesses
  // report them beside the uncached input and plain output). Reasoning must
  // never be added on top of output twice (UsageTokenTotals contract).
  const inputTokens = tokens.inputTokens + tokens.cachedInputTokens;
  const outputTokens = tokens.outputTokens + tokens.reasoningOutputTokens;
  return {
    usedTokens: tokens.totalTokens,
    totalProcessedTokens: cumulative.totalTokens,
    inputTokens,
    cachedInputTokens: tokens.cachedInputTokens,
    outputTokens,
    reasoningOutputTokens: tokens.reasoningOutputTokens,
    lastUsedTokens: tokens.totalTokens,
    lastInputTokens: inputTokens,
    lastCachedInputTokens: tokens.cachedInputTokens,
    lastOutputTokens: outputTokens,
    lastReasoningOutputTokens: tokens.reasoningOutputTokens,
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
