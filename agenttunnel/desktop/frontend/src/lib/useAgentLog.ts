// useAgentLog keeps one agent's full output for the detail panel: one
// agents.logs read when the agent opens, then the agent's own agent.output
// frames appended as they stream (batched per animation frame). `resync`
// changes after a reconnect, which reads the log again.

import { useEffect, useState } from "react";
import { dropSnapshotOverlap, useFrameQueue } from "./liveLog";
import type { Rpc } from "./rpc";

export type AgentLogEntry = { agentId: string; kind: string; text: string; at: string };

export function useAgentLog(
  rpc: Rpc | null,
  threadId: string | null,
  agentId: string | null,
  resync: number,
): AgentLogEntry[] {
  const [entries, setEntries] = useState<AgentLogEntry[]>([]);
  const { push, clear } = useFrameQueue<AgentLogEntry>((items) => setEntries((prev) => [...prev, ...items]));

  useEffect(() => {
    clear();
    setEntries([]);
    if (!rpc || !threadId || !agentId) return;
    let stale = false;
    // Frames that arrive before the read lands wait here, then merge after it.
    let buffered: AgentLogEntry[] | null = [];
    const off = rpc.onThreadEvent((raw) => {
      const ev = raw as { type?: string; threadId?: string; at?: string; payload?: Record<string, unknown> };
      if (stale || ev?.type !== "agent.output" || ev.threadId !== threadId) return;
      if (ev.payload?.agentId !== agentId) return;
      const entry = {
        agentId,
        kind: String(ev.payload.kind ?? "text"),
        text: String(ev.payload.text ?? ""),
        at: ev.at ?? new Date().toISOString(),
      };
      if (buffered) buffered.push(entry);
      else push(entry);
    });
    const settle = (snapshot: AgentLogEntry[]) => {
      if (stale) return;
      const fresh = dropSnapshotOverlap(snapshot, buffered ?? []);
      buffered = null;
      setEntries([...snapshot, ...fresh]);
    };
    rpc
      .call<AgentLogEntry[] | null>("agents.logs", { threadId, agentId })
      .then((l) => settle(l ?? []))
      .catch(() => settle([]));
    return () => {
      stale = true;
      off();
    };
  }, [rpc, threadId, agentId, resync, clear, push]);

  return entries;
}
