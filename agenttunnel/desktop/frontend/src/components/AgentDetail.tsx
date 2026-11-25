import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ToolCard } from "./ToolCard";
import { AgentElapsed, PhaseBadge, PhaseGlyph } from "./AgentStatus";
import type { DetailTab } from "./AgentTree";
import { agentStopped, foldToolLines } from "../lib/toolCalls";
import { agentLogStore } from "../lib/liveLog";
import { actionText } from "../lib/agentLive";
import { agentName, agentPhase, askingAgents, blockers, resolveDeps, workStart, type TreeAgent } from "../lib/agentTree";
import type { AgentQuestion } from "../lib/questions";
import { useStoreField } from "../lib/store";
import { copyText } from "../lib/clipboard";

type Usage = {
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  reasoningTokens?: number;
  costUsd: number;
  toolUses: number;
};

type Step = { at: string; usage: Usage };

type AgentLike = TreeAgent & {
  harness: string;
  model: string;
  /** The harness's id for the latest run; present only when it can be resumed. */
  harnessSession?: string;
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

/** How many of the newest output lines render before "show earlier". */
const OUTPUT_WINDOW = 200;
/** Output this close to the end counts as following the tail. */
const TAIL_SLACK = 40;

const TABS: { id: DetailTab; label: string }[] = [
  { id: "output", label: "Output" },
  { id: "tasks", label: "Tasks" },
  { id: "usage", label: "Usage" },
];

/**
 * AgentDetail is the drill-in for one agent. A summary header (phase, runtime,
 * time worked, usage, and what it is doing now) sits over three tabs: Output
 * (its log with tool cards, following the tail until you scroll up), Task (its
 * mission, what it waits on, and what it dispatched), and Usage (totals and one
 * row per model step).
 */
export function AgentDetail({
  agent,
  steps,
  roleName,
  agents,
  roleNames,
  questions,
  initialTab = "output",
  onOpenAgent,
  onResume,
  onClose,
}: {
  agent: AgentLike;
  steps: Step[];
  roleName: string;
  /** The thread's agents, for dependencies and dispatched work. */
  agents: AgentLike[];
  roleNames: Record<string, string>;
  questions?: AgentQuestion[] | null;
  initialTab?: DetailTab;
  onOpenAgent: (id: string) => void;
  /** Returns the shell line that resumes this agent's latest run in its harness. */
  onResume?: (agentId: string) => Promise<string>;
  onClose: () => void;
}) {
  const [resumeNote, setResumeNote] = useState<string | null>(null);
  useEffect(() => setResumeNote(null), [agent.id]);
  const resumeInHarness = async () => {
    if (!onResume) return;
    try {
      const command = await onResume(agent.id);
      const copied = await copyText(command);
      setResumeNote(copied ? "Command copied. Paste it in a terminal to continue this run." : "Could not copy the command.");
    } catch (e) {
      setResumeNote(e instanceof Error ? e.message : String(e));
    }
  };
  // The log comes from its store, so a streamed chunk re-renders this sheet only.
  const logs = useStoreField(agentLogStore, "entries");
  const [tab, setTab] = useState<DetailTab>(initialTab);
  // Another agent opened in the same sheet starts on the requested tab.
  useEffect(() => setTab(initialTab), [agent.id, initialTab]);
  const totals = useMemo(() => add(steps), [steps]);
  // Context size is what the latest step was fed, cache reads included.
  const last = steps[steps.length - 1]?.usage;
  // A tool result folds into its call's card, as in the live log.
  const lines = useMemo(() => foldToolLines(logs), [logs]);
  const asking = useMemo(() => askingAgents(questions), [questions]);
  const phase = agentPhase(agent, agents, asking);
  const deps = useMemo(() => resolveDeps(agent, agents), [agent, agents]);
  const blocking = useMemo(() => new Set(blockers(agent, agents).map((a) => a.id)), [agent, agents]);
  const children = useMemo(() => agents.filter((a) => a.parentAgentId === agent.id), [agent.id, agents]);
  const parent = agent.parentAgentId ? agents.find((a) => a.id === agent.parentAgentId) : undefined;
  const lastLine = lines.length > 0 ? lines[lines.length - 1] : null;
  const now = lastLine ? actionText(lastLine) : "";
  const stopped = agentStopped(agent.status);

  // Esc closes the sheet unless something inside already handled it.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !e.defaultPrevented && !e.isComposing) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  // Focus moves into the sheet while it is open and back to where it came
  // from (an agent row, a graph node) when it closes.
  const sheetRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const back = document.activeElement as HTMLElement | null;
    sheetRef.current?.focus();
    return () => back?.focus?.();
  }, []);

  // Tail-follow: the output keeps the newest line in view until the user
  // scrolls up, which pauses it; the pill (or scrolling back down) resumes.
  const bodyRef = useRef<HTMLDivElement>(null);
  const [following, setFollowing] = useState(true);
  const [shown, setShown] = useState(OUTPUT_WINDOW);
  useLayoutEffect(() => {
    const el = bodyRef.current;
    if (tab === "output" && following && el) el.scrollTop = el.scrollHeight;
  }, [lines, tab, following]);
  useEffect(() => {
    setFollowing(true);
    setShown(OUTPUT_WINDOW);
  }, [agent.id, tab]);
  const onScroll = () => {
    const el = bodyRef.current;
    if (!el || tab !== "output") return;
    const atEnd = el.scrollHeight - el.scrollTop - el.clientHeight < TAIL_SLACK;
    if (atEnd !== following) setFollowing(atEnd);
  };
  const hidden = Math.max(0, lines.length - shown);

  const name = agentName(agent, roleNames);
  const agentLink = (a: AgentLike) => (
    <button key={a.id} type="button" className="ad-link" onClick={() => onOpenAgent(a.id)}>
      <PhaseGlyph phase={agentPhase(a, agents, asking)} />
      <span className="ad-link-name">{agentName(a, roleNames)}</span>
      <span className="ad-link-task">{a.task}</span>
    </button>
  );

  return (
    <div className="overlay" onClick={onClose}>
      <div
        ref={sheetRef}
        tabIndex={-1}
        className="sheet wide agent-sheet"
        role="dialog"
        aria-modal="true"
        aria-label={`Agent ${name}`}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="sheet-head ad-head">
          <div className="ad-title">
            <div className="ad-title-row">
              <PhaseBadge phase={phase} />
              <strong>{name}</strong>
              <AgentElapsed phase={phase} since={workStart(agent, agents)} endedAt={agent.endedAt} />
            </div>
            <span className="detail-sub">
              {roleName} · {agent.harness}
              {agent.model ? ` · ${agent.model}` : ""}
              {parent ? ` · from ${agentName(parent, roleNames)}` : ""}
            </span>
            {agent.harnessSession && onResume && (
              <span className="ad-resume">
                <button
                  type="button"
                  onClick={resumeInHarness}
                  title="Copies a shell command that continues this run in its harness CLI"
                >
                  Resume in harness
                </button>
                {resumeNote && <span role="status">{resumeNote}</span>}
              </span>
            )}
          </div>
          <button onClick={onClose} aria-label="Close agent details">
            Close
          </button>
        </div>
        <div className="ad-summary">
          <span>
            <b>{fmt(totals.inputTokens + totals.outputTokens)}</b> tokens
          </span>
          <span>
            <b>{money(totals.costUsd)}</b>
          </span>
          <span>
            <b>{last ? fmt(last.inputTokens + last.cachedTokens) : "0"}</b> context
          </span>
          <span>
            <b>{totals.toolUses}</b> tools
          </span>
          <span>
            <b>{steps.length}</b> steps
          </span>
        </div>
        <div className={`ad-now ag-${phase}`} title={now || agent.task}>
          <span className="ad-now-k">
            {phase === "running" ? "Now" : phase === "waiting" ? "Asking" : phase === "blocked" ? "Waiting on" : "Last"}
          </span>
          <span className="ad-now-v">
            {phase === "blocked"
              ? deps.filter((d) => blocking.has(d.id)).map((d) => agentName(d, roleNames)).join(", ")
              : phase === "waiting"
                ? "A question for you is open in Chat"
                : now || (phase === "queued" ? "Not started yet" : "No output")}
          </span>
        </div>
        <div className="ad-tabs" role="tablist" aria-label="Agent details">
          {TABS.map((t) => (
            <button
              key={t.id}
              type="button"
              role="tab"
              aria-selected={tab === t.id}
              className={`ad-tab${tab === t.id ? " on" : ""}`}
              onClick={() => setTab(t.id)}
            >
              {t.label}
              {t.id === "output" && lines.length > 0 && <span className="pill">{lines.length}</span>}
              {t.id === "tasks" && deps.length + children.length > 0 && (
                <span className="pill">{deps.length + children.length}</span>
              )}
            </button>
          ))}
        </div>

        <div className="sheet-body ad-body" ref={bodyRef} onScroll={onScroll} role="tabpanel">
          {tab === "output" && (
            <div className="ad-output">
              {hidden > 0 && (
                <button type="button" className="ad-earlier" onClick={() => setShown((n) => n + OUTPUT_WINDOW * 2)}>
                  Show {Math.min(hidden, OUTPUT_WINDOW * 2)} earlier lines
                </button>
              )}
              {logs.length === 0 && <div className="log-line raw">No output yet…</div>}
              {lines.slice(hidden).map((l, i) =>
                l.tool ? (
                  <ToolCard key={`tool:${l.tool.id}`} line={l} stopped={stopped} />
                ) : (
                  <div key={hidden + i} className={`log-line ${l.kind}`}>
                    {l.text}
                  </div>
                ),
              )}
            </div>
          )}

          {tab === "tasks" && (
            <>
              <section className="set-section">
                <h4>Mission</h4>
                <div className="md mission">{agent.task}</div>
              </section>
              {deps.length > 0 && (
                <section className="set-section">
                  <h4>
                    Depends on
                    {blocking.size > 0 && <span className="ad-blocked"> · blocked by {blocking.size}</span>}
                  </h4>
                  <div className="ad-links">
                    {deps.map(agentLink)}
                  </div>
                </section>
              )}
              {children.length > 0 && (
                <section className="set-section">
                  <h4>Dispatched</h4>
                  <div className="ad-links">
                    {children.map(agentLink)}
                  </div>
                </section>
              )}
            </>
          )}

          {tab === "usage" && (
            <>
              <section className="set-section">
                <div className="usage-grid">
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
                    <div className="k">Cost</div>
                    <div className="v">{money(totals.costUsd)}</div>
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
            </>
          )}
        </div>
        {tab === "output" && !following && (
          <button
            type="button"
            className="ad-resume"
            onClick={() => {
              setFollowing(true);
            }}
          >
            {phaseLiveLabel(phase)}
          </button>
        )}
      </div>
    </div>
  );
}

const phaseLiveLabel = (phase: string) =>
  phase === "running" || phase === "waiting" ? "Paused · follow live output ↓" : "Jump to end ↓";
