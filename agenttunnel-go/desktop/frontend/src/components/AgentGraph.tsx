import { useEffect, useMemo, useRef, useState } from "react";
import {
  computeGraph,
  conversationBetween,
  conversationKey,
  type MailboxMessage,
} from "../lib/graph";
import {
  markersOf,
  reduceGraph,
  seekMarker,
  stateAt,
  toLayoutInput,
  turnRanges,
  type GraphState,
} from "../lib/graphEvents";
import { useGraphFeed } from "../lib/useGraphFeed";
import type { Rpc } from "../lib/rpc";
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

/** Replay pacing: the gap between two events is played at its real length,
 * capped so an idle hour does not stall playback, and never faster than
 * MIN_STEP_MS so bursts stay readable. */
const MAX_GAP_MS = 1500;
const MIN_STEP_MS = 40;
const SPEEDS = [1, 2, 4] as const;

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
const ms = (iso: string) => Date.parse(iso);

/**
 * AgentGraph renders the thread as a spawn tree: agent nodes layered by
 * depth, structural spawn edges, dashed task dependencies, and one accent
 * arrow per ordered agent pair that exchanged messages. Clicking a node
 * opens the agent drill-in; clicking a message arrow opens that pair's
 * conversation in both directions.
 *
 * The graph is built from the thread's event log, not from thread state:
 * live frames fold in as they arrive, and the playback bar folds a prefix of
 * the same log to replay a turn or the whole thread. Thread state still
 * supplies full message bodies and is the fallback until the log loads.
 */
