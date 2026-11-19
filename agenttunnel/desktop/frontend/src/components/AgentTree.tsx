import { memo, useCallback, useEffect, useMemo, type KeyboardEvent } from "react";
import { agentName, agentTree, PHASE_LABEL, type TreeAgent, type TreeRow } from "../lib/agentTree";
import { setAgentFocus, useFocusedAgent, useLastAction } from "../lib/agentLive";
import type { AgentQuestion } from "../lib/questions";
import { fmtCost, fmtTokens, totalTokens, type TokenUsage } from "../lib/usage";
import { AgentElapsed, PhaseGlyph } from "./AgentStatus";

export type TreeAgentFull = TreeAgent & { task: string; harness: string; model: string; pipelineId?: string };

/** Which detail tab an open request lands on. */
export type DetailTab = "output" | "tasks" | "usage";

type RowProps = {
  row: TreeRow<TreeAgentFull>;
  name: string;
  roleName: string;
  usage?: TokenUsage;
  selected: boolean;
  /** The row the keyboard rests on (Tab lands here). */
  cursor: boolean;
  onPick: (id: string) => void;
  onOpen: (id: string, tab?: DetailTab) => void;
  onStop: (id: string) => void;
  /** Names of the agents blocking this one. */
  blockedBy: string;
};

/** The second line: what the agent is doing now, or why it is not. */
function Activity({ row, blockedBy }: { row: TreeRow<TreeAgentFull>; blockedBy: string }) {
  const last = useLastAction(row.agent.id);
  let text: string;
  let cls = "ag-act";
  switch (row.phase) {
    case "waiting":
      text = "Waiting on your answer";
      cls += " attn";
      break;
    case "blocked":
      text = `After ${blockedBy}`;
      break;
    case "queued":
      text = row.agent.task;
      break;
    default:
      text = last || row.agent.task;
  }
  return (
    <div className={cls} title={text}>
      {text}
    </div>
  );
}

const AgentRow = memo(function AgentRow({
  row,
  name,
  roleName,
  usage,
  selected,
  cursor,
  onPick,
  onOpen,
  onStop,
  blockedBy,
}: RowProps) {
  const a = row.agent;
  const running = a.status === "running";
  return (
    <div
      id={`ag-row-${a.id}`}
      role="treeitem"
      aria-level={row.depth + 1}
      aria-selected={selected}
      aria-label={`${name}, ${PHASE_LABEL[row.phase]}`}
      tabIndex={cursor ? 0 : -1}
      className={`ag-row ag-${row.phase}${selected ? " on" : ""}`}
      style={row.depth > 0 ? { paddingLeft: 8 + row.depth * 14 } : undefined}
      onClick={() => {
        onPick(a.id);
        onOpen(a.id);
      }}
    >
      {row.depth > 0 && <span className="ag-guide" style={{ left: row.depth * 14 - 2 }} aria-hidden="true" />}
      <PhaseGlyph phase={row.phase} />
      <div className="ag-main">
        <div className="ag-line">
          <span className="ag-name">{name}</span>
          {roleName !== name && <span className="ag-role">{roleName}</span>}
          <span className="ag-spacer" />
          <AgentElapsed phase={row.phase} since={row.since} endedAt={a.endedAt} />
        </div>
        <Activity row={row} blockedBy={blockedBy} />
        <div className="ag-meta">
          <span className="ag-runtime" title={`${a.harness}${a.model ? ` · ${a.model}` : ""}`}>
            {a.harness}
            {a.model ? ` · ${a.model}` : ""}
          </span>
          {usage && (
            <span className="ag-usage">
              {fmtTokens(totalTokens(usage))} tok
              {usage.costUsd > 0 ? ` · ${fmtCost(usage.costUsd)}` : ""}
            </span>
          )}
          {running && (
            <button
              className="stop-btn mini"
              title="Stop this agent — the rest of the turn keeps running"
              tabIndex={-1}
              onClick={(e) => {
                e.stopPropagation();
                onStop(a.id);
              }}
            >
              Stop
            </button>
          )}
        </div>
      </div>
    </div>
  );
});

/**
 * AgentTree is the Agents panel's live team: each lead with the agents it
 * dispatched nested beneath, each row showing role, runtime, an honest phase,
 * how long it has worked, its usage, and its newest action. Everything comes
 * from the folded thread state and the live log already on the client.
 *
 * Keys: Up/Down (and Home/End) move the selection, Enter opens the agent,
 * T opens its output following the tail, Esc clears the selection. The
 * selected agent is shared, so the live log and the graph follow it.
 */
export function AgentTree({
  threadId,
  agents,
  questions,
  usage,
  roleNames,
  onOpen,
  onStop,
}: {
  threadId: string;
  agents: TreeAgentFull[];
  questions?: AgentQuestion[] | null;
  usage: Record<string, { usage: TokenUsage }> | null | undefined;
  roleNames: Record<string, string>;
  onOpen: (id: string, tab?: DetailTab) => void;
  onStop: (id: string) => void;
}) {
  const rows = useMemo(() => agentTree(agents, questions), [agents, questions]);
  const focused = useFocusedAgent(threadId);
  const selected = focused && rows.some((r) => r.agent.id === focused) ? focused : null;
  // Without a selection the keyboard cursor (the row Tab lands on) is the first row.
  const current = selected ?? rows[0]?.agent.id ?? null;

  const pick = useCallback((id: string) => setAgentFocus({ threadId, agentId: id }), [threadId]);

  // A selection made elsewhere (the graph) scrolls into view here.
  useEffect(() => {
    if (focused) document.getElementById(`ag-row-${focused}`)?.scrollIntoView({ block: "nearest" });
  }, [focused]);

  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.metaKey || e.ctrlKey || e.altKey || rows.length === 0) return;
    const at = Math.max(0, rows.findIndex((r) => r.agent.id === current));
    let next = -1;
    switch (e.key) {
      case "ArrowDown":
        next = Math.min(rows.length - 1, at + 1);
        break;
      case "ArrowUp":
        next = Math.max(0, at - 1);
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = rows.length - 1;
        break;
      case "Enter":
        if (current) onOpen(current);
        e.preventDefault();
        return;
      case "t":
      case "T":
        if (current) onOpen(current, "output");
        e.preventDefault();
        return;
      case "Escape":
        if (focused) {
          setAgentFocus(null);
          e.preventDefault();
          e.stopPropagation();
        }
        return;
      default:
        return;
    }
    e.preventDefault();
    const id = rows[next].agent.id;
    pick(id);
    const el = document.getElementById(`ag-row-${id}`);
    el?.focus();
  };

  if (rows.length === 0) return <p className="hint">No agents yet. Describe a task in Chat and the team appears here.</p>;

  return (
    <div
      className="ag-tree"
      role="tree"
      aria-label="Agents"
      onKeyDown={onKey}
    >
      {rows.map((r) => (
        <AgentRow
          key={r.agent.id}
          row={r}
          name={agentName(r.agent, roleNames)}
          roleName={roleNames[r.agent.role] || r.agent.role}
          usage={usage?.[r.agent.id]?.usage}
          selected={r.agent.id === selected}
          cursor={r.agent.id === current}
          onPick={pick}
          onOpen={onOpen}
          onStop={onStop}
          blockedBy={r.blockedBy.map((b) => agentName(b, roleNames)).join(", ")}
        />
      ))}
      <p className="ag-keys" aria-hidden="true">
        <kbd>↑</kbd>
        <kbd>↓</kbd> select · <kbd>↵</kbd> open · <kbd>T</kbd> tail output · <kbd>Esc</kbd> show all
      </p>
    </div>
  );
}
