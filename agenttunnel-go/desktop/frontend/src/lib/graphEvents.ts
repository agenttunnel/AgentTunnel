// Agent-graph replay state: a pure fold over the thread's graph-relevant
// events. Live mode applies each frame as it arrives; replay folds the prefix
// up to a cursor. Both go through applyEvent, so a scrubbed view and the live
// view of the same log are the same state.

import { computeGraph, type MailboxMessage } from "./graph";
import { addUsage, zeroUsage, type TokenUsage } from "./usage";

/** Bodies echoed into the feed are clipped; the full mailbox comes from
 * thread.get. Must match graphBodyMax on the daemon. */
const BODY_MAX = 280;

/** Checkpoint k is the folded state after the first k * CHECKPOINT_EVERY
 * events. A seek starts from the nearest checkpoint, so it applies at most
 * CHECKPOINT_EVERY events no matter how long the thread is. */
export const CHECKPOINT_EVERY = 200;

type Base = { seq: number; at: string };

/** Graph-relevant event, in the slim shape thread.events returns. Live frames
 * are normalized to the same shape by toGraphEvent. */
export type GraphEvent =
  | (Base & { type: "thread.message-sent"; payload: { author: string } })
  | (Base & {
      type: "agent.task-dispatched";
      payload: {
        agentId: string;
        parentAgentId?: string;
        name?: string;
        role: string;
        task: string;
        pipelineId?: string;
        dependsOn?: string[];
      };
    })
  | (Base & { type: "agent.status-changed"; payload: { agentId: string; status: string } })
  | (Base & { type: "agent.usage-recorded"; payload: { agentId: string; role: string; usage: TokenUsage } })
  | (Base & { type: "mailbox.posted"; payload: { from: string; to: string; kind: string; body: string } });

type GraphInput = Parameters<typeof computeGraph>[0];
export type GraphAgentInput = GraphInput["agents"][number];

type AgentRecord = {
  id: string;
  parentAgentId: string;
  name: string;
  role: string;
  status: string;
  task: string;
  pipelineId: string;
  dependsOn: string[];
};

/** A folded message keeps the seq it was posted at, so replay can hide
 * messages that come after the cursor by order rather than by time. */
export type GraphMessage = MailboxMessage & { seq: number };

export type GraphState = {
  /** Last applied seq; events at or below it are ignored. */
  seq: number;
  /** Insertion order is the agent order the layout stacks by. */
  agents: Map<string, AgentRecord>;
  usage: Map<string, { usage: TokenUsage }>;
  messages: GraphMessage[];
  /** The newest message, so the view can pulse its edge once. */
  lastMessage: { from: string; to: string; seq: number } | null;
};

export function emptyGraph(): GraphState {
  return { seq: 0, agents: new Map(), usage: new Map(), messages: [], lastMessage: null };
}

const clip = (s: string) => {
  const r = Array.from(s);
  return r.length <= BODY_MAX ? s : r.slice(0, BODY_MAX).join("") + "…";
};

const GRAPH_TYPES = new Set<string>([
  "thread.message-sent",
  "agent.task-dispatched",
  "agent.status-changed",
  "agent.usage-recorded",
  "mailbox.posted",
]);

/** Narrows a raw `thread.event` frame (or a thread.events row) to a graph
 * event, trimming fields the graph never reads. Returns null for other types
 * and for frames that are not for `threadId`. */
