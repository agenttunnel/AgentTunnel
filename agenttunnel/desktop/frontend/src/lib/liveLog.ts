// The live activity log: agent output folded into display lines. Each line has
// a stable id, so a list keyed by it re-renders only the line that grew, and
// streamed chunks are coalesced to one state update per animation frame.

import { useCallback, useEffect, useRef, useState } from "react";

/** One raw output chunk, as thread.logs and agent.output frames carry it.
 * `at` is when the daemon recorded it; a grown line keeps its first chunk's. */
export type LogChunk = { agentId: string; kind: string; text: string; at?: string };

/** One display line of the log. `id` survives the line growing. */
export type LiveLine = LogChunk & { id: number };

export const LIVE_LIMIT = 400;

let nextLineId = 1;

/** Appends one chunk. Harnesses stream text in chunks, so a text or reasoning
 * chunk from the same agent grows the last line instead of stacking. */
export function pushLiveLine(prev: LiveLine[], chunk: LogChunk): LiveLine[] {
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

/** useFrameQueue collects items and hands them to `flush` at most once per
 * animation frame, so a burst of streamed chunks costs one render. */
export function useFrameQueue<T>(flush: (items: T[]) => void): {
  push: (item: T) => void;
  clear: () => void;
} {
  const queue = useRef<T[]>([]);
  const frame = useRef(0);
  const flushRef = useRef(flush);
  flushRef.current = flush;
  const clear = useCallback(() => {
    queue.current = [];
    if (frame.current) cancelAnimationFrame(frame.current);
    frame.current = 0;
  }, []);
  const push = useCallback((item: T) => {
    queue.current.push(item);
    if (frame.current) return;
    frame.current = requestAnimationFrame(() => {
      frame.current = 0;
      const items = queue.current;
      queue.current = [];
      if (items.length > 0) flushRef.current(items);
    });
  }, []);
  useEffect(() => clear, [clear]);
  return { push, clear };
}

/** useLiveLog holds the activity log lines. `reset` replaces them (a thread's
 * persisted snapshot); `append` queues one streamed chunk for the next frame. */
export function useLiveLog() {
  const [lines, setLines] = useState<LiveLine[]>([]);
  const { push, clear } = useFrameQueue<LogChunk>((chunks) =>
    setLines((prev) => chunks.reduce(pushLiveLine, prev)),
  );
  const reset = useCallback(
    (chunks: LogChunk[]) => {
      clear();
      setLines(chunks.reduce<LiveLine[]>(pushLiveLine, []));
    },
    [clear],
  );
  return { lines, append: push, reset };
}

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
