import { useEffect, useMemo, useState } from "react";
import type { Rpc } from "../lib/rpc";
import { fmtTokens, totalTokens, type TokenUsage } from "../lib/usage";

/** One harness and model pair across every thread in the environment (stats.models). */
export type ModelStat = {
  harness: string;
  model: string;
  threads: number;
  agents: number;
  subagents: number;
  usage: TokenUsage;
};

type Metric = "agents" | "input" | "output" | "cache";

const METRICS: { id: Metric; label: string; title: string }[] = [
  { id: "agents", label: "Most used", title: "Agents run on this harness and model" },
  { id: "input", label: "Input", title: "Input tokens" },
  { id: "output", label: "Output", title: "Output tokens" },
  { id: "cache", label: "Cache", title: "Cached input tokens read back" },
];

function valueOf(s: ModelStat, metric: Metric): number {
  switch (metric) {
    case "agents":
      return s.agents;
    case "input":
      return s.usage.inputTokens;
    case "output":
      return s.usage.outputTokens;
    case "cache":
      return s.usage.cachedTokens;
  }
}

/** A model string is empty when the agent ran on the harness default. */
export const comboLabel = (s: Pick<ModelStat, "harness" | "model">) =>
  `${s.harness || "unknown"} · ${s.model || "default"}`;

/**
 * ModelLeaderboard ranks harness and model pairs by how often they run and how
 * many tokens they move. The server sends the raw rollup; the ranking happens
 * here so switching metrics never costs a round trip.
 */
export function ModelLeaderboard({ rpc }: { rpc: Rpc | null }) {
  const [stats, setStats] = useState<ModelStat[]>([]);
  const [metric, setMetric] = useState<Metric>("agents");
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

  const ranked = useMemo(
    () => [...stats].sort((a, b) => valueOf(b, metric) - valueOf(a, metric)),
    [stats, metric],
  );
  const top = Math.max(1, ...ranked.map((s) => valueOf(s, metric)));
  const fmt = (s: ModelStat) =>
    metric === "agents" ? `${s.agents} run${s.agents === 1 ? "" : "s"}` : fmtTokens(valueOf(s, metric));

  return (
    <div className="stats-panel">
      <div className="panel-bar">
        <span className="chat-title">Leaderboard</span>
        <button onClick={() => setReload((n) => n + 1)} disabled={loading}>
          {loading ? "Loading…" : "Refresh"}
        </button>
      </div>
      <div className="stats-scope" role="tablist" aria-label="Leaderboard metric">
        {METRICS.map((m) => (
          <button
            key={m.id}
            role="tab"
            title={m.title}
            aria-selected={metric === m.id}
            className={metric === m.id ? "on" : ""}
            onClick={() => setMetric(m.id)}
          >
            {m.label}
          </button>
        ))}
      </div>
      {error && <p className="hint err-text">{error}</p>}
      {ranked.length === 0 && !error && (
        <p className="hint">{loading ? "Loading…" : "No agents have run yet."}</p>
      )}
      <ol className="lb-list">
        {ranked.map((s, i) => {
          const value = valueOf(s, metric);
          return (
            <li key={`${s.harness}/${s.model}`} className="lb-row">
              <span className="lb-rank">{i + 1}</span>
              <div className="lb-main">
                <div className="lb-name" title={`${comboLabel(s)} · ${totalTokens(s.usage)} tokens`}>
                  {comboLabel(s)}
                </div>
                <div className="lb-bar" style={{ width: `${(value / top) * 100}%` }} />
              </div>
              <span className="lb-value">{fmt(s)}</span>
            </li>
          );
        })}
      </ol>
    </div>
  );
}
