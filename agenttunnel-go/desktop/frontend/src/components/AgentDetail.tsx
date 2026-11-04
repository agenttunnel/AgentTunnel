type Usage = {
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  reasoningTokens?: number;
  costUsd: number;
  toolUses: number;
};

type Step = { at: string; usage: Usage };
type Entry = { kind: string; text: string };

type AgentLike = {
  id: string;
  name?: string;
  role: string;
  harness: string;
  model: string;
  task: string;
  status: string;
  startedAt?: string;
  endedAt?: string;
};

const fmt = (n: number) =>
  n >= 1_000_000
    ? `${(n / 1_000_000).toFixed(2)}M`
    : n >= 1000
      ? `${(n / 1000).toFixed(1)}k`
      : String(n);
const money = (c: number) => `$${c.toFixed(4)}`;
const time = (iso?: string) => {
  if (!iso) return "";
  const t = new Date(iso);
  return Number.isNaN(t.getTime())
    ? ""
    : t.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
};
const duration = (from?: string, to?: string) => {
  if (!from || !to) return "";
  const secs = Math.max(0, Math.round((Date.parse(to) - Date.parse(from)) / 1000));
  return secs < 60
    ? `${secs}s`
    : `${Math.floor(secs / 60)}m ${String(secs % 60).padStart(2, "0")}s`;
};

const empty: Usage = {
  inputTokens: 0,
  outputTokens: 0,
  cachedTokens: 0,
  reasoningTokens: 0,
  costUsd: 0,
  toolUses: 0,
};
const add = (steps: Step[]): Usage =>
  steps.reduce<Usage>(
    (sum, s) => ({
      inputTokens: sum.inputTokens + s.usage.inputTokens,
      outputTokens: sum.outputTokens + s.usage.outputTokens,
      cachedTokens: sum.cachedTokens + s.usage.cachedTokens,
      reasoningTokens: (sum.reasoningTokens ?? 0) + (s.usage.reasoningTokens ?? 0),
      costUsd: sum.costUsd + s.usage.costUsd,
      toolUses: sum.toolUses + s.usage.toolUses,
    }),
    empty,
  );

/**
 * AgentDetail is the drill-in for one agent: its mission, its totals, one row per
 * model step, and the output it produced.
 */
export function AgentDetail({
  agent,
  steps,
  logs,
  roleName,
  onClose,
}: {
  agent: AgentLike;
  steps: Step[];
  logs: Entry[];
  roleName: string;
  onClose: () => void;
}) {
  const totals = add(steps);
  // Context size is what the latest step was fed, cache reads included.
  const last = steps[steps.length - 1]?.usage;

  return (
    <div className="overlay" onClick={onClose}>
      <div className="sheet wide" onClick={(e) => e.stopPropagation()}>
        <div className="sheet-head">
          <div className="detail-title">
            <span className={`badge ${agent.status}`}>{agent.status}</span>
            <strong>{agent.name || roleName}</strong>
            <span className="detail-sub">
              {roleName} · {agent.harness}
              {agent.model ? ` · ${agent.model}` : ""}
              {duration(agent.startedAt, agent.endedAt)
                ? ` · ${duration(agent.startedAt, agent.endedAt)}`
                : ""}
            </span>
          </div>
          <button onClick={onClose}>Close</button>
        </div>

        <div className="sheet-body">
          <section className="set-section">
            <h4>Mission</h4>
            <div className="md mission">{agent.task}</div>
          </section>

          <section className="set-section">
            <h4>Usage</h4>
            <div className="usage-grid">
              <div className="stat">
                <div className="k">Steps</div>
                <div className="v">{steps.length}</div>
              </div>
              <div className="stat">
                <div className="k">Total tokens</div>
                <div className="v">{fmt(totals.inputTokens + totals.outputTokens)}</div>
              </div>
              <div className="stat">
                <div className="k">Input</div>
                <div className="v">{fmt(totals.inputTokens)}</div>
              </div>
              <div className="stat">
                <div className="k">Output</div>
                <div className="v">{fmt(totals.outputTokens)}</div>
              </div>
              <div className="stat">
                <div className="k">Cached input</div>
                <div className="v">{fmt(totals.cachedTokens)}</div>
              </div>
              <div className="stat">
                <div className="k">Reasoning</div>
                <div className="v">{fmt(totals.reasoningTokens ?? 0)}</div>
              </div>
              <div className="stat">
                <div className="k">Context size</div>
                <div className="v">{last ? fmt(last.inputTokens + last.cachedTokens) : "0"}</div>
              </div>
              <div className="stat">
                <div className="k">Cost</div>
                <div className="v">{money(totals.costUsd)}</div>
              </div>
              <div className="stat">
                <div className="k">Tools</div>
                <div className="v">{totals.toolUses}</div>
              </div>
            </div>
          </section>

          <section className="set-section">
            <h4>By step</h4>
            {steps.length === 0 ? (
              <p className="hint">No usage recorded for this agent yet.</p>
            ) : (
              <div className="md-table-wrap">
                <table className="grid">
                  <thead>
                    <tr>
                      <th>Step</th>
                      <th>When</th>
                      <th>In</th>
                      <th>Cached</th>
                      <th>Out</th>
                      <th>Reasoning</th>
                      <th>Cost</th>
                      <th>Total</th>
                    </tr>
                  </thead>
                  <tbody>
                    {steps.map((s, i) => (
                      <tr key={i}>
                        <td>#{i + 1}</td>
                        <td>{time(s.at)}</td>
                        <td>{fmt(s.usage.inputTokens)}</td>
                        <td>{fmt(s.usage.cachedTokens)}</td>
                        <td>{fmt(s.usage.outputTokens)}</td>
                        <td>{fmt(s.usage.reasoningTokens ?? 0)}</td>
                        <td>{money(s.usage.costUsd)}</td>
                        <td>{fmt(s.usage.inputTokens + s.usage.outputTokens)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            <p className="hint">
              One row per model step. Tokens are that step's own; context size is what the latest
              step was fed, cache reads included.
            </p>
          </section>

          <section className="set-section">
            <h4>Output</h4>
            <div className="detail-logs">
              {logs.length === 0 && <div className="log-line raw">No output yet…</div>}
              {logs.map((l, i) => (
                <div key={i} className={`log-line ${l.kind}`}>
                  {l.text}
                </div>
              ))}
            </div>
          </section>
        </div>
      </div>
    </div>
  );
}
