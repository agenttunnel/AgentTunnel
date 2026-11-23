// Folds `thread.event` frames into a thread's read model on the client, so a
// running turn does not cost a thread.get per event. It mirrors the daemon's
// projector (internal/domain/projector.go) for the events a turn emits often;
// anything else asks for a refetch. Pure: no React, no RPC.
//
// Every durable event carries its thread's sequence number and thread.get's
// state carries the last one it applied (`version`). An event right after the
// version folds; one at or below it is a duplicate; anything further ahead
// means frames were dropped, and the caller refetches.

import type { ThreadState } from "../App";
import type { AgentQuestion } from "./questions";
import { addUsage, zeroUsage, type TokenUsage } from "./usage";

/** One `thread.event` frame as the daemon sends it. Ephemeral frames
 * (agent.output) carry seq 0: they never enter the log or the read model. */
export type ThreadEvent = {
  seq: number;
  threadId: string;
  type: string;
  at: string;
  payload: Record<string, unknown>;
};

/** parseThreadEvent narrows a raw frame, or returns null for a malformed one. */
export function parseThreadEvent(raw: unknown): ThreadEvent | null {
  if (!raw || typeof raw !== "object") return null;
  const r = raw as Record<string, unknown>;
  if (typeof r.type !== "string" || typeof r.threadId !== "string") return null;
  return {
    seq: typeof r.seq === "number" ? r.seq : 0,
    threadId: r.threadId,
    type: r.type,
    at: typeof r.at === "string" ? r.at : new Date().toISOString(),
    payload: r.payload && typeof r.payload === "object" ? (r.payload as Record<string, unknown>) : {},
  };
}

/** Wire fields the TS ThreadState leaves undeclared but the fold maintains. */
type Wire = ThreadState & {
  version?: number;
  archived?: boolean;
  lastSeenAt?: string;
  updatedAt?: string;
  description?: string;
  draft?: boolean;
  controlledBy?: string;
  model?: unknown;
  sharedOrder?: string[] | null;
  subthreads?: string[] | null;
};

type TurnWire = NonNullable<ThreadState["turns"]>[number] & { endedAt?: string; failed?: boolean };

/** The last event a state has applied; 0 for a thread with no durable log yet. */
export function stateVersion(s: ThreadState | null | undefined): number {
  return (s as Wire | null | undefined)?.version ?? 0;
}

export type FoldResult =
  /** Applied: the new state (a fresh object; untouched parts keep identity). */
  | { kind: "applied"; state: ThreadState }
  /** Already applied (a duplicate or late frame): keep the state as it is. */
  | { kind: "stale" }
  /** Frames are missing, the event is not folded here, or it cannot apply:
   * the caller refetches thread.get. */
  | { kind: "refetch" };

// Mirrors the daemon's read-model bounds so a folded state matches a fetched one.
const MAX_MAILBOX = 200;
const MAX_STEPS = 200;
const MAX_QUESTIONS = 100;

const TERMINAL = new Set(["succeeded", "failed", "cancelled"]);

const str = (v: unknown) => (typeof v === "string" ? v : "");

/** foldThreadEvent applies one durable event to a thread's state. */
export function foldThreadEvent(state: ThreadState, ev: ThreadEvent): FoldResult {
  if (ev.seq <= 0) return { kind: "stale" };
  const version = stateVersion(state);
  if (ev.seq <= version) return { kind: "stale" };
  if (ev.seq !== version + 1) return { kind: "refetch" };
  const next = apply(state as Wire, ev);
  if (!next) return { kind: "refetch" };
  next.version = ev.seq;
  // A view is not activity, so thread.seen leaves updatedAt alone.
  if (ev.type !== "thread.seen") next.updatedAt = ev.at;
  return { kind: "applied", state: next };
}

function turnFor(turns: TurnWire[], turn: number): [TurnWire[], TurnWire] {
  const i = turns.findIndex((t) => t.turn === turn);
  if (i >= 0) {
    const copy = { ...turns[i] };
    return [turns.map((t, j) => (j === i ? copy : t)), copy];
  }
  const fresh: TurnWire = { turn, agentOrder: [], agents: {} };
  return [[...turns, fresh], fresh];
}

