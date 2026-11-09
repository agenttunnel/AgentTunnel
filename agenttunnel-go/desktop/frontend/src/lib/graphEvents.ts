// Agent-graph replay state: a pure fold over the thread's graph-relevant
// events. Live mode applies each frame as it arrives; replay folds the prefix
// up to a cursor. Both go through applyEvent, so a scrubbed view and the live
// view of the same log are the same state.

import { computeGraph, type MailboxMessage } from "./graph";
import { addUsage, zeroUsage, type TokenUsage } from "./usage";

/** Bodies echoed into the feed are clipped; the full mailbox comes from
 * thread.get. Must match graphBodyMax on the daemon. */
const BODY_MAX = 280;

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

export type GraphState = {
  /** Last applied seq; events at or below it are ignored. */
  seq: number;
  /** Insertion order is the agent order the layout stacks by. */
  agents: Map<string, AgentRecord>;
  usage: Map<string, { usage: TokenUsage }>;
  messages: MailboxMessage[];
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

/** Applies one event to a state the caller owns. Mutates `s`. */
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
      s.messages.push({ from: p.from, to: p.to, kind: p.kind, body: p.body, at: ev.at });
      s.lastMessage = { from: p.from, to: p.to, seq: ev.seq };
      return;
    }
  }
}

/** Pure step: returns `state` itself when the event was already applied. */
export function reduceGraph(state: GraphState, ev: GraphEvent): GraphState {
  if (ev.seq <= state.seq) return state;
  const next: GraphState = {
    ...state,
    agents: new Map(state.agents),
    usage: new Map(state.usage),
    messages: state.messages.slice(),
  };
  applyEvent(next, ev);
  return next;
}

/** Folds the first `count` events into a fresh state. Mutates a private
 * accumulator, so a scrub costs one pass rather than one copy per event. */
export function foldTo(events: readonly GraphEvent[], count: number): GraphState {
  const s = emptyGraph();
  const n = Math.min(count, events.length);
  for (let i = 0; i < n; i++) {
    const ev = events[i];
    if (ev.seq <= s.seq) continue;
    applyEvent(s, ev);
  }
  return s;
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
