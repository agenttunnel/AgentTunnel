/**
 * Token usage right-panel surface: provider-reported IN/OUT accounting for the
 * active thread, decomposed per turn and per subagent. Read-only — the model
 * carries only usage the providers actually reported.
 */
import { useAtomValue } from "@effect/atom-react";
import type {
  AgentPanelModel,
  RuntimeSubagent,
} from "@t3tools/client-runtime/state/subagentRuntime";
import {
  formatSubagentModelLabel,
  formatSubagentTokenCount,
} from "@t3tools/client-runtime/state/subagentRuntime";
import type { EnvironmentId, OrchestrationThreadActivity } from "@t3tools/contracts";
import type { TimestampFormat } from "@t3tools/contracts/settings";
import { ChartNoAxesColumnIncreasing, ChevronLeft, ChevronRight } from "lucide-react";
import { type ReactNode, useMemo, useState } from "react";

import type { ContextWindowSnapshot } from "../lib/contextWindow";
import { cn } from "~/lib/utils";
import { orchestrationEnvironment } from "~/state/orchestration";
import { Button } from "~/components/ui/button";
import { ScrollArea } from "~/components/ui/scroll-area";
import { StatusDot, subagentStatusLabel } from "./AgentsPanel";
import { findAgentById } from "./AgentsPanel.logic";
import { ContextUsageBar } from "./ContextUsageBar";
import { deriveSubagentUsageStats, deriveTokenUsage } from "./TokenUsagePanel.logic";

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

/**
 * Per-turn stats body for the selected subagent. Reads the usage entries the
 * adapter persists in the agent's log (polled while this view is mounted), so
 * a running agent's turns stream in as they complete.
 */
function AgentUsageStatsBody({
  agent,
  environmentId,
}: {
  agent: RuntimeSubagent;
  environmentId: EnvironmentId;
}) {
  const outputPath = agent.outputFile;
  if (outputPath === null) {
    return <EmptyNote>Per-turn usage isn't recorded for this agent.</EmptyNote>;
  }
  return <AgentUsageStatsLoaded environmentId={environmentId} outputPath={outputPath} />;
}

