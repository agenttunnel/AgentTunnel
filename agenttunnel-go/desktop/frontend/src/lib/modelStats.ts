import type { Rpc } from "./rpc";
import { useStatsRpc } from "./rpcStats";
import type { TokenUsage } from "./usage";

/** One harness and model pair across every thread in the environment (stats.models). */
export type ModelStat = {
  harness: string;
  model: string;
  threads: number;
  agents: number;
  subagents: number;
  usage: TokenUsage;
  /** Subagent runs that succeeded: the sample behind the timing fields. */
  resolved: number;
  /** Wall-clock seconds of resolved runs; zero when there are none. */
  medianSeconds: number;
  p90Seconds: number;
};

/** A model string is empty when the agent ran on the harness default. */
export const comboLabel = (s: Pick<ModelStat, "harness" | "model">) =>
  `${s.harness || "unknown"} · ${s.model || "default"}`;

const NONE: ModelStat[] = [];

/** The harness-and-model rollup, loaded once and again only on refresh. */
export function useModelStats(rpc: Rpc | null) {
  const { data, loading, error, refresh } = useStatsRpc<ModelStat[]>(rpc, "stats.models", NONE);
  return { stats: data, loading, error, refresh };
}
