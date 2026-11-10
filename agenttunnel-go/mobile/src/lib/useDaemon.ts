// The daemon session as one hook: the RPC client, the thread list, the active
// thread's snapshot, and its live output. Screens read state and call actions;
// they never speak RPC themselves.
import { useCallback, useEffect, useRef, useState } from "react";

import {
  appendLiveLine,
  dropSnapshotOverlap,
  errorMessage,
  isStateFrame,
  outputFromFrame,
  type LiveLine,
  type ProjectView,
  type ThreadSnapshot,
  type ThreadSummary,
} from "./normalize";
import { RelayError, RpcClient, type DaemonPresence, type Pairing, type RpcStatus } from "./relay";

/** Refreshes coalesce: a burst of state frames costs one list and one snapshot read. */
const REFRESH_DEBOUNCE_MS = 250;

type LogRow = { agentId: string; kind: string; text: string };

export type DaemonState = {
  status: RpcStatus;
  statusError: RelayError | null;
  /**
   * The daemon's presence at the relay. "unknown" whenever the phone's own
   * socket is not open, since the relay cannot tell us then.
   */
  daemon: DaemonPresence | "unknown";
  queued: number;
  projects: ProjectView[];
  threads: ThreadSummary[];
  listError: string | null;
  activeId: string | null;
  snapshot: ThreadSnapshot | null;
  snapshotError: string | null;
  live: LiveLine[];
  /** Turn requests sent but not yet finished (includes ones waiting in the queue). */
  turnsInFlight: number;
  sendError: string | null;
};

export type DaemonActions = {
  refresh: () => Promise<void>;
  selectThread: (threadId: string) => void;
  /**
   * Sends a message. `steer` true routes it into the running turn (thread.steer)
   * instead of starting one. Resolves true once accepted or queued.
   */
  sendTurn: (text: string, steer: boolean) => Promise<boolean>;
  stopTurn: () => Promise<void>;
  clearSendError: () => void;
};

/**
 * Connects to the paired instance for as long as `pairing` is set. Initial
 * thread selection is the caller's; `initialThreadId` is opened once after the
 * first connect so the last-viewed thread comes back.
 */
