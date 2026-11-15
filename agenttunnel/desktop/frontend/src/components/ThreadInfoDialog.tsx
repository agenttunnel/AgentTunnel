import {
  addUsage,
  deriveThreadUsage,
  fmtCost,
  fmtTokens,
  totalTokens,
  type TokenUsage,
  type UsageEntry,
  type UsageStep,
} from "../lib/usage";

// Local structural types: the wire shape of a thread state, without importing
// the app component (same approach as AgentDetail).
type AgentLike = {
  id: string;
  parentAgentId?: string;
  name?: string;
  role: string;
  harness: string;
  model: string;
  task: string;
  status: string;
  startedAt?: string;
  endedAt?: string;
};
type StateLike = {
  title: string;
  turn: number;
  /** Who started this thread; absent (older threads) means user. */
  owner?: "user" | "orchestrator";
  messages?: { id: string }[] | null;
  agents: Record<string, AgentLike> | null;
  agentOrder: string[] | null;
  usage: Record<string, UsageEntry> | null;
  steps: Record<string, UsageStep[]> | null;
};

const duration = (from?: string, to?: string) => {
  if (!from || !to) return "";
  const secs = Math.max(0, Math.round((Date.parse(to) - Date.parse(from)) / 1000));
  return secs < 60
    ? `${secs}s`
    : `${Math.floor(secs / 60)}m ${String(secs % 60).padStart(2, "0")}s`;
};

/**
 * ThreadInfoDialog drills into a thread from its context menu: usage totals
 * split orchestrator vs subagents, and the subagent roster with missions.
 * The state is fetched fresh when the dialog opens, so it works for threads
 * that are not currently open.
 */
export function ThreadInfoDialog({
  thread,
  loading,
  tab,
  onTab,
  onClose,
}: {
  thread: StateLike | null;
  loading: boolean;
  tab: "usage" | "agents";
  onTab: (tab: "usage" | "agents") => void;
  onClose: () => void;
}) {
  const usage = deriveThreadUsage(thread);
  const all = addUsage(usage.orchestrator, usage.subagents);
  const agents = (thread?.agentOrder ?? [])
    .map((id) => thread?.agents?.[id])
    .filter((a): a is AgentLike => Boolean(a));

  return (
    <div className="overlay" onClick={onClose}>
      <div className="sheet wide" onClick={(e) => e.stopPropagation()}>
        <div className="sheet-head">
          <div className="detail-title">
            <strong>{thread?.title ?? "Thread"}</strong>
            <span className="detail-sub">
              {thread
                ? `${agents.length} agents · turn #${thread.turn} · ${
                    thread.owner === "orchestrator" ? "started by the orchestrator" : "started by you"
                  }`
                : "loading…"}
            </span>
          </div>
          <button onClick={onClose}>Close</button>
        </div>
        <div className="sheet-body">
          <div className="tabs dialog-tabs">
            <div className={`tab ${tab === "usage" ? "on" : ""}`} onClick={() => onTab("usage")}>
              Usage
            </div>
            <div className={`tab ${tab === "agents" ? "on" : ""}`} onClick={() => onTab("agents")}>
              Subagents
              {agents.length > 0 && <span className="pill">{agents.length}</span>}
            </div>
          </div>

          {loading && <p className="hint">Loading thread…</p>}
          {!loading && !thread && <p className="hint">Thread not found.</p>}

          {!loading && thread && tab === "usage" && (
            <>
              <section className="set-section">
                <div className="usage-grid">
                  <div className="stat">
                    <div className="k">Total tokens</div>
                    <div className="v">{fmtTokens(totalTokens(all))}</div>
                  </div>
                  <div className="stat">
                    <div className="k">Orchestrator</div>
                    <div className="v">{fmtTokens(totalTokens(usage.orchestrator))}</div>
                  </div>
                  <div className="stat">
                    <div className="k">Subagents</div>
                    <div className="v">{fmtTokens(totalTokens(usage.subagents))}</div>
                  </div>
                  <div className="stat">
                    <div className="k">Input</div>
                    <div className="v">{fmtTokens(all.inputTokens)}</div>
                  </div>
                  <div className="stat">
                    <div className="k">Output</div>
                    <div className="v">{fmtTokens(all.outputTokens)}</div>
                  </div>
                  <div className="stat">
                    <div className="k">Cached input</div>
                    <div className="v">{fmtTokens(all.cachedTokens)}</div>
                  </div>
                  <div className="stat">
                    <div className="k">Tools</div>
                    <div className="v">{all.toolUses}</div>
                  </div>
                  <div className="stat">
                    <div className="k">Cost</div>
                    <div className="v">{fmtCost(all.costUsd)}</div>
                  </div>
                </div>
              </section>
              {usage.byAgent && usage.byAgent.length > 0 && (
                <section className="set-section">
                  <h4>By agent</h4>
                  <div className="md-table-wrap">
                    <table className="grid">
                      <thead>
                        <tr>
                          <th>Agent</th>
                          <th>Kind</th>
                          <th>Role</th>
                          <th>Tokens</th>
                          <th>Cost</th>
                        </tr>
                      </thead>
                      <tbody>
                        {usage.byAgent.map((e) => {
                          const a = thread.agents?.[e.agentId];
                          return (
                            <tr key={e.agentId}>
                              <td>{a?.name || a?.role || e.role || e.agentId}</td>
                              <td>{a?.parentAgentId ? "subagent" : "orchestrator"}</td>
                              <td>{e.role}</td>
                              <td>{fmtTokens(totalTokens(e.usage))}</td>
                              <td>{fmtCost(e.usage.costUsd)}</td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </div>
                </section>
              )}
            </>
          )}

          {!loading && thread && tab === "agents" && (
            <section className="set-section">
              {agents.length === 0 && (
                <p className="hint">No agents have run in this thread yet.</p>
              )}
              {agents.map((a) => (
                <div className="wf-row" key={a.id} style={{ cursor: "default" }}>
                  <span className={`dot ${a.status}`} />
                  <div className="wf-main">
                    <div className="wf-name">
                      {a.name || a.role}
                      {!a.parentAgentId && <span className="wf-kind">orchestrator</span>}
                    </div>
                    <div className="wf-task">{a.task}</div>
                    <div className="wf-sub">
                      {a.role} · {a.harness}
                      {a.model ? ` · ${a.model}` : ""}
                      {duration(a.startedAt, a.endedAt)
                        ? ` · ${duration(a.startedAt, a.endedAt)}`
                        : ""}
                    </div>
                  </div>
                  <div className="wf-side">
                    <div className="wf-role">{a.status}</div>
                  </div>
                </div>
              ))}
            </section>
          )}
        </div>
      </div>
    </div>
  );
}

export type { AgentLike, StateLike, TokenUsage };
