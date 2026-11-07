import { useMemo, useState } from "react";
import {
  LAYER_H,
  NODE_H,
  NODE_W,
  layoutGraph,
  type GraphAgent,
  type GraphEdge,
  type GraphMailboxMessage,
  type GraphNode,
} from "../lib/graphLayout";
import { fmtTokens, totalTokens, type UsageEntry } from "../lib/usage";

/** clip shortens a label for the fixed node width (full text lives in the
 * tooltip). */
const clip = (s: string, max: number) => (s.length > max ? `${s.slice(0, max - 1)}…` : s);

/** Legacy mailbox entries carry a missing or zero time; both render blank. */
const fmtTime = (at?: string) => {
  if (!at) return "";
  const t = new Date(at);
  return Number.isNaN(t.getTime()) || t.getFullYear() < 2000
    ? ""
    : t.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
};

function edgePath(a: GraphNode, b: GraphNode, kind: GraphEdge["kind"]) {
  // Message edges bow slightly sideways when crossing layers so they don't
  // hide the spawn edge underneath.
  const dx = kind === "message" ? 12 : 0;
  if (Math.abs(a.y - b.y) >= LAYER_H / 2) {
    // Parent → child: bottom of a into the top of b, a cubic with vertical
    // handles. The badge sits at the curve's t=0.5 point.
    const x1 = a.x + NODE_W / 2 + dx;
    const x2 = b.x + NODE_W / 2 + dx;
    const y1 = a.y + NODE_H;
    const y2 = b.y;
    const mid = (y1 + y2) / 2;
    return {
      d: `M ${x1} ${y1} C ${x1} ${mid}, ${x2} ${mid}, ${x2} ${y2}`,
      mid: { x: (x1 + x2) / 2, y: mid },
    };
  }
  // Same layer: hop over the row, bowing above the nodes; arrows keep the
  // from → to direction regardless of left/right order.
  const right = b.x >= a.x;
  const x1 = right ? a.x + NODE_W : a.x;
  const x2 = right ? b.x : b.x + NODE_W;
  const y = a.y + NODE_H / 2;
  const cy = y - (kind === "message" ? 34 : 22);
  return {
    d: `M ${x1} ${y} Q ${(x1 + x2) / 2} ${cy}, ${x2} ${y}`,
    mid: { x: (x1 + x2) / 2, y: (y + cy) / 2 },
  };
}

/**
 * GraphView renders the agent team as a layered graph in the aside: solid
 * curves for spawns, dashed for task dependencies, and one directed path per
 * mailbox pair. Clicking a node opens the agent sheet; clicking a message edge
 * lists that pair's messages below the graph. It renders from the thread state
 * the App already holds — no fetching of its own.
 */
