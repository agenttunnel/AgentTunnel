/**
 * Context-window usage derivation shared by the Context and Token usage
 * surfaces, so both read the same labels and percentage.
 */
import { formatContextWindowTokens, type ContextWindowSnapshot } from "../lib/contextWindow";

export interface ContextUsageSummary {
  readonly usedTokens: number;
  readonly maxTokens: number | null;
  readonly usedPercentage: number | null;
  readonly usedLabel: string;
  readonly limitLabel: string | null;
  readonly percentLabel: string | null;
}

function formatPercentage(value: number | null): string | null {
  if (value === null || !Number.isFinite(value)) {
    return null;
  }
  const clamped = Math.max(0, Math.min(100, value));
  if (clamped < 10) {
    return `${clamped.toFixed(1).replace(/\.0$/, "")}%`;
  }
  return `${Math.round(clamped)}%`;
}

export function deriveContextUsage(
  contextWindow: ContextWindowSnapshot | null,
): ContextUsageSummary | null {
  if (contextWindow === null) {
    return null;
  }
  const maxTokens = contextWindow.maxTokens ?? null;
  const usedPercentage = contextWindow.usedPercentage ?? null;
  return {
    usedTokens: contextWindow.usedTokens,
    maxTokens,
    usedPercentage,
    usedLabel: formatContextWindowTokens(contextWindow.usedTokens),
    limitLabel: maxTokens === null ? null : formatContextWindowTokens(maxTokens),
    percentLabel: formatPercentage(usedPercentage),
  };
}
