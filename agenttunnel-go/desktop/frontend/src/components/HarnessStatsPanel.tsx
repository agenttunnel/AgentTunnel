import { useEffect, useRef, useState } from "react";
import type { Rpc } from "../lib/rpc";
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
        <p className="hint">{loading ? "Loading…" : "No agents have run in this scope yet."}</p>
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

type StatsScope = "thread" | "project" | "all";

const SCOPE_LABEL: Record<StatsScope, string> = { thread: "Thread", project: "Project", all: "All" };

/**
 * HarnessStatsView loads the rollup for one scope and shows it. With a thread or
 * project to narrow to it offers a picker; with neither (the app-level view) it
 * shows every project.
 */
export function HarnessStatsView({
  rpc,
  threadId,
  projectId,
}: {
  rpc: Rpc | null;
  threadId: string | null;
  projectId: string | null;
}) {
  const canPick = threadId !== null || projectId !== null;
  const [scope, setScope] = useState<StatsScope>(
    threadId !== null ? "thread" : projectId !== null ? "project" : "all",
  );
  const [stats, setStats] = useState<HarnessStat[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reload, setReload] = useState(0);
  // Each request gets a number; only the newest one may write, so a slow reply
  // for a previous scope never replaces the current one.
  const latest = useRef(0);

  // The params for the chosen scope, serialized so the effect only re-runs when
  // the scope actually changes. Null means the scope has no thread or project.
  const params =
    scope === "all"
      ? {}
      : scope === "thread"
        ? threadId !== null
          ? { threadId }
          : null
        : projectId !== null
          ? { projectId }
          : null;
  const paramsKey = params === null ? null : JSON.stringify(params);

  useEffect(() => {
    if (!rpc || paramsKey === null) return;
    const id = ++latest.current;
    setLoading(true);
    setError(null);
    rpc
      .call<HarnessStat[]>("stats.harnesses", JSON.parse(paramsKey))
      .then((next) => {
        if (id === latest.current) setStats(next);
      })
      .catch((e: unknown) => {
        if (id === latest.current) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (id === latest.current) setLoading(false);
      });
  }, [rpc, paramsKey, reload]);

  return (
    <>
      {canPick && (
        <div className="stats-scope" role="tablist" aria-label="Stats scope">
          {(["thread", "project", "all"] as const)
            .filter((s) => (s === "thread" ? threadId !== null : s === "project" ? projectId !== null : true))
            .map((s) => (
              <button
                key={s}
                role="tab"
                aria-selected={scope === s}
                className={scope === s ? "on" : ""}
                onClick={() => setScope(s)}
              >
                {SCOPE_LABEL[s]}
              </button>
            ))}
        </div>
      )}
      {paramsKey === null ? (
        <p className="hint">Select a thread or project to see its stats.</p>
      ) : (
        <HarnessStatsPanel
          stats={stats}
          loading={loading}
          error={error}
          onRefresh={() => setReload((n) => n + 1)}
        />
      )}
    </>
  );
}
