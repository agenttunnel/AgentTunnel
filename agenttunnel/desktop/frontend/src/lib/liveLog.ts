import { createStore } from "./store";
import { pushToolLine, type ToolFields } from "./toolCalls";

/** One line of the activity log box. */
export type LiveLine = { agentId: string; kind: string; text: string } & ToolFields;
const LIVE_LIMIT = 400;

/** Appends one output line. Harnesses stream text in chunks, so a text or
 *  reasoning chunk from the same agent grows the last block instead of stacking.
 *  A tool result folds into its call's card. */
export function pushLiveLine(prev: LiveLine[], line: LiveLine): LiveLine[] {
  if (line.tool) return pushToolLine(prev, line, LIVE_LIMIT);
  const last = prev[prev.length - 1];
  if (
    last &&
    last.agentId === line.agentId &&
    last.kind === line.kind &&
    (line.kind === "text" || line.kind === "reasoning")
  ) {
    return [...prev.slice(0, -1), { ...last, text: last.text + line.text }];
  }
  return [...prev.slice(-(LIVE_LIMIT - 1)), line];
}

/** Frames published while the snapshot loads may already be in the snapshot:
 *  the daemon appends a line before publishing it. Drop the longest run at the
 *  start of `buffered` that repeats the snapshot's tail, keeping only new lines. */
export function dropSnapshotOverlap(snapshot: LiveLine[], buffered: LiveLine[]): LiveLine[] {
  const same = (a: LiveLine, b: LiveLine) =>
    a.agentId === b.agentId && a.kind === b.kind && a.text === b.text;
  for (let k = Math.min(snapshot.length, buffered.length); k > 0; k--) {
    const tail = snapshot.slice(snapshot.length - k);
    if (tail.every((line, i) => same(line, buffered[i]))) return buffered.slice(k);
  }
  return buffered;
}

/**
 * The open thread's live output. Streamed chunks land here instead of in App
 * state, so a chunk re-renders the log box (components/LiveLog) and nothing else.
 */
export const liveLogStore = createStore({ lines: [] as LiveLine[] });
export const setLive = liveLogStore.setter("lines");

/** The log of the agent open in the detail panel, polled while it is open. It
 * is read by AgentDetail alone, so a poll re-renders that sheet and not App. */
export const agentLogStore = createStore({
  entries: [] as ({ kind: string; text: string; at?: string; agentId?: string } & ToolFields)[],
});
export const setLogs = agentLogStore.setter("entries");
