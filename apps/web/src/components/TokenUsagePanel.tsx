/**
 * Token usage right-panel surface: provider-reported IN/OUT accounting for the
 * active thread, decomposed per turn and per subagent. Read-only — the model
 * carries only usage the providers actually reported.
 */
import type { AgentPanelModel } from "@t3tools/client-runtime/state/subagentRuntime";
import {
  formatSubagentModelLabel,
  formatSubagentTokenCount,
} from "@t3tools/client-runtime/state/subagentRuntime";
import type { OrchestrationThreadActivity } from "@t3tools/contracts";
import type { TimestampFormat } from "@t3tools/contracts/settings";
import { ChartNoAxesColumnIncreasing } from "lucide-react";
import { type ReactNode, useMemo } from "react";

import type { ContextWindowSnapshot } from "../lib/contextWindow";
import { cn } from "~/lib/utils";
import { ScrollArea } from "~/components/ui/scroll-area";
import { ContextUsageBar } from "./ContextUsageBar";
import { deriveTokenUsage } from "./TokenUsagePanel.logic";

const STATUS_DOT_CLASSES: Record<string, string> = {
  pending: "bg-info",
  running: "bg-info",
  waiting: "bg-info",
  idle: "bg-muted-foreground/50",
  completed: "bg-success",
  failed: "bg-destructive",
  cancelled: "bg-muted-foreground/60",
  interrupted: "bg-muted-foreground/60",
};

function SectionHeader({ children }: { children: ReactNode }) {
  return (
    <div className="px-0.5 pb-1.5 text-[.65rem] font-medium uppercase tracking-wider text-muted-foreground/70">
      {children}
    </div>
  );
}

function StatTile({
  label,
  value,
  emphasis = false,
}: {
  label: string;
  value: string;
  emphasis?: boolean;
}) {
  return (
    <div className="min-w-0">
      <div className="truncate text-[.6rem] uppercase tracking-wider text-muted-foreground/60">
        {label}
      </div>
      <div
        className={cn(
          "truncate font-mono text-xs tabular-nums",
          emphasis ? "font-medium text-foreground" : "text-foreground/90",
        )}
      >
        {value}
      </div>
    </div>
  );
}

function EmptyNote({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-md border border-border/50 bg-card/30 px-2.5 py-2 text-xs text-muted-foreground">
      {children}
    </div>
  );
}

