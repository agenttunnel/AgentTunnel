import { comboLabel, type ModelStat } from "../lib/modelStats";

/** Seconds as "12.3s", or "1m 05s" once a task runs past a minute. */
export function fmtSeconds(s: number): string {
  if (s < 60) return `${s.toFixed(1)}s`;
  const whole = Math.round(s);
  return `${Math.floor(whole / 60)}m ${String(whole % 60).padStart(2, "0")}s`;
}

/**
 * TaskTimeChart ranks harness and model pairs by how long a successful task
 * takes, fastest first, in the style of a benchmark chart: one bar per pair with
 * the median as its length and a tick at p90. Pairs with no resolved task are
 * left out, and counted, rather than drawn as a zero-length bar.
 */
export function TaskTimeChart({ stats }: { stats: ModelStat[] }) {
  const rows = stats
    .filter((s) => s.resolved > 0)
    .sort((a, b) => a.medianSeconds - b.medianSeconds);
  const unmeasured = stats.length - rows.length;
  const scale = Math.max(1, ...rows.map((r) => r.p90Seconds));

  return (
    <div className="stats-panel">
      <div className="panel-bar">
        <span className="chat-title">Time per task</span>
      </div>
      <p className="hint">
        Wall-clock time of subagent tasks that succeeded. The bar is the median; the tick is p90.
        Fastest first.
      </p>
      {rows.length === 0 && <p className="hint">No task has succeeded yet.</p>}
      {rows.length > 0 && (
        <div className="tt-chart" role="list" aria-label="Time per task by harness and model">
          {rows.map((r) => (
            <div className="tt-row" role="listitem" key={`${r.harness}/${r.model}`}>
              <div className="tt-label" title={comboLabel(r)}>
                {comboLabel(r)}
              </div>
              <div
                className="tt-track"
                title={`median ${fmtSeconds(r.medianSeconds)}, p90 ${fmtSeconds(r.p90Seconds)}, ${r.resolved} task${r.resolved === 1 ? "" : "s"}`}
              >
                <div className="tt-bar" style={{ width: `${(r.medianSeconds / scale) * 100}%` }} />
                <div className="tt-p90" style={{ left: `${(r.p90Seconds / scale) * 100}%` }} />
              </div>
              <div className="tt-value">
                {fmtSeconds(r.medianSeconds)}
                <span className="tt-n">n={r.resolved}</span>
              </div>
            </div>
          ))}
          <div className="tt-axis" aria-hidden="true">
            <span className="tt-axis-gap" />
            <div className="tt-axis-ticks">
              <span>0</span>
              <span>{fmtSeconds(scale)}</span>
            </div>
          </div>
        </div>
      )}
      {unmeasured > 0 && (
        <p className="hint">
          {unmeasured} harness and model pair{unmeasured === 1 ? " has" : "s have"} no succeeded task
          yet, so not shown.
        </p>
      )}
    </div>
  );
}
