/**
 * Context-window usage meter shared by the Context and Token usage surfaces.
 * Renders only a snapshot the app already tracks; no estimates.
 */
import { cn } from "~/lib/utils";

export function ContextUsageBar({
  usedLabel,
  limitLabel,
  percentLabel,
  percentage,
}: {
  usedLabel: string;
  limitLabel: string;
  percentLabel: string | null;
  percentage: number;
}) {
  const normalized = Math.max(0, Math.min(100, percentage));
  return (
    <div className="rounded-md border border-border/50 bg-card/30 p-2">
      <div className="flex items-center justify-between gap-2 pb-1.5 font-mono text-[.7rem] text-muted-foreground">
        <span>Context usage</span>
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
        aria-label="Context window usage"
      >
        <div
          className={cn("h-full rounded-full", normalized > 90 ? "bg-destructive" : "bg-info")}
          style={{ width: `${normalized}%` }}
        />
      </div>
    </div>
  );
}
