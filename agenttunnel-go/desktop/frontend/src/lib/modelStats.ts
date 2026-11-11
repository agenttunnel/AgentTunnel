import { useCallback, useEffect, useState } from "react";
import type { Rpc } from "./rpc";
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

/** Loads the harness-and-model rollup once, and again only on refresh. */
export function useModelStats(rpc: Rpc | null) {
  const [stats, setStats] = useState<ModelStat[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    if (!rpc) return;
    let live = true;
    setLoading(true);
    setError(null);
    rpc
      .call<ModelStat[]>("stats.models")
      .then((next) => live && setStats(next))
      .catch((e: unknown) => live && setError(e instanceof Error ? e.message : String(e)))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [rpc, reload]);

  const refresh = useCallback(() => setReload((n) => n + 1), []);
  return { stats, loading, error, refresh };
}
