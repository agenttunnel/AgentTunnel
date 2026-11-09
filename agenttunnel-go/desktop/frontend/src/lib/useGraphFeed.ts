// useGraphFeed keeps the agent graph's event log and folded state for one
// thread: it loads the thread's graph events once, then applies each live
// `thread.event` frame as it arrives. Publishing is batched to animation
// frames, so a burst of events re-renders the graph once per frame.

import { useEffect, useState } from "react";
import { emptyGraph, reduceGraph, toGraphEvent, type GraphEvent, type GraphState } from "./graphEvents";
import type { Rpc } from "./rpc";

export type GraphFeed = {
  threadId: string | null;
  /** False until the history has loaded; the graph falls back to thread state. */
  loaded: boolean;
  events: GraphEvent[];
  state: GraphState;
};

const EMPTY: GraphFeed = { threadId: null, loaded: false, events: [], state: emptyGraph() };

export function useGraphFeed(rpc: Rpc | null, threadId: string): GraphFeed {
  const [feed, setFeed] = useState<GraphFeed>(EMPTY);

  useEffect(() => {
    if (!rpc || !threadId) return;
    // Mutable only inside this effect; published copies are immutable.
    const log: GraphEvent[] = [];
    let state = emptyGraph();
    let loaded = false;
    let buffered: GraphEvent[] = [];
    let frame = 0;
    let cancelled = false;

    const accept = (ev: GraphEvent) => {
      const next = reduceGraph(state, ev);
      if (next === state) return;
      state = next;
      log.push(ev);
    };
    const publish = () => {
      frame = 0;
      if (cancelled) return;
      setFeed({ threadId, loaded, events: log.slice(), state });
    };
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(publish);
    };

    // Subscribe before reading history, so no frame can fall between the two.
    // Frames that arrive during the load are buffered and applied after it;
    // accept() drops anything the history already covered.
    const off = rpc.onThreadEvent((raw) => {
      const ev = toGraphEvent(raw, threadId);
      if (!ev) return;
      if (!loaded) {
        buffered.push(ev);
        return;
      }
      accept(ev);
      schedule();
    });

    rpc
      .call<unknown[]>("thread.events", { threadId })
      .then((rows) => {
        if (cancelled) return;
        for (const row of rows) {
          const ev = toGraphEvent(row);
          if (ev) accept(ev);
        }
        for (const ev of buffered) accept(ev);
        buffered = [];
        loaded = true;
        publish();
      })
      .catch(() => undefined);

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
