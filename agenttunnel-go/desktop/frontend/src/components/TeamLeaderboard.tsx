import { useMemo } from "react";
import type { TeamStat } from "../lib/teamStats";
import { fmtTokens } from "../lib/usage";
import { fmtSeconds } from "./TaskTimeChart";

/** A team's members as one readable label, e.g. "claude/opus + codex/gpt-5". */
export const teamLabel = (t: Pick<TeamStat, "members">) => t.members.join(" + ");

const pct = (t: TeamStat) => (t.tasks === 0 ? 0 : Math.round((t.resolved / t.tasks) * 100));

/**
 * TeamLeaderboard ranks the teams that worked turns: which combination of
 * harness and model resolves the most tasks, how fast, and on how few tokens.
 * The table is the reading; the scatter puts speed and cost on one plane so the
 * best teams sit in the bottom-left corner.
 */
export function TeamLeaderboard({
  teams,
  loading,
  error,
  onRefresh,
}: {
  teams: TeamStat[];
  loading: boolean;
  error: string | null;
  onRefresh: () => void;
}) {
  // Teams that never resolved a task have no time or token figures to compare.
  const measured = teams.filter((t) => t.resolved > 0);
  const unmeasured = teams.length - measured.length;
  const fewest = measured.reduce<TeamStat | null>(
    (best, t) => (best === null || t.medianTokens < best.medianTokens ? t : best),
    null,
  );

  return (
    <div className="stats-panel">
      <div className="panel-bar">
        <span className="chat-title">Team effectiveness</span>
        <button onClick={onRefresh} disabled={loading}>
          {loading ? "Loading…" : "Refresh"}
        </button>
      </div>
      <p className="hint">
        A team is the harness and model pairs that worked one turn together. Ranked by the share of
        turns it resolved, then by median time. A turn is resolved when no lead failed.
      </p>
      {error && <p className="hint err-text">{error}</p>}
      {teams.length === 0 && !error && (
        <p className="hint">{loading ? "Loading…" : "No turn has finished yet."}</p>
      )}
      {teams.length > 0 && (
        <>
          <table className="stats-table team-table">
            <thead>
              <tr>
                <th>Team</th>
                <th className="num">Tasks</th>
                <th className="num">Resolved</th>
                <th className="num">Median time</th>
                <th className="num">p90</th>
                <th className="num">Median tokens</th>
              </tr>
            </thead>
            <tbody>
              {teams.map((t, i) => (
                <tr key={teamLabel(t)}>
                  <td>
                    <span className="team-rank">{i + 1}</span>
                    <span className="team-name" title={teamLabel(t)}>
                      {teamLabel(t)}
                    </span>
                    {t === fewest && <span className="team-badge">fewest tokens</span>}
                  </td>
                  <td className="num">{t.tasks}</td>
                  <td className="num">
                    {t.resolved} <span className="tt-n">{pct(t)}%</span>
                  </td>
                  <td className="num">{t.resolved > 0 ? fmtSeconds(t.medianSeconds) : "—"}</td>
                  <td className="num">{t.resolved > 0 ? fmtSeconds(t.p90Seconds) : "—"}</td>
                  <td className="num">{t.resolved > 0 ? fmtTokens(t.medianTokens) : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <TeamScatter teams={measured} />
          {unmeasured > 0 && (
            <p className="hint">
              {unmeasured} team{unmeasured === 1 ? " has" : "s have"} no resolved task yet, so they
              have no time or token figures.
            </p>
          )}
        </>
      )}
    </div>
  );
}

const W = 640;
const H = 300;
const M = { top: 14, right: 18, bottom: 40, left: 58 };

/** Rounds a maximum up to a tidy tick so the axis ends on a clean number. */
function niceMax(v: number): number {
  if (v <= 0) return 1;
  const exp = Math.pow(10, Math.floor(Math.log10(v)));
  const f = v / exp;
  const step = f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10;
  return step * exp;
}

/**
 * TeamScatter plots each measured team by median time per resolved task (x) and
 * median tokens per resolved task (y). Points are labelled with their rank in
 * the table above, so the two read together.
 */
function TeamScatter({ teams }: { teams: TeamStat[] }) {
  const { xMax, yMax } = useMemo(
    () => ({
      xMax: niceMax(Math.max(0, ...teams.map((t) => t.medianSeconds))),
      yMax: niceMax(Math.max(0, ...teams.map((t) => t.medianTokens))),
    }),
    [teams],
  );
  if (teams.length === 0) return null;

  const plotW = W - M.left - M.right;
  const plotH = H - M.top - M.bottom;
  const x = (v: number) => M.left + (v / xMax) * plotW;
  const y = (v: number) => M.top + plotH - (v / yMax) * plotH;
  const ticks = [0, 0.25, 0.5, 0.75, 1];

  return (
    <figure className="team-scatter">
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label="Teams by median time and tokens per task">
        {ticks.map((f) => (
          <g key={f}>
            <line className="ts-grid" x1={M.left} x2={W - M.right} y1={y(yMax * f)} y2={y(yMax * f)} />
            <text className="ts-tick" x={M.left - 8} y={y(yMax * f) + 3} textAnchor="end">
              {fmtTokens(yMax * f)}
            </text>
            <text className="ts-tick" x={x(xMax * f)} y={H - M.bottom + 16} textAnchor="middle">
              {fmtSeconds(xMax * f)}
            </text>
          </g>
        ))}
        <text className="ts-axis" x={W / 2} y={H - 6} textAnchor="middle">
          median time per resolved task →
        </text>
        <text
          className="ts-axis"
          transform={`translate(14 ${M.top + plotH / 2}) rotate(-90)`}
          textAnchor="middle"
        >
          median tokens per task →
        </text>
        {teams.map((t, i) => (
          <g key={teamLabel(t)}>
            <circle className="ts-dot" cx={x(t.medianSeconds)} cy={y(t.medianTokens)} r={5}>
              <title>{`#${i + 1} ${teamLabel(t)}: ${fmtSeconds(t.medianSeconds)}, ${fmtTokens(t.medianTokens)} tokens`}</title>
            </circle>
            <text className="ts-label" x={x(t.medianSeconds) + 8} y={y(t.medianTokens) + 4}>
              {i + 1}
            </text>
          </g>
        ))}
      </svg>
      <figcaption className="hint">Bottom-left is faster and cheaper. Numbers match the table.</figcaption>
    </figure>
  );
}
