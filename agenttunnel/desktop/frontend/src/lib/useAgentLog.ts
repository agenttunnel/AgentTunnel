// useAgentLog keeps one agent's full output for the detail panel: one
// agents.logs read when the agent opens, then the agent's own agent.output
// frames appended as they stream (batched per animation frame). It writes to
// agentLogStore, which only AgentDetail reads. `resync` changes after a
// reconnect, which reads the log again.

import { useEffect } from "react";
import { agentLogStore, createFrameQueue, dropSnapshotOverlap, setLogs } from "./liveLog";
import type { Rpc } from "./rpc";
import type { ToolCall } from "./toolCalls";

type Entry = (typeof agentLogStore extends { get: () => { entries: (infer E)[] } } ? E : never) & {
  agentId: string;
  at: string;
};

export function useAgentLog(
  rpc: Rpc | null,
  threadId: string | null,
  agentId: string | null,
  resync: number,
): void {
  useEffect(() => {
    setLogs([]);
    if (!rpc || !threadId || !agentId) return;
    let stale = false;
    const queue = createFrameQueue<Entry>((items) => setLogs((prev) => [...prev, ...items]));
    // Frames that arrive before the read lands wait here, then merge after it.
    let buffered: Entry[] | null = [];
    const off = rpc.onThreadEvent((raw) => {
      const ev = raw as { type?: string; threadId?: string; at?: string; payload?: Record<string, unknown> };
      if (stale || ev?.type !== "agent.output" || ev.threadId !== threadId) return;
      if (ev.payload?.agentId !== agentId) return;
      const entry: Entry = {
        agentId,
        kind: String(ev.payload.kind ?? "text"),
        text: String(ev.payload.text ?? ""),
        at: ev.at ?? new Date().toISOString(),
        tool: ev.payload.tool as ToolCall | undefined,
      };
      if (buffered) buffered.push(entry);
      else queue.push(entry);
    });
    const settle = (snapshot: Entry[]) => {
      if (stale) return;
      const fresh = dropSnapshotOverlap(snapshot, buffered ?? []);
      buffered = null;
      setLogs([...snapshot, ...fresh]);
    };
    rpc
      .call<Entry[] | null>("agents.logs", { threadId, agentId })
      .then((l) => settle(l ?? []))
      .catch(() => settle([]));
    return () => {
      stale = true;
      queue.clear();
      off();
    };
  }, [rpc, threadId, agentId, resync]);
}
