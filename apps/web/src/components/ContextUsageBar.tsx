/**
 * Context-window usage summary and meter shared by the Context and Token usage
 * surfaces, so both read the same labels and percentage from one derivation.
 */
import { cn } from "~/lib/utils";
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

export function ContextUsageBar({
  usedLabel,
  limitLabel,
  percentLabel,
  percentage,
  label = "Context usage",
}: {
  usedLabel: string;
  limitLabel: string;
  percentLabel: string | null;
  percentage: number;
  label?: string;
}) {
  const normalized = Math.max(0, Math.min(100, percentage));
  return (
    <div className="rounded-md border border-border/50 bg-card/30 p-2">
      <div className="flex items-center justify-between gap-2 pb-1.5 font-mono text-[.7rem] text-muted-foreground">
        <span>{label}</span>
        <span className="tabular-nums text-foreground/80">
          {usedLabel} / {limitLabel}
          {percentLabel === null ? "" : ` · ${percentLabel}`}
        </span>
      </div>
      <div
        className="h-1.5 w-full overflow-hidden rounded-full bg-muted/60"
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(normalized)}
        aria-label={label}
      >
        <div
          className={cn("h-full rounded-full", normalized > 90 ? "bg-destructive" : "bg-info")}
          style={{ width: `${normalized}%` }}
        />
      </div>
    </div>
  );
}