function apply(s: Wire, ev: ThreadEvent): Wire | null {
  const p = ev.payload;
  const n: Wire = { ...s };
  switch (ev.type) {
    case "thread.renamed":
      n.title = str(p.title);
      return n;
    case "thread.described":
      n.description = str(p.description);
      return n;
    case "thread.draft-started":
      n.draft = false;
      return n;
    // Settings events carry Go field names: their payloads have no json tags.
    case "thread.automode-set":
      n.autoMode = !!p.Enabled;
      return n;
    case "thread.auto-approved-models-set":
      n.autoApprovedModels = p.Models && typeof p.Models === "object" ? (p.Models as Record<string, string[]>) : {};
      return n;
    case "thread.archive-set":
      n.archived = !!p.Archived;
      return n;
    case "thread.peer-mailbox-set":
      n.peerMailbox = !!p.Enabled;
      return n;
    case "thread.status-period-set":
      n.statusPeriodSeconds = Number(p.PeriodSeconds) || 0;
      return n;
    case "thread.leads-set":
      n.leads = Number(p.Count) || 0;
      return n;
    case "thread.model-set":
      n.model = p.Selection;
      return n;
    case "thread.seen":
      if (!n.lastSeenAt || after(ev.at, n.lastSeenAt)) n.lastSeenAt = ev.at;
      return n;
    case "thread.steering-injected":
      return n;
    case "thread.steering-consumed": {
      const ids = new Set(Array.isArray(p.messageIds) ? (p.messageIds as string[]) : []);
      n.messages = (s.messages ?? []).map((m) => (ids.has(m.id) ? { ...m, consumed: true } : m));
      return n;
    }
    case "thread.message-sent": {
      const author = str(p.author);
      const images = Array.isArray(p.images) && p.images.length > 0 ? (p.images as never[]) : undefined;
      n.messages = [
        ...(s.messages ?? []),
        { id: str(p.messageId), author, text: str(p.text), at: ev.at, ...(images ? { images } : {}) },
      ];
      // A user message opens a turn, and so does the controller's relaunch.
      if (author === "user" || (!!s.controlledBy && author === s.controlledBy)) {
        n.turn = (s.turn ?? 0) + 1;
        const [turns, t] = turnFor((s.turns ?? []) as TurnWire[], n.turn);
        t.at = ev.at;
        n.turns = turns;
      }
      return n;
    }
    case "thread.task-added":
      n.tasks = [...(s.tasks ?? []), { name: str(p.name), title: str(p.title), status: "todo" }];
      return n;
    case "thread.task-status-changed":
      n.tasks = (s.tasks ?? []).map((t) => (t.name === p.name ? { ...t, status: str(p.status) } : t));
      return n;
    case "agent.task-dispatched": {
      const id = str(p.agentId);
      const agents = { ...(s.agents ?? {}) };
      if (!agents[id]) n.agentOrder = [...(s.agentOrder ?? []), id];
      agents[id] = {
        id,
        threadId: ev.threadId,
        parentAgentId: str(p.parentAgentId) || undefined,
        name: str(p.name) || undefined,
        role: str(p.role),
        harness: str(p.harness),
        task: str(p.task),
        model: str(p.model),
        pipelineId: str(p.pipelineId) || undefined,
        dependsOn: Array.isArray(p.dependsOn) ? (p.dependsOn as string[]) : undefined,
        status: "pending",
        startedAt: ev.at,
      };
      n.agents = agents;
      return n;
    }
    case "agent.status-changed": {
      const id = str(p.agentId);
      const prev = s.agents?.[id];
      if (!prev) return null;
      const status = str(p.status);
      const a = { ...prev, status, reason: str(p.reason) || undefined } as typeof prev & { reason?: string };
      if (TERMINAL.has(status)) {
        a.endedAt = ev.at;
        // A lead finishing is the team finishing the turn.
        if (!prev.parentAgentId && (s.turn ?? 0) > 0) {
          const [turns, t] = turnFor((s.turns ?? []) as TurnWire[], s.turn);
          t.endedAt = ev.at;
          if (status !== "succeeded") t.failed = true;
          n.turns = turns;
        }
      }
      n.agents = { ...s.agents, [id]: a };
      return n;
    }
    case "agent.model-observed": {
      const id = str(p.agentId);
      const prev = s.agents?.[id];
      if (!prev) return null;
      n.agents = { ...s.agents, [id]: { ...prev, model: str(p.model) } };
      return n;
    }
    case "agent.usage-recorded": {
      const id = str(p.agentId);
      const usage = { ...zeroUsage, ...(p.usage as Partial<TokenUsage> | undefined) };
      const cur = s.usage?.[id];
      n.usage = {
        ...s.usage,
        [id]: { agentId: id, role: str(p.role), usage: addUsage(cur?.usage ?? zeroUsage, usage) },
      };
      const [turns, t] = turnFor((s.turns ?? []) as TurnWire[], Number(p.turn) || 0);
      const order = t.agentOrder ?? [];
      t.agentOrder = order.includes(id) ? order : [...order, id];
      t.agents = { ...t.agents, [id]: addUsage(t.agents?.[id] ?? zeroUsage, usage) };
      n.turns = turns;
      const steps = [...(s.steps?.[id] ?? []), { at: ev.at, usage }];
      n.steps = { ...s.steps, [id]: steps.length > MAX_STEPS ? steps.slice(-MAX_STEPS) : steps };
      return n;
    }
    case "context.shared-set": {
      const key = str(p.key);
      if (!s.shared || !(key in s.shared)) n.sharedOrder = [...(s.sharedOrder ?? []), key];
      n.shared = { ...s.shared, [key]: str(p.value) };
      return n;
    }
    case "mailbox.posted": {
      const mailbox = [
        ...(s.mailbox ?? []),
        { from: str(p.from), to: str(p.to), kind: str(p.kind), body: str(p.body), at: ev.at, turn: s.turn },
      ];
      n.mailbox = mailbox.length > MAX_MAILBOX ? mailbox.slice(-MAX_MAILBOX) : mailbox;
      return n;
    }
    case "thread.question-asked": {
      const q: AgentQuestion = {
        questionId: str(p.questionId),
        agentId: str(p.agentId),
        kind: str(p.kind) as AgentQuestion["kind"],
        question: str(p.question),
        options: Array.isArray(p.options) ? (p.options as string[]) : undefined,
        notify: str(p.notify) || undefined,
        status: "pending",
        at: ev.at,
      };
      n.questions = trimQuestions([...(s.questions ?? []), q]);
      return n;
    }
    case "thread.question-closed": {
      const id = str(p.questionId);
      if (!(s.questions ?? []).some((q) => q.questionId === id)) return null;
      n.questions = (s.questions ?? []).map((q) =>
        q.questionId === id
          ? {
              ...q,
              status: p.answered ? "answered" : "closed",
              selected: Array.isArray(p.selected) ? (p.selected as string[]) : undefined,
              text: str(p.text) || undefined,
              reason: str(p.reason) || undefined,
            }
          : q,
      );
      return n;
    }
    case "thread.subthread-spawned":
      n.subthreads = [...(s.subthreads ?? []), str(p.threadId)];
      return n;
    default:
      // Rare events (worktrees, schedules, reviews, runtimes, creation and
      // deletion) are left to a refetch rather than mirrored here.
      return null;
  }
}

