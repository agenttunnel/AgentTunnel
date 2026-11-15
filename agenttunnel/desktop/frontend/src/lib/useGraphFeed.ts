// useGraphFeed keeps the agent graph's event log and folded state for one
// thread: it loads the thread's graph events once, then applies each live
// `thread.event` frame as it arrives. Publishing is batched to animation
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
  /** Append-only log of applied events. Each publish is a copy of it. */
  events: GraphEvent[];
  /** Fold snapshots every CHECKPOINT_EVERY events, aligned with `events`. */
  checkpoints: GraphState[];
  state: GraphState;
};

const EMPTY: GraphFeed = {
  threadId: null,
  loaded: false,
  events: [],
  checkpoints: [emptyGraph()],
  state: emptyGraph(),
};

export function useGraphFeed(rpc: Rpc | null, threadId: string): GraphFeed {
  const [feed, setFeed] = useState<GraphFeed>(EMPTY);

  useEffect(() => {
    if (!rpc || !threadId) return;
    // Mutable only inside this effect; published copies are immutable.
    const log: GraphEvent[] = [];
    let checkpoints: GraphState[] = [emptyGraph()];
    let state = emptyGraph();
    let loaded = false;
    let historyFailed = false;
    let buffered: GraphEvent[] = [];
    let frame = 0;
    let cancelled = false;

    const accept = (ev: GraphEvent) => {
      const next = reduceGraph(state, ev);
      if (next === state) return;
      state = next;
      log.push(ev);
      if (log.length % CHECKPOINT_EVERY === 0) checkpoints.push(next);
    };
    const publish = () => {
      frame = 0;
      if (cancelled) return;
      setFeed({ threadId, loaded, events: log.slice(), checkpoints: checkpoints.slice(), state });
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
      accept(ev);
      schedule();
    });

    rpc
      .call<unknown[]>("thread.events", { threadId })
      .then((rows) => {
        if (cancelled) return;
        // Seed in one pass: history and the frames buffered during the load,
        // keeping only strictly increasing seqs so overlap is dropped once.
        // The log is empty here, so the fold runs once over the whole history
        // rather than copying the state per event.
        let tail = 0;
        const take = (ev: GraphEvent | null) => {
          if (!ev || ev.seq <= tail) return;
          tail = ev.seq;
          log.push(ev);
        };
        for (const row of rows) take(toGraphEvent(row));
        for (const ev of buffered) take(ev);
        buffered = [];
        checkpoints = buildCheckpoints(log);
        state = stateAt(log, checkpoints, log.length);
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
  }, [rpc, threadId]);

  // A feed from another thread is never shown, even for the one render before
  // the effect swaps it.
  return feed.threadId === threadId ? feed : { ...EMPTY, threadId };
}
