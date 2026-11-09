import { fmtCost, fmtTokens, totalTokens, type TokenUsage } from "../lib/usage";

/** One harness's rollup across every thread in the environment (stats.harnesses). */
export type HarnessStat = {
  harness: string;
  threads: number;
  agents: number;
  subagents: number;
  usage: TokenUsage;
};

/**
 * HarnessStatsPanel shows which harnesses the environment uses most. Bars are
 * scaled to the busiest harness by agent count, so the table reads at a glance
 * without a chart library.
 */
export function HarnessStatsPanel({
  stats,
  loading,
  error,
  onRefresh,
}: {
  stats: HarnessStat[];
  loading: boolean;
  error: string | null;
  onRefresh: () => void;
}) {
  const busiest = Math.max(1, ...stats.map((s) => s.agents));
  return (
    <div className="stats-panel">
      <div className="panel-bar">
        <span className="chat-title">Harnesses</span>
        <button onClick={onRefresh} disabled={loading}>
          {loading ? "Loading…" : "Refresh"}
        </button>
      </div>
      {error && <p className="hint err-text">{error}</p>}
      {stats.length === 0 && !error && (
        <p className="hint">{loading ? "Loading…" : "No agents have run in this environment yet."}</p>
      )}
      {stats.length > 0 && (
        <table className="stats-table">
          <thead>
            <tr>
              <th>Harness</th>
              <th className="num">Agents</th>
              <th className="num">Subagents</th>
              <th className="num">Threads</th>
              <th className="num">Tokens</th>
              <th className="num">Cost</th>
            </tr>
          </thead>
          <tbody>
            {stats.map((s) => (
              <tr key={s.harness || "unknown"}>
                <td>
                  <div className="stats-name">{s.harness || "unknown"}</div>
                  <div className="stats-bar" style={{ width: `${(s.agents / busiest) * 100}%` }} />
                </td>
                <td className="num">{s.agents}</td>
                <td className="num">{s.subagents}</td>
                <td className="num">{s.threads}</td>
                <td className="num" title={`cached ${s.usage.cachedTokens}`}>
                  {fmtTokens(totalTokens(s.usage))}
                </td>
                <td className="num">{fmtCost(s.usage.costUsd)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
