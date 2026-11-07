import { useEffect, useMemo, useState } from "react";
import {
  computeGraph,
  conversationBetween,
  conversationKey,
  type MailboxMessage,
} from "../lib/graph";
import { fmtTokens, type TokenUsage } from "../lib/usage";
import { Markdown } from "./Markdown";

type GraphAgent = {
  id: string;
  parentAgentId?: string;
  name?: string;
  role: string;
  status: string;
  task?: string;
  dependsOn?: string[];
};

/** Message timestamps follow the usage table: short local time, with the full
 * date available on hover. */
const fmtTime = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? ""
    : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
};
const fullTime = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString();
};

/**
 * AgentGraph renders the thread as a spawn tree: agent nodes layered by
 * depth, structural spawn edges, dashed task dependencies, and one accent
 * arrow per ordered agent pair that exchanged messages. Clicking a node
 * opens the agent drill-in; clicking a message arrow opens that pair's
 * conversation in both directions. It is a pure function of the thread
 * state passed in — live updates just re-render it.
 */
export function AgentGraph({
  agents,
  roleNames,
  usage,
  mailbox,
  peerMailbox,
  onOpenAgent,
}: {
  agents: GraphAgent[];
  roleNames: Record<string, string>;
  usage: Record<string, { usage: TokenUsage }> | null;
  mailbox: MailboxMessage[] | null;
  peerMailbox: boolean;
  onOpenAgent: (id: string) => void;
}) {
  const [conversation, setConversation] = useState<{ a: string; b: string } | null>(null);

  // Layout is pure geometry over the thread state and only reruns when that
  // state changes — never per frame.
  const layout = useMemo(
    () => computeGraph({ agents, roleNames, usage, mailbox }),
    [agents, roleNames, usage, mailbox],
  );
  const byId = useMemo(() => new Map(agents.map((a) => [a.id, a])), [agents]);
  const nameOf = (id: string) => {
    const a = byId.get(id);
    return a ? a.name || roleNames[a.role] || a.role : id;
  };
  // The conversation holds every message between the two agents in either
  // direction; a thread switch or a vanished pair drops the selection so the
  // sheet cannot pop back open when an unrelated pair reappears.
  const talk = useMemo(
    () => (conversation ? conversationBetween(mailbox, conversation.a, conversation.b) : []),
    [conversation, mailbox],
  );
  useEffect(() => {
    if (conversation && talk.length === 0) setConversation(null);
  }, [conversation, talk]);

  // Esc closes the conversation sheet, the way the context menus close.
  useEffect(() => {
    if (!conversation) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setConversation(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [conversation]);

  if (agents.length === 0) {
    return (
      <div className="graph-scroll">
        <div className="empty">No agents yet. Describe a task in Chat and the team appears here.</div>
      </div>
    );
  }

  const messageCount = mailbox?.length ?? 0;
  // Both arrows of the selected pair light up while its sheet is open.
  const onPair = (from: string, to: string) =>
    !!conversation && conversationKey(from, to) === conversationKey(conversation.a, conversation.b);

  return (
    <div className="graph-scroll">
      {messageCount === 0 && (
        <p className="graph-hint">
          {peerMailbox ? (
            <>
              No messages between agents yet. Subagents can write to each other with the message
              tool, e.g.{" "}
              <code>$AT_TOOL tool message --to researcher-1 --text "parser is ready"</code>.
            </>
          ) : (
            <>
              Peer chat is off for this thread — turn on the <b>Peer chat</b> toggle in the thread
              header above to let agents message each other.
            </>
          )}
        </p>
      )}
      <div className="graph-legend">
        <span>
          <i className="graph-lg spawn" /> spawn
        </span>
        <span>
          <i className="graph-lg depends" /> depends
        </span>
        <span>
          <i className="graph-lg message" /> message
        </span>
      </div>
      <div className="graph-canvas" style={{ width: layout.width, height: layout.height }}>
        <svg className="graph-edges" width={layout.width} height={layout.height}>
          <defs>
            <marker
              id="ag-arrow"
              viewBox="0 0 10 10"
              refX="9"
              refY="5"
              markerWidth="7"
              markerHeight="7"
              orient="auto"
            >
              <path d="M0,0 L10,5 L0,10 z" className="graph-arrow-head" />
            </marker>
          </defs>
          {layout.spawnEdges.map((e) => (
            <path key={e.key} className="graph-spawn" d={e.path} />
          ))}
          {layout.dependEdges.map((e) => (
            <path key={e.key} className="graph-depends" d={e.path} />
          ))}
          {layout.messageEdges.map((e) => (
            <g key={e.key} className={onPair(e.from, e.to) ? "graph-pair on" : "graph-pair"}>
              <path className="graph-msg" d={e.path} markerEnd="url(#ag-arrow)" />
              <text className="graph-msg-count" x={e.labelX} y={e.labelY} textAnchor="middle">
                {e.count === 1 ? "1 msg" : `${e.count} msgs`}
              </text>
              <path
                className="graph-hit"
                d={e.path}
                onClick={() => setConversation({ a: e.from, b: e.to })}
              />
            </g>
          ))}
        </svg>
        {layout.nodes.map((n) => (
          <button
            key={n.id}
            className="graph-node"
            style={{ left: n.x, top: n.y, width: n.width, height: n.height }}
            onClick={() => onOpenAgent(n.id)}
            title={`${n.name} (${n.role}) — open details`}
          >
            <div className="graph-node-head">
              <span className={`dot ${n.status}`} />
              <span className="graph-node-name">{n.name}</span>
              {n.tokens > 0 && <span className="graph-node-tokens">{fmtTokens(n.tokens)}</span>}
            </div>
            <div className="graph-node-role">{n.role}</div>
          </button>
        ))}
      </div>

      {conversation && talk.length > 0 && (
        <div className="overlay" onClick={() => setConversation(null)}>
          <div className="sheet wide" onClick={(e) => e.stopPropagation()}>
            <div className="sheet-head">
              <strong>
                {nameOf(conversation.a)} ↔ {nameOf(conversation.b)}
              </strong>
              <span className="pill">{talk.length}</span>
              <button onClick={() => setConversation(null)}>Close</button>
            </div>
            <div className="sheet-body">
              {talk.map((m, i) => (
                <div className="graph-msg-item" key={i}>
                  <div className="graph-msg-meta">
                    <span className="graph-msg-kind">{m.kind}</span>
                    <span className="graph-msg-from">
                      {nameOf(m.from)} → {nameOf(m.to)}
                    </span>
                    {fullTime(m.at) && (
                      <span className="graph-msg-time" title={fullTime(m.at)}>
                        {fmtTime(m.at)}
                      </span>
                    )}
                  </div>
                  <div className="graph-msg-body">
                    <Markdown text={m.body} />
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