export function GraphView({
  agents,
  agentOrder,
  mailbox,
  usage,
  onNodeClick,
}: {
  agents: Record<string, GraphAgent> | null;
  agentOrder: string[] | null;
  mailbox: GraphMailboxMessage[];
  usage?: Record<string, UsageEntry> | null;
  onNodeClick: (agentId: string) => void;
}) {
  const [selected, setSelected] = useState<{ fromId: string; toId: string } | null>(null);

  const usageTotals = useMemo(() => {
    const out: Record<string, number> = {};
    for (const [id, entry] of Object.entries(usage ?? {})) out[id] = totalTokens(entry.usage);
    return out;
  }, [usage]);

  const graph = useMemo(
    () => layoutGraph(agents, agentOrder, mailbox, usageTotals),
    [agents, agentOrder, mailbox, usageTotals],
  );
  const nodeByKey = useMemo(() => new Map(graph.nodes.map((n) => [n.key, n])), [graph]);

  const nameOf = (id: string) => agents?.[id]?.name || agents?.[id]?.role || id;
  const isEdgeSelected = (e: GraphEdge) =>
    !!selected && e.kind === "message" && e.fromId === selected.fromId && e.toId === selected.toId;
  // With a pair selected, its endpoints stay lit and the rest recede.
  const relatedKeys = useMemo(() => {
    if (!selected) return null;
    const edge = graph.edges.find(
      (e) => e.kind === "message" && e.fromId === selected.fromId && e.toId === selected.toId,
    );
    return edge ? new Set([edge.from, edge.to]) : null;
  }, [selected, graph]);
  const selectedMsgs = useMemo(
    () =>
      selected ? mailbox.filter((m) => m.from === selected.fromId && m.to === selected.toId) : [],
    [selected, mailbox],
  );

  if (graph.nodes.length === 0) return <div className="empty">No agents yet</div>;

  return (
    <>
      <div className="gv-legend">
        <span>
          <i className="gv-lg spawn" /> spawn
        </span>
        <span>
          <i className="gv-lg depends" /> depends
        </span>
        <span>
          <i className="gv-lg message" /> messages
        </span>
      </div>
      <div className="gv-canvas">
        <svg
          className="gv-svg"
          width={graph.width}
          height={graph.height}
          viewBox={`0 0 ${graph.width} ${graph.height}`}
          role="img"
          aria-label="agent team graph"
        >
          <defs>
            <marker
              id="gv-arrow"
              viewBox="0 0 10 10"
              refX="8.5"
              refY="5"
              markerWidth="6.5"
              markerHeight="6.5"
              orient="auto-start-reverse"
            >
              <path d="M0 1 L9 5 L0 9 Z" style={{ fill: "var(--muted)" }} />
            </marker>
            <marker
              id="gv-arrow-hi"
              viewBox="0 0 10 10"
              refX="8.5"
              refY="5"
              markerWidth="6.5"
              markerHeight="6.5"
              orient="auto-start-reverse"
            >
              <path d="M0 1 L9 5 L0 9 Z" style={{ fill: "var(--accent)" }} />
            </marker>
          </defs>
          {graph.edges.map((e) => {
            const from = nodeByKey.get(e.from);
            const to = nodeByKey.get(e.to);
            if (!from || !to) return null;
            const path = edgePath(from, to, e.kind);
            const on = isEdgeSelected(e);
            const dim =
              relatedKeys != null &&
              (e.kind === "message" ? !on : !(relatedKeys.has(e.from) || relatedKeys.has(e.to)));
            if (e.kind === "message") {
              return (
                <g key={e.key} className={`gv-pair${dim ? " dim" : ""}`}>
                  <path
                    className={`gv-edge message${on ? " on" : ""}`}
                    d={path.d}
                    markerEnd={`url(#${on ? "gv-arrow-hi" : "gv-arrow"})`}
                  />
                  <path
                    className="gv-hit"
                    d={path.d}
                    onClick={() => setSelected(on ? null : { fromId: e.fromId, toId: e.toId })}
                  />
                  <g className="gv-badge" transform={`translate(${path.mid.x}, ${path.mid.y})`}>
                    <circle r={8} />
                    <text dy={3}>{e.count}</text>
                  </g>
                </g>
              );
            }
            return (
              <path
                key={e.key}
                className={`gv-edge ${e.kind}${dim ? " dim" : ""}`}
                d={path.d}
                markerEnd={`url(#gv-arrow)`}
              />
            );
          })}
          {graph.nodes.map((n) => {
            const dim = relatedKeys != null && !relatedKeys.has(n.key);
            return (
              <g
                key={n.key}
                className={`gv-node${dim ? " dim" : ""}`}
                transform={`translate(${n.x}, ${n.y})`}
                onClick={() => onNodeClick(n.id)}
              >
                <title>
                  {n.name}
                  {n.usage != null ? ` — ${fmtTokens(n.usage)} tok` : ""}{"\n"}
                  {n.task}
                </title>
                <rect width={NODE_W} height={NODE_H} rx={9} />
                <circle className={`gv-dot ${n.status}`} cx={12} cy={NODE_H / 2} r={3.5} />
                <text className="gv-name" x={22} y={18}>
                  {clip(n.name, 16)}
                </text>
                <text className="gv-tok" x={NODE_W - 8} y={18} textAnchor="end">
                  {n.usage != null ? `${fmtTokens(n.usage)} tok` : ""}
                </text>
                <text className="gv-role" x={22} y={31}>
                  {clip(n.role, 14)}
                </text>
              </g>
            );
          })}
        </svg>
      </div>
      {selected && (
        <div className="gv-msgs">
          <div className="gv-msgs-head">
            <strong>
              {nameOf(selected.fromId)} → {nameOf(selected.toId)}
            </strong>
            <span className="pill">
              {selectedMsgs.length} message{selectedMsgs.length === 1 ? "" : "s"}
            </span>
            <button onClick={() => setSelected(null)}>Close</button>
          </div>
          {selectedMsgs.length === 0 && (
            <div className="gv-msg-meta">No messages carry timestamps yet…</div>
          )}
          {selectedMsgs.map((m, i) => (
            <div className="gv-msg" key={i}>
              <div className="gv-msg-meta">
                <span className="gv-msg-kind">{m.kind}</span>
                {fmtTime(m.at) && <span>{fmtTime(m.at)}</span>}
              </div>
              <div className="gv-msg-body">{m.body}</div>
            </div>
          ))}
        </div>
      )}
    </>
  );
}