export function TokenUsagePanel({
  model,
  contextWindow,
  activities,
  timestampFormat,
}: {
  model: AgentPanelModel;
  contextWindow: ContextWindowSnapshot | null;
  activities: ReadonlyArray<OrchestrationThreadActivity>;
  timestampFormat: TimestampFormat;
}) {
  const usage = useMemo(
    () => deriveTokenUsage({ activities, model, contextWindow, timestampFormat }),
    [activities, model, contextWindow, timestampFormat],
  );
  const totals = usage.totals;

  if (!usage.hasUsage) {
    return (
      <div className="flex h-full min-h-0 flex-col items-center justify-center gap-2 p-6 text-center">
        <ChartNoAxesColumnIncreasing aria-hidden className="size-5 text-muted-foreground/50" />
        <p className="text-xs text-muted-foreground">No token usage recorded in this thread yet.</p>
        <p className="max-w-60 text-[.65rem] leading-relaxed text-muted-foreground/60">
          Providers report usage as turns run; subagents show up here when they do.
        </p>
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ScrollArea className="min-h-0 flex-1">
        <div className="flex flex-col gap-3 p-3">
          <section>
            <SectionHeader>Token usage</SectionHeader>
            <div className="grid grid-cols-2 gap-x-3 gap-y-2.5">
              <StatTile
                label="Total tokens"
                value={formatSubagentTokenCount(totals.totalTokens)}
                emphasis
              />
              <StatTile
                label="Subagent tokens"
                value={formatSubagentTokenCount(totals.subagentTokens)}
              />
              <StatTile label="Input tokens" value={formatSubagentTokenCount(totals.inputTokens)} />
              <StatTile
                label="Output tokens"
                value={formatSubagentTokenCount(totals.outputTokens)}
              />
              {totals.cachedInputTokens > 0 ? (
                <StatTile
                  label="Cached input"
                  value={formatSubagentTokenCount(totals.cachedInputTokens)}
                />
              ) : null}
              {totals.reasoningOutputTokens > 0 ? (
                <StatTile
                  label="Reasoning output"
                  value={formatSubagentTokenCount(totals.reasoningOutputTokens)}
                />
              ) : null}
              {totals.processedTokens !== null ? (
                <StatTile
                  label="Processed"
                  value={formatSubagentTokenCount(totals.processedTokens)}
                />
              ) : null}
              <StatTile label="Agents" value={`${totals.agentCount}`} />
            </div>
          </section>

          {usage.context !== null &&
          usage.context.maxTokens !== null &&
          usage.context.usedPercentage !== null ? (
            <ContextUsageBar
              usedLabel={usage.context.usedLabel}
              limitLabel={usage.context.limitLabel ?? String(usage.context.maxTokens)}
              percentLabel={usage.context.percentLabel}
              percentage={usage.context.usedPercentage}
            />
          ) : null}

          <section>
            <SectionHeader>By turn</SectionHeader>
            {usage.turns.length > 0 ? (
              <div className="overflow-x-auto rounded-md border border-border/50">
                <table className="w-full min-w-[24rem] border-collapse text-left font-mono text-[.68rem]">
                  <thead>
                    <tr className="text-[.6rem] uppercase tracking-wider text-muted-foreground/60">
                      <th className="py-1 pr-2 pl-2 font-medium">Turn</th>
                      <th className="py-1 pr-2 font-medium">When</th>
                      <th className="py-1 pr-2 text-right font-medium">In</th>
                      <th className="py-1 pr-2 text-right font-medium">Out</th>
                      <th className="py-1 pr-2 text-right font-medium">Subagents</th>
                      <th className="py-1 pr-2 text-right font-medium">Total</th>
                    </tr>
                  </thead>
                  <tbody>
                    {usage.turns.map((turn) => (
                      <tr key={turn.turnId} className="border-t border-border/40">
                        <td className="py-1 pr-2 pl-2 text-muted-foreground">#{turn.ordinal}</td>
                        <td className="py-1 pr-2 text-muted-foreground">{turn.timeLabel}</td>
                        <td className="py-1 pr-2 text-right tabular-nums text-foreground/90">
                          {turn.inputLabel}
                        </td>
                        <td className="py-1 pr-2 text-right tabular-nums text-foreground/90">
                          {turn.outputLabel}
                        </td>
                        <td className="py-1 pr-2 text-right tabular-nums text-muted-foreground">
                          {turn.subagentLabel}
                        </td>
                        <td className="py-1 pr-2 text-right font-medium tabular-nums text-foreground">
                          {turn.totalLabel}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <EmptyNote>No per-turn usage recorded.</EmptyNote>
            )}
          </section>

          <section>
            <SectionHeader>Agents</SectionHeader>
            {usage.agents.length > 0 ? (
              <div className="overflow-x-auto rounded-md border border-border/50">
                <table className="w-full min-w-[24rem] border-collapse text-left font-mono text-[.68rem]">
                  <thead>
                    <tr className="text-[.6rem] uppercase tracking-wider text-muted-foreground/60">
                      <th className="py-1 pr-2 pl-2 font-medium">Agent</th>
                      <th className="py-1 pr-2 font-medium">Model</th>
                      <th className="py-1 pr-2 text-right font-medium">In</th>
                      <th className="py-1 pr-2 text-right font-medium">Out</th>
                      <th className="py-1 pr-2 text-right font-medium">Tools</th>
                      <th className="py-1 pr-2 text-right font-medium">Total</th>
                    </tr>
                  </thead>
                  <tbody>
                    {usage.agents.map((agent) => (
                      <tr key={agent.id} className="border-t border-border/40">
                        <td className="py-1 pr-2 pl-2">
                          <div className="flex max-w-44 items-center gap-1.5">
                            <span
                              aria-hidden
                              className={cn(
                                "size-1.5 shrink-0 rounded-full",
                                STATUS_DOT_CLASSES[agent.status] ?? "bg-muted-foreground/50",
                              )}
                            />
                            <span className="truncate text-foreground/90">{agent.title}</span>
                          </div>
                        </td>
                        <td className="py-1 pr-2 text-muted-foreground">
                          {formatSubagentModelLabel(agent.model, null) ?? "—"}
                        </td>
                        <td className="py-1 pr-2 text-right tabular-nums text-foreground/90">
                          {agent.inputLabel}
                        </td>
                        <td className="py-1 pr-2 text-right tabular-nums text-foreground/90">
                          {agent.outputLabel}
                        </td>
                        <td className="py-1 pr-2 text-right tabular-nums text-muted-foreground">
                          {agent.toolUses ?? "—"}
                        </td>
                        <td className="py-1 pr-2 text-right font-medium tabular-nums text-foreground">
                          {agent.totalLabel}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <EmptyNote>No subagents in this thread.</EmptyNote>
            )}
          </section>

          <p className="px-0.5 text-[.65rem] leading-relaxed text-muted-foreground/60">
            Provider-reported usage. Each turn shows its latest snapshot; a subagent counts toward
            the turn that spawned it.
          </p>
        </div>
      </ScrollArea>
    </div>
  );
}