function AgentUsageStatsLoaded({
  environmentId,
  outputPath,
}: {
  environmentId: EnvironmentId;
  outputPath: string;
}) {
  const result = useAtomValue(
    orchestrationEnvironment.agentOutput({ environmentId, input: { outputPath } }),
  );
  if (result._tag === "Failure") {
    return <EmptyNote>Per-turn usage isn't recorded for this agent.</EmptyNote>;
  }
  if (result._tag !== "Success") {
    return <div className="text-xs text-muted-foreground">Loading turns…</div>;
  }
  const stats = deriveSubagentUsageStats(result.value.entries);
  if (!stats.hasUsage) {
    return <EmptyNote>No per-turn usage recorded for this agent yet.</EmptyNote>;
  }
  return (
    <div className="flex flex-col gap-3">
      <div className="grid grid-cols-2 gap-x-3 gap-y-2.5">
        <StatTile label="Turns" value={`${stats.turnCount}`} emphasis />
        <StatTile label="Total tokens" value={stats.totalLabel} emphasis />
        {stats.contextLabel !== null ? (
          <StatTile label="Context size" value={stats.contextLabel} />
        ) : null}
        <StatTile label="Input tokens" value={stats.inputLabel} />
        <StatTile label="Output tokens" value={stats.outputLabel} />
        {stats.cachedInputTokens > 0 ? (
          <StatTile label="Cached input" value={stats.cachedLabel} />
        ) : null}
        {stats.reasoningOutputTokens > 0 ? (
          <StatTile label="Reasoning output" value={stats.reasoningLabel} />
        ) : null}
      </div>
      <div className="overflow-x-auto rounded-md border border-border/50">
        <table className="w-full min-w-[26rem] border-collapse text-left font-mono text-[.68rem]">
          <thead>
            <tr className="text-[.6rem] uppercase tracking-wider text-muted-foreground/60">
              <th className="py-1 pr-2 pl-2 font-medium">Turn</th>
              <th className="py-1 pr-2 text-right font-medium">In</th>
              <th className="py-1 pr-2 text-right font-medium">Cached</th>
              <th className="py-1 pr-2 text-right font-medium">Out</th>
              <th className="py-1 pr-2 text-right font-medium">Reasoning</th>
              <th className="py-1 pr-2 text-right font-medium">Total</th>
            </tr>
          </thead>
          <tbody>
            {stats.turns.map((turn) => (
              <tr key={turn.ordinal} className="border-t border-border/40">
                <td className="py-1 pr-2 pl-2 text-muted-foreground">#{turn.ordinal}</td>
                <td className="py-1 pr-2 text-right tabular-nums text-foreground/90">
                  {turn.inputLabel}
                </td>
                <td className="py-1 pr-2 text-right tabular-nums text-muted-foreground">
                  {turn.cachedLabel}
                </td>
                <td className="py-1 pr-2 text-right tabular-nums text-foreground/90">
                  {turn.outputLabel}
                </td>
                <td className="py-1 pr-2 text-right tabular-nums text-muted-foreground">
                  {turn.reasoningLabel}
                </td>
                <td className="py-1 pr-2 text-right font-medium tabular-nums text-foreground">
                  {turn.totalLabel}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="px-0.5 text-[.65rem] leading-relaxed text-muted-foreground/60">
        One row per model step. Input and output include their cached and reasoning subsets; context
        size is the tokens fed into the latest step.
      </p>
    </div>
  );
}

/** Full-panel drill-in for one subagent: identity and its per-turn usage. */
function AgentStatsDetail({
  agent,
  environmentId,
  onBack,
}: {
  agent: RuntimeSubagent;
  environmentId: EnvironmentId | null;
  onBack: () => void;
}) {
  const modelLabel = formatSubagentModelLabel(agent.model, agent.effort);
  const metadata = [
    agent.role,
    agent.phaseTitle,
    agent.workflowName,
    modelLabel,
    agent.usage?.toolUses !== undefined ? `${agent.usage.toolUses} tools` : null,
  ].filter((value): value is string => typeof value === "string" && value.length > 0);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex items-center gap-2 border-b border-border/60 px-2 py-1.5">
        <Button
          size="icon-micro"
          variant="ghost-muted"
          onClick={onBack}
          aria-label="Back to agents"
        >
          <ChevronLeft aria-hidden className="size-3.5" />
        </Button>
        <StatusDot status={agent.status} />
        <span className="min-w-0 flex-1 truncate text-sm font-medium">
          {agent.title || subagentStatusLabel(agent.status)}
        </span>
      </header>
      {metadata.length > 0 ? (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 border-b border-border/40 px-3 py-1 font-mono text-[.7rem] text-muted-foreground">
          {metadata.map((entry) => (
            <span key={entry}>{entry}</span>
          ))}
        </div>
      ) : null}
      <ScrollArea className="min-h-0 flex-1">
        <div className="flex flex-col gap-3 p-3">
          <div>
            <div className="pb-1 text-[.65rem] font-medium uppercase tracking-wider text-muted-foreground/70">
              Task
            </div>
            <p className="whitespace-pre-wrap break-words text-xs leading-relaxed text-foreground/85">
              {agent.title}
            </p>
          </div>
          <div>
            <div className="pb-1 text-[.65rem] font-medium uppercase tracking-wider text-muted-foreground/70">
              Turns
            </div>
            {environmentId !== null ? (
              <AgentUsageStatsBody agent={agent} environmentId={environmentId} />
            ) : (
              <EmptyNote>Per-turn usage isn't recorded for this agent.</EmptyNote>
            )}
          </div>
        </div>
      </ScrollArea>
    </div>
  );
}

export function TokenUsagePanel({
  model,
  contextWindow,
  activities,
  timestampFormat,
  environmentId,
}: {
  model: AgentPanelModel;
  contextWindow: ContextWindowSnapshot | null;
  activities: ReadonlyArray<OrchestrationThreadActivity>;
  timestampFormat: TimestampFormat;
  environmentId: EnvironmentId | null;
}) {
  const usage = useMemo(
    () => deriveTokenUsage({ activities, model, contextWindow, timestampFormat }),
    [activities, model, contextWindow, timestampFormat],
  );
  const [selectedAgentId, setSelectedAgentId] = useState<string | null>(null);
  const selectedAgent = selectedAgentId === null ? null : findAgentById(model, selectedAgentId);

  if (selectedAgent !== null) {
    return (
      <AgentStatsDetail
        agent={selectedAgent}
        environmentId={environmentId}
        onBack={() => setSelectedAgentId(null)}
      />
    );
  }

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
              {usage.orchestrator !== null ? (
                <StatTile
                  label="Orchestrator tokens"
                  value={formatSubagentTokenCount(totals.mainTokens)}
                />
              ) : null}
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
            {usage.orchestrator !== null || usage.agents.length > 0 ? (
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
                    {usage.orchestrator !== null ? (
                      <tr
                        key={`orchestrator:${usage.orchestrator.id}`}
                        className="border-t border-border/40"
                      >
                        <td className="py-1 pr-2 pl-2">
                          <div className="flex max-w-44 items-center gap-1.5">
                            {usage.orchestrator.status !== null ? (
                              <StatusDot status={usage.orchestrator.status} />
                            ) : null}
                            <span className="truncate text-foreground/90">
                              {usage.orchestrator.title}
                            </span>
                            {usage.orchestrator.status !== null ? (
                              <span className="sr-only">
                                {subagentStatusLabel(usage.orchestrator.status)}
                              </span>
                            ) : null}
                          </div>
                        </td>
                        <td className="py-1 pr-2 text-muted-foreground">
                          {usage.orchestrator.modelLabel ?? "—"}
                        </td>
                        <td className="py-1 pr-2 text-right tabular-nums text-foreground/90">
                          {usage.orchestrator.inputLabel}
                        </td>
                        <td className="py-1 pr-2 text-right tabular-nums text-foreground/90">
                          {usage.orchestrator.outputLabel}
                        </td>
                        <td className="py-1 pr-2 text-right tabular-nums text-muted-foreground">
                          —
                        </td>
                        <td className="py-1 pr-2 text-right font-medium tabular-nums text-foreground">
                          {usage.orchestrator.totalLabel}
                        </td>
                      </tr>
                    ) : null}
                    {usage.agents.map((agent) => (
                      <tr
                        key={agent.id}
                        tabIndex={0}
                        role="button"
                        aria-label={`View usage for ${agent.title}`}
                        onClick={() => setSelectedAgentId(agent.id)}
                        onKeyDown={(event) => {
                          if (event.key === "Enter" || event.key === " ") {
                            event.preventDefault();
                            setSelectedAgentId(agent.id);
                          }
                        }}
                        className="cursor-pointer border-t border-border/40 hover:bg-accent/40 focus-visible:bg-accent/40 focus-visible:outline-none"
                      >
                        <td className="py-1 pr-2 pl-2">
                          <div className="flex max-w-44 items-center gap-1.5">
                            <StatusDot status={agent.status} />
                            <span className="truncate text-foreground/90">{agent.title}</span>
                            <span className="sr-only">{subagentStatusLabel(agent.status)}</span>
                            <ChevronRight
                              aria-hidden
                              className="size-3 shrink-0 text-muted-foreground/40"
                            />
                          </div>
                        </td>
                        <td className="py-1 pr-2 text-muted-foreground">
                          {agent.modelLabel ?? "—"}
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
            Provider-reported usage. Each turn shows its latest snapshot, and a subagent counts
            toward the turn that spawned it; Processed is the provider's cumulative session total
            for the main thread. In AgentTunnel threads the Orchestrator row is the main thread's
            own snapshots and has no per-step drill-in.
          </p>
        </div>
      </ScrollArea>
    </div>
  );
}
