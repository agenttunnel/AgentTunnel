// Token usage helpers shared by the aside panels, the thread info dialog, and
// the agent drill-in. Pure functions over the wire shapes.

export type TokenUsage = {
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  reasoningTokens?: number;
  costUsd: number;
  toolUses: number;
};
export type UsageEntry = { agentId: string; role: string; usage: TokenUsage };
export type UsageStep = { at: string; usage: TokenUsage };
export type TurnUsage = {
  turn: number;
  at?: string;
  agentOrder: string[] | null;
  agents: Record<string, TokenUsage> | null;
};
export type ThreadUsage = {
  orchestrator: TokenUsage;
  subagents: TokenUsage;
  byAgent: UsageEntry[] | null;
};

export const zeroUsage: TokenUsage = {
  inputTokens: 0,
  outputTokens: 0,
  cachedTokens: 0,
  costUsd: 0,
  toolUses: 0,
};

export const addUsage = (a: TokenUsage, b: TokenUsage): TokenUsage => ({
  inputTokens: a.inputTokens + b.inputTokens,
  outputTokens: a.outputTokens + b.outputTokens,
  cachedTokens: a.cachedTokens + b.cachedTokens,
  reasoningTokens: (a.reasoningTokens ?? 0) + (b.reasoningTokens ?? 0),
  costUsd: a.costUsd + b.costUsd,
  toolUses: a.toolUses + b.toolUses,
});

export const totalTokens = (u: TokenUsage) => u.inputTokens + u.outputTokens;
export const fmtTokens = (n: number) => (n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n));
export const fmtCost = (c: number) => `$${c.toFixed(4)}`;

/** Splits one thread's spend into orchestrator vs subagents, in agent order.
 * Accepts anything shaped like a thread state, so callers can pass fetched
 * states without importing the app's full types. */
export function deriveThreadUsage(
  t: {
    agentOrder?: string[] | null;
    agents?: Record<string, { parentAgentId?: string }> | null;
    usage?: Record<string, UsageEntry> | null;
  } | null,
): ThreadUsage {
  const out: ThreadUsage = { orchestrator: zeroUsage, subagents: zeroUsage, byAgent: [] };
  if (!t?.agentOrder) return out;
  for (const id of t.agentOrder) {
    const entry = t.usage?.[id];
    if (!entry) continue;
    out.byAgent!.push(entry);
    const isOrchestrator = !t.agents?.[id]?.parentAgentId;
    if (isOrchestrator) out.orchestrator = addUsage(out.orchestrator, entry.usage);
    else out.subagents = addUsage(out.subagents, entry.usage);
  }
  return out;
}