export function useDaemon(
  pairing: Pairing | null,
  initialThreadId: string | null,
): [DaemonState, DaemonActions] {
  const [status, setStatus] = useState<RpcStatus>("idle");
  const [statusError, setStatusError] = useState<RelayError | null>(null);
  const [daemon, setDaemon] = useState<DaemonPresence | "unknown">("unknown");
  const [queued, setQueued] = useState(0);
  const [projects, setProjects] = useState<ProjectView[]>([]);
  const [threads, setThreads] = useState<ThreadSummary[]>([]);
  const [listError, setListError] = useState<string | null>(null);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [snapshot, setSnapshot] = useState<ThreadSnapshot | null>(null);
  const [snapshotError, setSnapshotError] = useState<string | null>(null);
  const [live, setLive] = useState<LiveLine[]>([]);
  const [turnsInFlight, setTurnsInFlight] = useState(0);
  const [sendError, setSendError] = useState<string | null>(null);

  const rpcRef = useRef<RpcClient | null>(null);
  const activeRef = useRef<string | null>(null);
  /** Frames that arrive before the thread.logs snapshot lands wait here. */
  const bufferRef = useRef<LiveLine[] | null>(null);
  const refreshTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const openedInitialRef = useRef(false);
  const initialRef = useRef(initialThreadId);
  initialRef.current = initialThreadId;

  // Functions the RPC callbacks call. Held in a ref so the socket never closes
  // over a stale closure.
  const refreshListRef = useRef<() => Promise<void>>(async () => undefined);
  const refreshSnapshotRef = useRef<(id: string) => Promise<void>>(async () => undefined);
  const attachRef = useRef<(id: string) => void>(() => undefined);

  const refreshList = useCallback(async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      const [ps, ts] = await Promise.all([
        rpc.call<ProjectView[]>("project.list"),
        rpc.call<ThreadSummary[]>("thread.list"),
      ]);
      setProjects(ps ?? []);
      setThreads(ts ?? []);
      setListError(null);
    } catch (e) {
      setListError(errorMessage(e));
    }
  }, []);

  const refreshSnapshot = useCallback(async (threadId: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      const st = await rpc.call<ThreadSnapshot>("thread.get", { threadId });
      if (activeRef.current !== threadId) return;
      setSnapshot(st);
      setSnapshotError(null);
    } catch (e) {
      if (activeRef.current !== threadId) return;
      setSnapshotError(errorMessage(e));
    }
  }, []);

  const attach = useCallback((threadId: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    bufferRef.current = [];
    setLive([]);
    // Subscribe before reading the log so no frame falls between the two.
    rpc.call("thread.subscribe", { threadId }).catch(() => undefined);
    rpc
      .call<LogRow[]>("thread.logs", { threadId })
      .then((rows) => {
        if (activeRef.current !== threadId) return;
        const snapshotLines: LiveLine[] = rows.map((r) => ({
          agentId: String(r.agentId ?? ""),
          kind: String(r.kind ?? "text"),
          text: String(r.text ?? ""),
        }));
        applyBuffered(snapshotLines);
      })
      .catch(() => {
        if (activeRef.current === threadId) applyBuffered([]);
      });
    refreshSnapshotRef.current(threadId).catch(() => undefined);
  }, []);

  function applyBuffered(snapshotLines: LiveLine[]) {
    const fresh = dropSnapshotOverlap(snapshotLines, bufferRef.current ?? []);
    bufferRef.current = null;
    setLive([...snapshotLines, ...fresh].reduce<LiveLine[]>(appendLiveLine, []));
  }

  refreshListRef.current = refreshList;
  refreshSnapshotRef.current = refreshSnapshot;
  attachRef.current = attach;

  const scheduleRefresh = useCallback(() => {
    if (refreshTimerRef.current !== null) clearTimeout(refreshTimerRef.current);
    refreshTimerRef.current = setTimeout(() => {
      refreshTimerRef.current = null;
      refreshListRef.current().catch(() => undefined);
      const id = activeRef.current;
      if (id) refreshSnapshotRef.current(id).catch(() => undefined);
    }, REFRESH_DEBOUNCE_MS);
  }, []);

  useEffect(() => {
    // A new pairing (or unpairing) starts from empty state, never the last
    // instance's threads.
    activeRef.current = null;
    openedInitialRef.current = false;
    setProjects([]);
    setThreads([]);
    setListError(null);
    setActiveId(null);
    setSnapshot(null);
    setSnapshotError(null);
    setLive([]);
    setQueued(0);
    setDaemon("unknown");
    if (!pairing) return;
    let presence: DaemonPresence | "unknown" = "unknown";
    const rpc = new RpcClient({
      wsUrl: pairing.wsUrl,
      token: pairing.token,
      onStatus: (next, error) => {
        setStatus(next);
        setStatusError(error ?? null);
        setQueued(rpc.queuedCount());
        if (next !== "open") {
          // The relay cannot report the daemon while our socket is down.
          presence = "unknown";
          setDaemon("unknown");
        }
        if (next === "open") {
          // Reconnect: refresh the list and snapshot, and reattach the thread.
          refreshListRef.current().catch(() => undefined);
          const id = activeRef.current;
          if (id) attachRef.current(id);
          if (!openedInitialRef.current) {
            openedInitialRef.current = true;
            const first = initialRef.current;
            if (first && !activeRef.current) {
              activeRef.current = first;
              setActiveId(first);
              setSnapshot(null);
              attachRef.current(first);
            }
          }
        }
      },
      onEvent: (frame) => {
        const out = outputFromFrame(frame);
        if (out) {
          if (out.threadId !== activeRef.current) return;
          if (bufferRef.current) bufferRef.current.push(out.line);
          else setLive((prev) => appendLiveLine(prev, out.line));
          return;
        }
        if (isStateFrame(frame)) scheduleRefresh();
      },
      onDaemon: (next) => {
        const was = presence;
        presence = next;
        setDaemon(next);
        // The daemon came back: its state may have moved while it was away.
        if (next === "online" && was === "offline") {
          refreshListRef.current().catch(() => undefined);
          const id = activeRef.current;
          if (id) refreshSnapshotRef.current(id).catch(() => undefined);
        }
      },
      // The daemon dropped events for this phone. Re-read, as after a reconnect.
      onResync: () => {
        refreshListRef.current().catch(() => undefined);
        const id = activeRef.current;
        if (id) attachRef.current(id);
      },
    });
    rpcRef.current = rpc;
    rpc.start();
    return () => {
      rpc.stop();
      rpcRef.current = null;
      if (refreshTimerRef.current !== null) clearTimeout(refreshTimerRef.current);
      refreshTimerRef.current = null;
    };
  }, [pairing, scheduleRefresh]);

  const selectThread = useCallback(
    (threadId: string) => {
      if (activeRef.current === threadId) return;
      activeRef.current = threadId;
      setActiveId(threadId);
      setSnapshot(null);
      setSnapshotError(null);
      attachRef.current(threadId);
    },
    [],
  );

  const sendTurn = useCallback(async (text: string, steer: boolean): Promise<boolean> => {
    const rpc = rpcRef.current;
    const threadId = activeRef.current;
    if (!rpc || !threadId) return false;
    setSendError(null);
    setTurnsInFlight((n) => n + 1);
    try {
      // A turn replies only when it finishes, so it is never timed out. A steer
      // replies at once. When offline, send() queues synchronously, so the
      // count is read right after.
      const pending = steer
        ? rpc.send("thread.steer", { threadId, text })
        : rpc.send("thread.turn.start", { threadId, text }, 0);
      setQueued(rpc.queuedCount());
      await pending;
      return true;
    } catch (e) {
      setSendError(errorMessage(e));
      return false;
    } finally {
      setTurnsInFlight((n) => Math.max(0, n - 1));
      setQueued(rpc.queuedCount());
      refreshSnapshotRef.current(threadId).catch(() => undefined);
    }
  }, []);

  const stopTurn = useCallback(async () => {
    const rpc = rpcRef.current;
    const threadId = activeRef.current;
    if (!rpc || !threadId) return;
    try {
      await rpc.call("thread.turn.stop", { threadId });
    } catch (e) {
      setSendError(errorMessage(e));
    }
    refreshSnapshotRef.current(threadId).catch(() => undefined);
  }, []);

  const refresh = useCallback(async () => {
    const id = activeRef.current;
    await Promise.all([
      refreshListRef.current(),
      id ? refreshSnapshotRef.current(id) : Promise.resolve(),
    ]);
  }, []);

  const clearSendError = useCallback(() => setSendError(null), []);

  return [
    {
      status,
      statusError,
      daemon,
      queued,
      projects,
      threads,
      listError,
      activeId,
      snapshot,
      snapshotError,
      live,
      turnsInFlight,
      sendError,
    },
    { refresh, selectThread, sendTurn, stopTurn, clearSendError },
  ];
}
