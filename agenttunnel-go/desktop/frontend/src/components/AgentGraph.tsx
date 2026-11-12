import { useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import {
  computeGraph,
  type GraphNode,
  conversationBetween,
  conversationKey,
  harnessTint,
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
import {
  RING_GAP,
  SNAP_GAP,
  buildGroupPrompt,
  inSameGroup,
  mergeNodes,
  nearestNode,
  pruneGroups,
  removeFromGroup,
  snapSlot,
  ungroup,
  type Group,
  type GroupMember,
} from "../lib/graphGroups";
import type { Rpc } from "../lib/rpc";
import { fmtTokens, type TokenUsage } from "../lib/usage";
import { Markdown } from "./Markdown";

type GraphAgent = {
  id: string;
  parentAgentId?: string;
  name?: string;
  role: string;
  status: string;
  harness?: string;
  model?: string;
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

/** Group header and padding around member nodes, in canvas pixels. */
const GROUP_PAD = 12;
const GROUP_HEAD = 30;

type Proximity = { from: string; to: string; snapped: boolean };
const sameProximity = (a: Proximity | null, b: Proximity | null) =>
  a === b || (!!a && !!b && a.to === b.to && a.snapped === b.snapped);

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
  onSubmitGroup,
}: {
  rpc: Rpc | null;
  threadId: string;
  agents: GraphAgent[];
  roleNames: Record<string, string>;
  usage: Record<string, { usage: TokenUsage }> | null;
  mailbox: MailboxMessage[] | null;
  peerMailbox: boolean;
  onOpenAgent: (id: string) => void;
  /** Sends a group prompt to the thread: a new turn, or a steer while one runs. */
  onSubmitGroup: (threadId: string, text: string) => void;
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
  // Nodes the user dragged, by agent id. The graph remounts per thread (keyed by
  // the App), so the positions are scoped to the thread on screen.
  const [positions, setPositions] = useState<Record<string, { x: number; y: number }>>({});
  const drag = useRef<{
    id: string;
    pointer: number;
    startX: number;
    startY: number;
    x: number;
    y: number;
    moved: boolean;
  } | null>(null);
  // Set when a drag ends, so the click that follows the release does not open details.
  const dragEnded = useRef<string | null>(null);
  // Agent groups are selection only, scoped to this mounted graph like positions.
  const [groups, setGroups] = useState<Group[]>([]);
  const [composing, setComposing] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  // The node under the dragged one. Set only when it changes, so moves that stay
  // in range do not re-render. The ref is what the drop reads.
  const [prox, setProx] = useState<Proximity | null>(null);
  const proxRef = useRef<Proximity | null>(null);
  // Nodes that just joined a group, for the one-shot merge settle.
  const [settle, setSettle] = useState<string[]>([]);

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
        positions,
      }),
    [folded, agents, roleNames, usage, mailbox, positions],
  );
  const byId = useMemo(() => new Map(members.map((a) => [a.id, a])), [members]);
  // Groups are read pruned: an agent that vanished in replay drops out of its group.
  const liveGroups = useMemo(
    () => pruneGroups(groups, new Set(members.map((a) => a.id))),
    [groups, members],
  );
  // Group boxes and the × marks on grouped nodes, measured from the layout.
  const { boxes, unlinks } = useMemo(() => {
    const at = new Map(layout.nodes.map((n) => [n.id, n]));
    const boxes = liveGroups.flatMap((g) => {
      const ns = g.members.map((id) => at.get(id)).filter((n) => !!n);
      if (ns.length < 2) return [];
      const left = Math.max(0, Math.min(...ns.map((n) => n.x)) - GROUP_PAD);
      const top = Math.max(0, Math.min(...ns.map((n) => n.y)) - GROUP_HEAD);
      const right = Math.max(...ns.map((n) => n.x + n.width)) + GROUP_PAD;
      const bottom = Math.max(...ns.map((n) => n.y + n.height)) + GROUP_PAD;
      return [{ id: g.id, count: ns.length, left, top, width: right - left, height: bottom - top }];
    });
    const unlinks = liveGroups.flatMap((g) =>
      g.members.flatMap((id) => {
        const n = at.get(id);
        // Sits on the node's top-right corner, just under the group header.
        return n ? [{ id, name: n.name, x: n.x + n.width - 9, y: n.y - 4 }] : [];
      }),
    );
    return { boxes, unlinks };
  }, [liveGroups, layout.nodes]);
  const composeGroup = liveGroups.find((g) => g.id === composing) ?? null;
  // A group that dissolves under an open composer closes it, so a later group
  // that reuses the id does not reopen the stale draft.
  useEffect(() => {
    if (composing !== null && !composeGroup) setComposing(null);
  }, [composing, composeGroup]);

  // Dragging pins a node at the pointer's offset from where the press began.
  // Positions are clamped to the canvas origin so a node cannot leave the
  // scrollable area.
  const DRAG_SLOP = 4;
  const startDrag = (e: ReactPointerEvent<HTMLButtonElement>, n: GraphNode) => {
    if (e.button !== 0) return;
    e.currentTarget.setPointerCapture(e.pointerId);
    dragEnded.current = null;
    drag.current = {
      id: n.id,
      pointer: e.pointerId,
      startX: e.clientX,
      startY: e.clientY,
      x: n.x,
      y: n.y,
      moved: false,
    };
  };
  const moveDrag = (e: ReactPointerEvent<HTMLButtonElement>) => {
    const d = drag.current;
    if (!d || e.pointerId !== d.pointer) return;
    const dx = e.clientX - d.startX;
    const dy = e.clientY - d.startY;
    if (!d.moved && Math.hypot(dx, dy) < DRAG_SLOP) return;
    d.moved = true;
    let x = Math.max(0, d.x + dx);
    let y = Math.max(0, d.y + dy);
    // Proximity to another node: rings inside RING_GAP, a magnetic snap inside SNAP_GAP.
    // A node already in the target's group gets neither: there is nothing to join.
    const self = layout.nodes.find((n) => n.id === d.id);
    const hit = self
      ? nearestNode({ id: d.id, x, y, width: self.width, height: self.height }, layout.nodes)
      : null;
    let next: Proximity | null = null;
    if (self && hit && hit.gap < RING_GAP && !inSameGroup(liveGroups, d.id, hit.target.id)) {
      const snapped = hit.gap < SNAP_GAP;
      if (snapped) ({ x, y } = snapSlot({ id: d.id, x, y, width: self.width, height: self.height }, hit.target));
      next = { from: d.id, to: hit.target.id, snapped };
    }
    setPositions((prev) => ({ ...prev, [d.id]: { x, y } }));
    if (!sameProximity(proxRef.current, next)) {
      proxRef.current = next;
      setProx(next);
    }
  };
  const endDrag = (e: ReactPointerEvent<HTMLButtonElement>) => {
    const d = drag.current;
    if (!d || e.pointerId !== d.pointer) return;
    drag.current = null;
    if (!d.moved) return;
    dragEnded.current = d.id;
    // Only a snapped drop merges; a drop in the ring zone just places the node.
    const p = proxRef.current;
    if (p?.snapped) {
      setGroups(mergeNodes(liveGroups, p.from, p.to));
      setSettle([p.from, p.to]);
    }
    proxRef.current = null;
    setProx(null);
  };
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

  // The merge settle lasts one short animation, then the class is dropped.
  useEffect(() => {
    if (settle.length === 0) return;
    const t = window.setTimeout(() => setSettle([]), 320);
    return () => window.clearTimeout(t);
  }, [settle]);

  const openComposer = (groupId: string) => {
    setComposing(groupId);
    setDraft("");
  };

  // Sends the composer's ask with each member's state and output tail. The logs
  // are fetched only now, on submit, so grouping itself costs no requests.
  const sendGroup = async () => {
    if (!composeGroup || sending) return;
    const ask = draft.trim();
    if (!ask) return;
    setSending(true);
    const logs = await Promise.all(
      composeGroup.members.map((id) =>
        rpc
          ? rpc
              .call<{ kind: string; text: string }[]>("agents.logs", { threadId, agentId: id })
              .catch(() => [])
          : Promise.resolve([]),
      ),
    );
    const prompt: GroupMember[] = composeGroup.members.flatMap((id, i) => {
      const a = byId.get(id);
      if (!a) return [];
      return [
        {
          name: a.name || roleNames[a.role] || a.role,
          role: roleNames[a.role] || a.role,
          status: a.status,
          task: a.task,
          logs: logs[i],
        },
      ];
    });
    setSending(false);
    setComposing(null);
    setDraft("");
    onSubmitGroup(threadId, buildGroupPrompt(ask, prompt));
  };

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
        {Object.keys(positions).length > 0 && (
          <button className="graph-reset" onClick={() => setPositions({})}>
            Reset layout
          </button>
        )}
      </div>
      {composeGroup && (
        <div className="graph-composer">
          <div className="graph-composer-to">
            {composeGroup.members.map((id) => (
              <span key={id} className="graph-chip">
                {nameOf(id)}
              </span>
            ))}
          </div>
          <textarea
            autoFocus
            rows={3}
            value={draft}
            placeholder="Ask this group…"
            aria-label="Group prompt"
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setComposing(null);
                setDraft("");
              } else if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                void sendGroup();
              }
            }}
          />
          <div className="graph-composer-actions">
            <span className="graph-hint-keys">⌘↵ send · Esc cancel</span>
            <button
              onClick={() => {
                setComposing(null);
                setDraft("");
              }}
            >
              Cancel
            </button>
            <button className="primary" disabled={!draft.trim() || sending} onClick={() => void sendGroup()}>
              Send
            </button>
          </div>
        </div>
      )}
      {/* A press that moves past the slop drags the node; one that does not is a click. */}
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
        {boxes.map((b) => (
          <div
            key={b.id}
            className="graph-group"
            style={{ left: b.left, top: b.top, width: b.width, height: b.height }}
          >
            <div className="graph-group-head">
              <span>Group · {b.count}</span>
              <button onClick={() => openComposer(b.id)}>Ask</button>
              <button onClick={() => setGroups(ungroup(liveGroups, b.id))}>Ungroup</button>
            </div>
          </div>
        ))}
        {layout.nodes.map((n) => {
          const tint = harnessTint(n.harness);
          return (
            <button
              key={n.id}
              className={`graph-node enter${tint ? ` ${tint}` : ""}${
                prox?.to === n.id ? " near" : ""
              }${prox?.from === n.id && prox.snapped ? " snap" : ""}${settle.includes(n.id) ? " merge" : ""}`}
            style={{ left: n.x, top: n.y, width: n.width, height: n.height }}
            onPointerDown={(e) => startDrag(e, n)}
            onPointerMove={moveDrag}
            onPointerUp={endDrag}
            onPointerCancel={endDrag}
            onClick={() => {
              if (dragEnded.current === n.id) {
                dragEnded.current = null;
                return;
              }
              onOpenAgent(n.id);
            }}
            aria-label={`${n.name} (${n.role})${n.model ? ` · ${n.model}` : ""} — open details`}
          >
            {prox?.to === n.id && (
              <span className="graph-rings" aria-hidden>
                <i />
                <i />
                <i />
              </span>
            )}
            <div className="graph-node-head">
              <span className={`dot ${n.status}`} />
              <span className="graph-node-name">{n.name}</span>
              {n.tokens > 0 && <span className="graph-node-tokens">{fmtTokens(n.tokens)}</span>}
            </div>
            <div className="graph-node-role">{n.role}</div>
            {n.model && <div className="graph-node-model">{n.model}</div>}
            </button>
          );
        })}
        {unlinks.map((u) => (
          <button
            key={`unlink-${u.id}`}
            className="graph-unlink"
            style={{ left: u.x, top: u.y }}
            onClick={() => setGroups(removeFromGroup(liveGroups, u.id))}
            aria-label={`Remove ${u.name} from group`}
            title="Remove from group"
          >
            ×
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