export function toGraphEvent(raw: unknown, threadId?: string): GraphEvent | null {
  const ev = raw as {
    seq?: number;
    type?: string;
    at?: string;
    threadId?: string;
    payload?: Record<string, unknown>;
  } | null;
  if (!ev || typeof ev.type !== "string" || !GRAPH_TYPES.has(ev.type)) return null;
  if (threadId !== undefined && ev.threadId !== undefined && ev.threadId !== threadId) return null;
  const base = { seq: Number(ev.seq ?? 0), at: String(ev.at ?? "") };
  const p = (ev.payload ?? {}) as Record<string, any>;
  switch (ev.type) {
    case "thread.message-sent":
      return { ...base, type: ev.type, payload: { author: String(p.author ?? "") } };
    case "agent.task-dispatched":
      return {
        ...base,
        type: ev.type,
        payload: {
          agentId: String(p.agentId ?? ""),
          parentAgentId: p.parentAgentId || undefined,
          name: p.name || undefined,
          role: String(p.role ?? ""),
          task: String(p.task ?? ""),
          pipelineId: p.pipelineId || undefined,
          dependsOn: Array.isArray(p.dependsOn) ? p.dependsOn.map(String) : undefined,
        },
      };
    case "agent.status-changed":
      return { ...base, type: ev.type, payload: { agentId: String(p.agentId ?? ""), status: String(p.status ?? "") } };
    case "agent.usage-recorded":
      return {
        ...base,
        type: ev.type,
        payload: { agentId: String(p.agentId ?? ""), role: String(p.role ?? ""), usage: p.usage as TokenUsage },
      };
    case "mailbox.posted":
      return {
        ...base,
        type: ev.type,
        payload: { from: String(p.from ?? ""), to: String(p.to ?? ""), kind: String(p.kind ?? ""), body: clip(String(p.body ?? "")) },
      };
  }
  return null;
}

/** Applies one event to a state the caller owns. Mutates `s`. Agent and usage
 * records are replaced, never edited, so a shallow copy of the Maps is a safe
 * snapshot. */
function applyEvent(s: GraphState, ev: GraphEvent) {
  s.seq = ev.seq;
  switch (ev.type) {
    case "thread.message-sent":
      return;
    case "agent.task-dispatched": {
      const p = ev.payload;
      // Re-dispatch keeps the agent's place in the order (Map.set on an
      // existing key does not move it), matching the projector.
      s.agents.set(p.agentId, {
        id: p.agentId,
        parentAgentId: p.parentAgentId ?? "",
        name: p.name ?? "",
        role: p.role,
        status: "pending",
        task: p.task,
        pipelineId: p.pipelineId ?? "",
        dependsOn: p.dependsOn ?? [],
      });
      return;
    }
    case "agent.status-changed": {
      // Ordering between dispatch and status is not guaranteed for live frames,
      // so a status for an unseen agent creates a placeholder.
      const cur = s.agents.get(ev.payload.agentId);
      s.agents.set(ev.payload.agentId, {
        id: ev.payload.agentId,
        parentAgentId: cur?.parentAgentId ?? "",
        name: cur?.name ?? "",
        role: cur?.role ?? "",
        status: ev.payload.status,
        task: cur?.task ?? "",
        pipelineId: cur?.pipelineId ?? "",
        dependsOn: cur?.dependsOn ?? [],
      });
      return;
    }
    case "agent.usage-recorded": {
      const prev = s.usage.get(ev.payload.agentId)?.usage ?? zeroUsage;
      s.usage.set(ev.payload.agentId, { usage: addUsage(prev, ev.payload.usage) });
      return;
    }
    case "mailbox.posted": {
      const p = ev.payload;
      s.messages.push({ from: p.from, to: p.to, kind: p.kind, body: p.body, at: ev.at, seq: ev.seq });
      s.lastMessage = { from: p.from, to: p.to, seq: ev.seq };
      return;
    }
  }
}

/** Copies the containers so the copy can be applied to without touching the
 * original. Agent and usage values are shared, which is safe because they are
 * replaced rather than mutated. */
function cloneGraph(s: GraphState): GraphState {
  return {
    seq: s.seq,
    agents: new Map(s.agents),
    usage: new Map(s.usage),
    messages: s.messages.slice(),
    lastMessage: s.lastMessage,
  };
}

/** Pure step: returns `state` itself when the event was already applied. */
export function reduceGraph(state: GraphState, ev: GraphEvent): GraphState {
  if (ev.seq <= state.seq) return state;
  const next = cloneGraph(state);
  applyEvent(next, ev);
  return next;
}

/** Applies events[from, to) to `s` in place, skipping any already applied. */
function applyRange(s: GraphState, events: readonly GraphEvent[], from: number, to: number): GraphState {
  for (let i = from; i < to; i++) {
    const ev = events[i];
    if (ev.seq <= s.seq) continue;
    applyEvent(s, ev);
  }
  return s;
}

/** Snapshots of the folded state every CHECKPOINT_EVERY events: entry k is the
 * state after the first k * CHECKPOINT_EVERY events, and entry 0 is empty.
 * Append-only, like the log it describes. */
