// useGraphFeed keeps the agent graph's event log and folded state for one
// thread: it loads the thread's graph events once, then applies each live
// `thread.event` frame as it arrives, and loads again after a reconnect. Publishing is batched to animation
// frames, so a burst of events re-renders the graph once per frame.

import { useEffect, useState } from "react";
import {
  CHECKPOINT_EVERY,
  buildCheckpoints,
  emptyGraph,
  reduceGraph,
  stateAt,
  toGraphEvent,
  type GraphEvent,
  type GraphState,
} from "./graphEvents";
import type { Rpc } from "./rpc";

export type GraphFeed = {
  threadId: string | null;
  /** False until the history has loaded; the graph falls back to thread state. */
  loaded: boolean;
  /** Append-only log shared with the fold. Only the first `count` entries belong to this snapshot. */
  events: readonly GraphEvent[];
  count: number;
  /** Fold snapshots every CHECKPOINT_EVERY events, aligned with `events`; also append-only. */
  checkpoints: readonly GraphState[];
  state: GraphState;
};

const EMPTY: GraphFeed = {
  threadId: null,
  loaded: false,
  events: [],
  count: 0,
  checkpoints: [emptyGraph()],
  state: emptyGraph(),
};

/** The fold behind one feed. The log and checkpoints grow in place; a publish
 * hands out the same arrays plus a count, so a frame costs the same whatever
 * the thread's length. States are immutable: each applied event makes a new one. */
export class GraphFold {
  readonly events: GraphEvent[] = [];
  checkpoints: GraphState[] = [emptyGraph()];
  state: GraphState = emptyGraph();

  /** Folds one live event. Returns false when it was already applied. */
  accept(ev: GraphEvent): boolean {
    const next = reduceGraph(this.state, ev);
    if (next === this.state) return false;
    this.state = next;
    this.events.push(ev);
    if (this.events.length % CHECKPOINT_EVERY === 0) this.checkpoints.push(next);
    return true;
  }

  /** Seeds from the loaded history, then the frames buffered during the load.
   * Only strictly increasing seqs are kept, so overlap is dropped once. */
  seed(history: readonly unknown[], buffered: readonly GraphEvent[]): void {
    let tail = 0;
    const take = (ev: GraphEvent | null) => {
      if (!ev || ev.seq <= tail) return;
      tail = ev.seq;
      this.events.push(ev);
    };
    for (const row of history) take(toGraphEvent(row));
    for (const ev of buffered) take(ev);
    this.checkpoints = buildCheckpoints(this.events);
    this.state = stateAt(this.events, this.checkpoints, this.events.length);
  }
}

export function useGraphFeed(rpc: Rpc | null, threadId: string): GraphFeed {
  const [feed, setFeed] = useState<GraphFeed>(EMPTY);
  // Frames sent while the socket was down are lost: a reconnect reads the
  // history again. The old feed stays on screen until the new one loads.
  const [epoch, setEpoch] = useState(0);
  useEffect(() => rpc?.onReconnect(() => setEpoch((n) => n + 1)), [rpc]);

  useEffect(() => {
    if (!rpc || !threadId) return;
    const fold = new GraphFold();
    let loaded = false;
    let historyFailed = false;
    let buffered: GraphEvent[] = [];
    let frame = 0;
    let cancelled = false;

    const publish = () => {
      frame = 0;
      if (cancelled) return;
      setFeed({
        threadId,
        loaded,
        events: fold.events,
        count: fold.events.length,
        checkpoints: fold.checkpoints,
        state: fold.state,
      });
    };
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(publish);
    };

    // Subscribe before reading history, so no frame can fall between the two.
    // Frames that arrive during the load are buffered and applied after it.
    // If the load fails, nothing more is buffered: the graph keeps the thread
    // state fallback and the frames are dropped.
    const off = rpc.onThreadEvent((raw) => {
      const ev = toGraphEvent(raw, threadId);
      if (!ev) return;
      if (!loaded) {
        if (!historyFailed) buffered.push(ev);
        return;
      }
      fold.accept(ev);
      schedule();
    });

    rpc
      .call<unknown[]>("thread.events", { threadId })
      .then((rows) => {
        if (cancelled) return;
        // The log is empty here, so the fold runs once over the whole history
        // rather than copying the state per event.
        fold.seed(rows, buffered);
        buffered = [];
        loaded = true;
        publish();
      })
      .catch(() => {
        historyFailed = true;
        buffered = [];
      });

    return () => {
      cancelled = true;
      off();
      if (frame) cancelAnimationFrame(frame);
    };
  }, [rpc, threadId, epoch]);

  // A feed from another thread is never shown, even for the one render before
  // the effect swaps it.
  return feed.threadId === threadId ? feed : { ...EMPTY, threadId };
}
