/**
 * Context right-panel surface: a read-only analysis of the active AgentTunnel
 * thread. Renders only data the app already tracks — the thread read model,
 * the latest context-window snapshot, the subagent roster, and the
 * AgentTunnel role overrides. No cost, per-message, or tool-token estimates.
 */
import type { AgentPanelModel } from "@t3tools/client-runtime/state/subagentRuntime";
import type { TimestampFormat } from "@t3tools/contracts/settings";
import { Gauge } from "lucide-react";
import { type ReactNode, useMemo } from "react";

import type { ContextWindowSnapshot } from "../lib/contextWindow";
import { cn } from "~/lib/utils";
import { ScrollArea } from "~/components/ui/scroll-area";
import type { AgentTunnelRoleOption } from "@t3tools/client-runtime/state/agentTunnelRoles";
import { ContextUsageBar } from "./ContextUsageBar";
import { type ContextAnalysisThread, deriveContextAnalysis } from "./ContextPanel.logic";

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

export function ContextPanel({
  model,
  contextWindow,
  thread,
  providerLabel,
  modelLabel,
  roles,
  timestampFormat,
}: {
  model: AgentPanelModel;
  contextWindow: ContextWindowSnapshot | null;
  thread: ContextAnalysisThread;
  providerLabel: string;
  modelLabel: string | null;
  roles: ReadonlyArray<AgentTunnelRoleOption>;
  timestampFormat: TimestampFormat;
}) {
  const analysis = useMemo(
    () =>
      deriveContextAnalysis({
        model,
        contextWindow,
        thread,
        providerLabel,
        modelLabel,
        roles,
        timestampFormat,
      }),
    [model, contextWindow, thread, providerLabel, modelLabel, roles, timestampFormat],
  );
  const usage = analysis.usage;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ScrollArea className="min-h-0 flex-1">
        <div className="flex flex-col gap-3 p-3">
          <section>
            <SectionHeader>Overview</SectionHeader>
            <div className="grid grid-cols-2 gap-x-3 gap-y-2.5">
              {analysis.stats.map((stat) => (
                <div key={stat.label} className="min-w-0">
                  <div className="truncate text-[.6rem] uppercase tracking-wider text-muted-foreground/60">
                    {stat.label}
                  </div>
                  <div className="truncate font-mono text-xs tabular-nums text-foreground/90">
                    {stat.value}
                  </div>
                </div>
              ))}
            </div>
          </section>

          {usage !== null && usage.maxTokens !== null && usage.usedPercentage !== null ? (
            <ContextUsageBar
              usedLabel={usage.usedLabel}
              limitLabel={usage.limitLabel ?? String(usage.maxTokens)}
              percentLabel={usage.percentLabel}
              percentage={usage.usedPercentage}
            />
          ) : null}

          {analysis.roleModels.length > 0 ? (
            <section>
              <SectionHeader>Role models</SectionHeader>
              <div className="flex flex-col gap-1">
                {analysis.roleModels.map((role) => (
                  <div
                    key={role.id}
                    className="flex items-center justify-between gap-2 rounded-md border border-border/50 bg-card/30 px-2 py-1"
                  >
                    <span className="shrink-0 text-xs text-muted-foreground">{role.label}</span>
                    <span
                      className={cn(
                        "truncate font-mono text-[.7rem]",
                        role.auto || !role.model
                          ? "text-muted-foreground/60"
                          : "text-foreground/90",
                      )}
                    >
                      {role.value}
                    </span>
                  </div>
                ))}
              </div>
            </section>
          ) : null}

          <section>
            <SectionHeader>Agents</SectionHeader>
            {analysis.hasAgents ? (
              <div className="overflow-x-auto rounded-md border border-border/50">
                <table className="w-full min-w-[26rem] border-collapse text-left font-mono text-[.68rem]">
                  <thead>
                    <tr className="text-[.6rem] uppercase tracking-wider text-muted-foreground/60">
                      <th className="py-1 pr-2 pl-2 font-medium">Role</th>
                      <th className="py-1 pr-2 font-medium">Title</th>
                      <th className="py-1 pr-2 font-medium">Pipeline</th>
                      <th className="py-1 pr-2 font-medium">Status</th>
                      <th className="py-1 pr-2 font-medium">Duration</th>
                      <th className="py-1 pr-2 text-right font-medium">Tokens</th>
                      <th className="py-1 pr-2 text-right font-medium">Output</th>
                    </tr>
                  </thead>
                  <tbody>
                    {analysis.agents.map((agent) => (
                      <tr key={agent.id} className="border-t border-border/40 align-top">
                        <td className="py-1 pr-2 pl-2 text-muted-foreground">
                          {agent.role ?? "—"}
                        </td>
                        <td className="py-1 pr-2">
                          <div className="max-w-40 truncate text-foreground/90">{agent.title}</div>
                        </td>
                        <td className="py-1 pr-2 text-muted-foreground">{agent.pipeline ?? "—"}</td>
                        <td className="py-1 pr-2">
                          <span className="inline-flex items-center gap-1.5 text-muted-foreground">
                            <span
                              aria-hidden
                              className={cn(
                                "size-1.5 shrink-0 rounded-full",
                                STATUS_DOT_CLASSES[agent.status] ?? "bg-muted-foreground/50",
                              )}
                            />
                            {agent.statusLabel}
                          </span>
                        </td>
                        <td className="py-1 pr-2 tabular-nums text-muted-foreground">
                          {agent.duration ?? "—"}
                        </td>
                        <td className="py-1 pr-2 text-right tabular-nums text-muted-foreground">
                          {agent.tokens}
                        </td>
                        <td className="py-1 pr-2 text-right tabular-nums text-muted-foreground">
                          {agent.outputLines}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <div className="flex items-center gap-2 rounded-md border border-border/50 bg-card/30 px-2.5 py-2 text-xs text-muted-foreground">
                <Gauge aria-hidden className="size-3.5 shrink-0 text-muted-foreground/60" />
                No agents in this thread yet. Subagents and workflows show up here when they run.
              </div>
            )}
          </section>
        </div>
      </ScrollArea>
    </div>
  );
}
