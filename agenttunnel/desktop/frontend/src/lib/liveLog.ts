// The live activity log: agent output folded into display lines. Each line has
// a stable id, so a list keyed by it re-renders only the line that grew, and
// streamed chunks are coalesced to one store update per animation frame.
// Components read the stores themselves, so a chunk re-renders the log box and
// nothing else.

import { useCallback, useEffect, useRef, useSyncExternalStore } from "react";
import { createStore } from "./store";
import { pushToolLine, type ToolFields } from "./toolCalls";

/** One raw output chunk, as thread.logs and agent.output frames carry it.
 * `at` is when the daemon recorded it; a grown line keeps its first chunk's. */
export type LogChunk = { agentId: string; kind: string; text: string; at?: string } & ToolFields;

/** One display line of the log. `id` survives the line growing. */
export type LiveLine = LogChunk & { id: number };

export const LIVE_LIMIT = 400;

let nextLineId = 1;

/** Appends one chunk. Harnesses stream text in chunks, so a text or reasoning
 * chunk from the same agent grows the last line instead of stacking. A tool
 * result folds into its call's card. */
export function pushLiveLine(prev: LiveLine[], chunk: LogChunk): LiveLine[] {
  if (chunk.tool) return pushToolLine(prev, { ...chunk, id: nextLineId++ }, LIVE_LIMIT);
  const last = prev[prev.length - 1];
  if (
    last &&
    last.agentId === chunk.agentId &&
    last.kind === chunk.kind &&
    (chunk.kind === "text" || chunk.kind === "reasoning")
  ) {
    return [...prev.slice(0, -1), { ...last, text: last.text + chunk.text }];
  }
  return [...prev.slice(-(LIVE_LIMIT - 1)), { ...chunk, id: nextLineId++ }];
}

/** Frames published while the snapshot loads may already be in the snapshot:
 * the daemon appends a line before publishing it. Drops the longest run at the
 * start of `buffered` that repeats the snapshot's tail, keeping only new ones. */
export function dropSnapshotOverlap<T extends LogChunk>(snapshot: T[], buffered: T[]): T[] {
  const same = (a: LogChunk, b: LogChunk) =>
    a.agentId === b.agentId && a.kind === b.kind && a.text === b.text;
  for (let k = Math.min(snapshot.length, buffered.length); k > 0; k--) {
    const tail = snapshot.slice(snapshot.length - k);
    if (tail.every((line, i) => same(line, buffered[i]))) return buffered.slice(k);
  }
  return buffered;
}

/** A queue that hands items to `flush` at most once per animation frame, so a
 * burst of streamed chunks costs one render. */
export function createFrameQueue<T>(flush: (items: T[]) => void) {
  let queue: T[] = [];
  let frame = 0;
  const clear = () => {
    queue = [];
    if (frame) cancelAnimationFrame(frame);
    frame = 0;
  };
  const push = (item: T) => {
    queue.push(item);
    if (frame) return;
    frame = requestAnimationFrame(() => {
      frame = 0;
      const items = queue;
      queue = [];
      if (items.length > 0) flush(items);
    });
  };
  return { push, clear };
}

/** useFrameQueue is createFrameQueue for a component: it keeps the latest
 * `flush` and cancels the pending frame on unmount. */
export function useFrameQueue<T>(flush: (items: T[]) => void): {
  push: (item: T) => void;
  clear: () => void;
} {
  const flushRef = useRef(flush);
  flushRef.current = flush;
  const queue = useRef<ReturnType<typeof createFrameQueue<T>>>();
  if (!queue.current) queue.current = createFrameQueue<T>((items) => flushRef.current(items));
  const { push, clear } = queue.current;
  useEffect(() => clear, [clear]);
  return { push, clear };
}

/**
 * The open thread's live output. Streamed chunks land here instead of in App
 * state, so a chunk re-renders the log box (components/LiveLog) and nothing else.
 */
export const liveLogStore = createStore({ lines: [] as LiveLine[] });
export const setLive = liveLogStore.setter("lines");

const liveQueue = createFrameQueue<LogChunk>((chunks) => setLive((prev) => chunks.reduce(pushLiveLine, prev)));

/** Queues one streamed chunk for the next animation frame. */
export const appendLive = liveQueue.push;

/** Replaces the log (a thread's persisted snapshot) and drops queued chunks. */
export function resetLive(chunks: LogChunk[]) {
  liveQueue.clear();
  setLive(chunks.reduce<LiveLine[]>(pushLiveLine, []));
}

/** The log of the agent open in the detail panel, written by useAgentLog. It
 * is read by AgentDetail alone, so a streamed chunk re-renders that sheet and
 * not App. */
export const agentLogStore = createStore({
  entries: [] as ({ kind: string; text: string; at?: string; agentId?: string } & ToolFields)[],
});
export const setLogs = agentLogStore.setter("entries");

/** What the transcript needs to show the reply a running lead is writing. */
export type ReplySource = {
  /** Ids of the running lead agents (those whose final text is the message). */
  leads: ReadonlySet<string>;
  /** Start of the current turn. */
  since: string | undefined;
  author: string;
};

/**
 * The reply being written: the newest prose line a running lead (an agent
 * whose final text becomes the turn's message) wrote since `since`, the start
 * of the current turn. Lead ids repeat across turns, so lines from before the
 * turn never count. Null when there is nothing to show.
 */
export function streamingReply(
  lines: LiveLine[],
  runningLeads: ReadonlySet<string>,
  since: string | undefined,
): LiveLine | null {
  if (runningLeads.size === 0 || !since) return null;
  const start = Date.parse(since);
  for (let i = lines.length - 1; i >= 0; i--) {
    const l = lines[i];
    if (!l.at || Date.parse(l.at) < start) return null;
    if (runningLeads.has(l.agentId) && l.kind === "text" && l.text.trim() !== "") return l;
  }
  return null;
}

/** useStreamingLine reads the reply line from the live log. The line object is
 * stable until a chunk grows it, so only the reply row re-renders. */
export function useStreamingLine(source: ReplySource | null | undefined): LiveLine | null {
  const get = useCallback(
    () => (source ? streamingReply(liveLogStore.get().lines, source.leads, source.since) : null),
    [source],
  );
  return useSyncExternalStore(liveLogStore.subscribe, get);
}