export function buildCheckpoints(events: readonly GraphEvent[]): GraphState[] {
  const checkpoints = [emptyGraph()];
  const acc = emptyGraph();
  for (let i = 0; i < events.length; i++) {
    applyRange(acc, events, i, i + 1);
    if ((i + 1) % CHECKPOINT_EVERY === 0) checkpoints.push(cloneGraph(acc));
  }
  return checkpoints;
}

/** The state after the first `count` events. Starts from the nearest
 * checkpoint at or below `count`, so the cost is bounded by CHECKPOINT_EVERY
 * plus one copy, not by the prefix length. */
export function stateAt(events: readonly GraphEvent[], checkpoints: readonly GraphState[], count: number): GraphState {
  const n = Math.min(Math.max(count, 0), events.length);
  const k = Math.min(Math.floor(n / CHECKPOINT_EVERY), checkpoints.length - 1);
  return applyRange(cloneGraph(checkpoints[k]), events, k * CHECKPOINT_EVERY, n);
}

/** Index ranges per user turn. Turn k runs from its user message (or from the
 * start of the log for the first) up to the next user message. `end` is
 * exclusive and is the number of events to apply to see the turn finish. */
export type TurnRange = { turn: number; start: number; end: number };

export function turnRanges(events: readonly GraphEvent[]): TurnRange[] {
  if (events.length === 0) return [];
  const starts = [0];
  events.forEach((ev, i) => {
    if (i > 0 && ev.type === "thread.message-sent" && ev.payload.author === "user") starts.push(i);
  });
  return starts.map((start, k) => ({
    turn: k + 1,
    start,
    end: starts[k + 1] ?? events.length,
  }));
}

export type MarkerKind = "turn" | "spawn" | "message" | "done";

/** A meaningful event on the scrubber. `index` is the event's position in the
 * log; applying it means a cursor of index + 1. */
export type Marker = { index: number; kind: MarkerKind; label: string };

const TERMINAL = new Set<string>(["succeeded", "failed", "cancelled"]);

export function markersOf(events: readonly GraphEvent[]): Marker[] {
  const out: Marker[] = [];
  events.forEach((ev, index) => {
    switch (ev.type) {
      case "thread.message-sent":
        if (ev.payload.author === "user") out.push({ index, kind: "turn", label: "Turn start" });
        return;
      case "agent.task-dispatched": {
        const p = ev.payload;
        out.push({ index, kind: "spawn", label: `Spawn ${p.name || p.role || p.agentId}` });
        return;
      }
      case "mailbox.posted": {
        const p = ev.payload;
        out.push({ index, kind: "message", label: `Message ${p.from} → ${p.to}` });
        return;
      }
      case "agent.status-changed":
        if (TERMINAL.has(ev.payload.status)) {
          out.push({ index, kind: "done", label: `${ev.payload.agentId} ${ev.payload.status}` });
        }
        return;
    }
  });
  return out;
}

/** The cursor that lands on the nearest marker strictly before (dir -1) or
 * after (dir 1) `at`, restricted to the turn bounds [lo, hi]. Null if none. */
export function seekMarker(
  markers: readonly Marker[],
  at: number,
  dir: 1 | -1,
  lo: number,
  hi: number,
): number | null {
  const inBounds = (m: Marker) => m.index >= lo && m.index < hi;
  if (dir > 0) {
    for (const m of markers) {
      if (inBounds(m) && m.index + 1 > at) return m.index + 1;
    }
    return null;
  }
  for (let i = markers.length - 1; i >= 0; i--) {
    const m = markers[i];
    if (inBounds(m) && m.index + 1 < at) return m.index + 1;
  }
  return null;
}

/** The shape computeGraph takes, from a folded state. */
export function toLayoutInput(s: GraphState): Pick<GraphInput, "agents" | "usage" | "mailbox"> {
  const agents: GraphAgentInput[] = [...s.agents.values()].map((a) => ({
    id: a.id,
    parentAgentId: a.parentAgentId || undefined,
    name: a.name || undefined,
    role: a.role,
    status: a.status,
    task: a.task,
    dependsOn: a.dependsOn,
  }));
  return {
    agents,
    usage: Object.fromEntries(s.usage),
    mailbox: s.messages,
  };
}