/** after compares two daemon timestamps as Go's time.After does: Date.parse
 * stops at milliseconds, so the nanosecond digits break a tie. */
function after(a: string, b: string): boolean {
  const ms = Date.parse(a) - Date.parse(b);
  if (ms !== 0) return ms > 0;
  const subMs = (s: string) => Number((/\.(\d+)/.exec(s)?.[1] ?? "").padEnd(9, "0").slice(3, 9));
  return subMs(a) > subMs(b);
}

/** Drops the oldest closed questions past the cap; pending ones always stay. */
function trimQuestions(qs: AgentQuestion[]): AgentQuestion[] {
  let overflow = qs.length - MAX_QUESTIONS;
  if (overflow <= 0) return qs;
  return qs.filter((q) => {
    if (overflow > 0 && q.status !== "pending") {
      overflow--;
      return false;
    }
    return true;
  });
}

/** Events that can change a thread's row in thread.list. High-volume team
 * chatter (usage steps, mailbox, shared context, steering) never does. */
export function changesThreadList(type: string): boolean {
  return !QUIET_FOR_LIST.has(type);
}

const QUIET_FOR_LIST = new Set([
  "agent.output",
  "agent.usage-recorded",
  "mailbox.posted",
  "context.shared-set",
  "thread.steering-injected",
  "thread.steering-consumed",
  "thread.task-added",
  "thread.task-status-changed",
]);