export function AgentGraph({
  rpc,
  threadId,
  agents,
  roleNames,
  usage,
  mailbox,
  peerMailbox,
  onOpenAgent,
}: {
  rpc: Rpc | null;
  threadId: string;
  agents: GraphAgent[];
  roleNames: Record<string, string>;
  usage: Record<string, { usage: TokenUsage }> | null;
  mailbox: MailboxMessage[] | null;
  peerMailbox: boolean;
  onOpenAgent: (id: string) => void;
}) {
  const feed = useGraphFeed(rpc, threadId);
  const { events, checkpoints } = feed;
  const total = events.length;
  const [conversation, setConversation] = useState<{ a: string; b: string } | null>(null);
  // Playback: `cursor` is how many events are applied; null means live.
  // `turn` bounds playback to one user turn; null means the whole thread.
  const [cursor, setCursor] = useState<number | null>(null);
  const [turn, setTurn] = useState<number | null>(null);
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeed] = useState<(typeof SPEEDS)[number]>(1);

  const ranges = useMemo(() => turnRanges(events), [events]);
  const markers = useMemo(() => markersOf(events), [events]);
  const range = turn === null ? null : (ranges.find((r) => r.turn === turn) ?? null);
  const lo = range?.start ?? 0;
  const hi = range?.end ?? total;
  const replay = cursor !== null && feed.loaded && total > 0;
  const applied = replay ? Math.min(Math.max(cursor, lo), hi) : total;

  // The last replayed state. Stepping forward by one applies only that event;
  // any other move (scrub, jump, turn switch) restarts from the nearest
  // checkpoint. Live mode uses the state the feed already folded.
  const replayRef = useRef<{ at: number; state: GraphState } | null>(null);
  const view = useMemo(() => {
    if (!replay) {
      replayRef.current = null;
      return feed.state;
    }
    const prev = replayRef.current;
    let state: GraphState;
    if (prev && applied === prev.at + 1) state = reduceGraph(prev.state, events[applied - 1]);
    else if (prev && applied === prev.at) state = prev.state;
    else state = stateAt(events, checkpoints, applied);
    replayRef.current = { at: applied, state };
    return state;
  }, [replay, events, checkpoints, applied, feed.state]);
  const folded = useMemo(() => (feed.loaded ? toLayoutInput(view) : null), [feed.loaded, view]);
  const members = folded?.agents ?? agents;

  // Layout is pure geometry over the folded state and reruns only when that
  // state changes, never per animation frame.
  const layout = useMemo(
    () =>
      computeGraph({
        agents: folded?.agents ?? agents,
        roleNames,
        usage: folded ? folded.usage : usage,
        mailbox: folded ? folded.mailbox : mailbox,
      }),
    [folded, agents, roleNames, usage, mailbox],
  );
  const byId = useMemo(() => new Map(members.map((a) => [a.id, a])), [members]);
  const nameOf = (id: string) => {
    const a = byId.get(id);
    return a ? a.name || roleNames[a.role] || a.role : id;
  };

  // The sheet reads the folded messages, so replay hides anything after the
  // cursor by seq. Thread state's mailbox is capped and may lack older
  // messages, so it supplies the full body when it has one; otherwise the
  // clipped folded body is shown.
  const fullBodies = useMemo(() => {
    const byKey = new Map<string, string>();
    for (const m of mailbox ?? []) {
      const k = `${m.from}\u0000${m.to}\u0000${m.at}`;
      if (!byKey.has(k)) byKey.set(k, m.body);
    }
    return byKey;
  }, [mailbox]);
  const talk = useMemo(() => {
    if (!conversation) return [];
    if (!feed.loaded) return conversationBetween(mailbox, conversation.a, conversation.b);
    return conversationBetween(view.messages, conversation.a, conversation.b).map((m) => ({
      ...m,
      body: fullBodies.get(`${m.from}\u0000${m.to}\u0000${m.at}`) ?? m.body,
    }));
  }, [conversation, feed.loaded, mailbox, view, fullBodies]);
  // A thread switch or a vanished pair drops the selection so the sheet
  // cannot pop back open when an unrelated pair reappears.
  useEffect(() => {
    if (conversation && talk.length === 0) setConversation(null);
  }, [conversation, talk]);

  // Playback: one timer per step, paced by the real gap between events.
  const running = playing && replay && applied < hi;
  useEffect(() => {
    if (!running) return;
    const gap = applied > 0 && applied < total ? ms(events[applied].at) - ms(events[applied - 1].at) : 0;
    const wait = Math.max(MIN_STEP_MS, Math.min(Number.isFinite(gap) ? gap : 0, MAX_GAP_MS)) / speed;
    const t = window.setTimeout(() => {
      const next = applied + 1;
      setCursor(next);
      // Reaching the bound ends playback. Stopping here, rather than leaving
      // `playing` set, keeps later live events from restarting it.
      if (next >= hi) setPlaying(false);
    }, wait);
    return () => window.clearTimeout(t);
  }, [running, applied, hi, speed, events, total]);

  // Esc closes the conversation sheet, the way the context menus close.
  useEffect(() => {
    if (!conversation) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setConversation(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [conversation]);

  const togglePlay = () => {
    if (running) {
      setPlaying(false);
      return;
    }
    // Resume from the cursor, or restart the bound when it has finished.
    const start = replay && applied < hi ? applied : lo;
    setCursor(start);
    setPlaying(true);
  };
  const goLive = () => {
    setPlaying(false);
    setTurn(null);
    setCursor(null);
  };
  const scrub = (value: number) => {
    setPlaying(false);
    setCursor(value);
  };
  // A marker or prev/next lands the cursor on the event's position, so the
  // event itself is applied.
  const jump = (pos: number) => {
    setPlaying(false);
    setCursor(pos);
  };
  const prevMarker = seekMarker(markers, applied, -1, lo, hi);
  const nextMarker = seekMarker(markers, applied, 1, lo, hi);
  const visibleMarkers = markers.filter((m) => m.index >= lo && m.index < hi);
  const pickTurn = (value: string) => {
    setPlaying(false);
    if (value === "all") {
      setTurn(null);
      return;
    }
    const r = ranges.find((x) => x.turn === Number(value));
    if (!r) return;
    setTurn(r.turn);
    setCursor(r.start);
  };

  if (members.length === 0) {
    return (
      <div className="graph-scroll">
        <div className="empty">
          No agents yet. Describe a task in Chat and the team appears here.
        </div>
      </div>
    );
  }

  const messageCount = (folded?.mailbox ?? mailbox ?? []).length;
  // Both arrows of the selected pair light up while its sheet is open.
  const onPair = (from: string, to: string) =>
    !!conversation && conversationKey(from, to) === conversationKey(conversation.a, conversation.b);
  // The newest message's edge gets one traveling pulse. The key is the seq,
  // so the pulse mounts (and animates) once per new message.
  const pulse = view.lastMessage;
  const pulseEdge = pulse
    ? layout.messageEdges.find((e) => e.from === pulse.from && e.to === pulse.to)
    : undefined;

  return (
    <div className="graph-scroll">
      {feed.loaded && total > 0 && (
        <div className="graph-bar" role="group" aria-label="Graph playback">
          <button
            className={`graph-live ${replay ? "" : "on"}`}
            onClick={goLive}
            aria-pressed={!replay}
          >
            Live
          </button>
          <button
            className="graph-play"
            onClick={togglePlay}
            aria-label={running ? "Pause replay" : "Play replay"}
          >
            {running ? "Pause" : "Play"}
          </button>
          <select
            aria-label="Turn"
            value={turn === null ? "all" : String(turn)}
            onChange={(e) => pickTurn(e.target.value)}
          >
            <option value="all">All turns</option>
            {ranges.map((r) => (
              <option key={r.turn} value={r.turn}>
                Turn {r.turn}
              </option>
            ))}
          </select>
          <button
            className="graph-marker-step"
            onClick={() => prevMarker !== null && jump(prevMarker)}
            disabled={prevMarker === null}
            aria-label="Previous marker"
            title="Previous marker"
          >
            ‹
          </button>
          <div className="graph-track">
            <input
              className="graph-scrub"
              type="range"
              aria-label="Replay position"
              min={lo}
              max={hi}
              step={1}
              value={applied}
              onChange={(e) => scrub(Number(e.target.value))}
            />
            {visibleMarkers.map((m) => (
              <button
                key={m.index}
                className={`graph-mark ${m.kind}`}
                style={{ left: `${((m.index + 1 - lo) / Math.max(hi - lo, 1)) * 100}%` }}
                onClick={() => jump(m.index + 1)}
                aria-label={`${m.label}, event ${m.index + 1}`}
                title={`${m.label} · event ${m.index + 1}`}
              />
            ))}
          </div>
          <button
            className="graph-marker-step"
            onClick={() => nextMarker !== null && jump(nextMarker)}
            disabled={nextMarker === null}
            aria-label="Next marker"
            title="Next marker"
          >
            ›
          </button>
          <div className="graph-speed" role="group" aria-label="Replay speed">
            {SPEEDS.map((s) => (
              <button
                key={s}
                className={speed === s ? "on" : ""}
                aria-pressed={speed === s}
                onClick={() => setSpeed(s)}
              >
                {s}×
              </button>
            ))}
          </div>
          <span className="graph-clock">
            {replay ? (
              <>
                {applied} / {total}
                {applied > 0 && <span title={fullTime(events[applied - 1].at)}> · {fmtTime(events[applied - 1].at)}</span>}
              </>
            ) : (
              "live"
            )}
          </span>
        </div>
      )}
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
                role="button"
                tabIndex={0}
                aria-label={`${nameOf(e.from)} to ${nameOf(e.to)}, ${e.count} message${e.count === 1 ? "" : "s"}`}
                onClick={() => setConversation({ a: e.from, b: e.to })}
                onKeyDown={(ev) => {
                  if (ev.key === "Enter" || ev.key === " ") setConversation({ a: e.from, b: e.to });
                }}
              />
            </g>
          ))}
          {pulse && pulseEdge && (
            <path
              key={`pulse-${pulse.seq}`}
              className="graph-pulse"
              d={pulseEdge.path}
              pathLength={1}
            />
          )}
        </svg>
        {layout.nodes.map((n) => (
          <button
            key={n.id}
            className="graph-node enter"
            style={{ left: n.x, top: n.y, width: n.width, height: n.height }}
            onClick={() => onOpenAgent(n.id)}
            aria-label={`${n.name} (${n.role}) — open details`}
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
                    <span className="graph-msg-kind">{m.kind || "message"}</span>
                    <span className="graph-msg-from">
                      {nameOf(m.from)} → {nameOf(m.to)}
                    </span>
                    {fullTime(m.at) && (
                      <span className="graph-msg-time" aria-label={fullTime(m.at)}>
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
