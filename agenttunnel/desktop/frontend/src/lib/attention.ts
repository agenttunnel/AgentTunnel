// "A turn finished or an agent asked a question while you were not looking."
// One signal, derived from the thread list the app already keeps fresh (events
// plus the 4s poll), drives every surface: the tab title count, the desktop
// dock badge, browser notifications in the web UI and native notifications in
// the desktop shell.

import { useEffect, useMemo, useRef, useState } from "react";
import { Events } from "@wailsio/runtime";
import { inDesktopShell } from "./rpc";
import { getPref, setPref } from "./prefs";

/** The thread.list fields the signal reads. */
export type AttentionThread = {
  id: string;
  title: string;
  archived?: boolean;
  owner?: string;
  unseen?: boolean;
  pendingQuestions?: number;
  runningAgents?: number;
};

export type AttentionEvent = { thread: AttentionThread; kind: "finished" | "question" };

type Snap = { running: number; unseen: boolean; questions: number };

/** Threads the user drives; orchestrator-started subthreads report to their lead instead. */
function watched(t: AttentionThread): boolean {
  return !t.archived && t.owner !== "orchestrator";
}

/** needsAttention is the daemon's view: a finished turn not yet seen, or a question waiting. */
export function needsAttention(t: AttentionThread): boolean {
  return watched(t) && (!!t.unseen || (t.pendingQuestions ?? 0) > 0);
}

/**
 * attentionEvents compares two thread-list snapshots and reports what newly
 * happened: a turn that ended (running agents went to none) or a question that
 * was asked. The first snapshot (prev null) only primes, so loading the app
 * never replays history as notifications.
 */
export function attentionEvents(
  prev: Map<string, Snap> | null,
  threads: AttentionThread[],
): { snap: Map<string, Snap>; events: AttentionEvent[] } {
  const snap = new Map<string, Snap>();
  const events: AttentionEvent[] = [];
  for (const t of threads) {
    const now = { running: t.runningAgents ?? 0, unseen: !!t.unseen, questions: t.pendingQuestions ?? 0 };
    snap.set(t.id, now);
    const before = prev?.get(t.id);
    if (!prev || !before || !watched(t)) continue;
    if (now.questions > before.questions) events.push({ thread: t, kind: "question" });
    else if ((before.running > 0 && now.running === 0) || (now.unseen && !before.unseen)) {
      events.push({ thread: t, kind: "finished" });
    }
  }
  return { snap, events };
}

/** The tab title: the unseen count, then the open thread, then the app. */
export function attentionTitle(count: number, threadTitle: string | null): string {
  const base = threadTitle ? `${threadTitle} — AgentTunnel` : "AgentTunnel";
  return count > 0 ? `(${count}) ${base}` : base;
}

const BROWSER_NOTIFY_KEY = "at.browserNotify";

function browserNotifySupported(): boolean {
  return !inDesktopShell() && typeof Notification !== "undefined";
}

/** Browser notifications are on when the user asked for them and the browser allows them. */
function browserNotifyOn(): boolean {
  return browserNotifySupported() && getPref(BROWSER_NOTIFY_KEY) === "1" && Notification.permission === "granted";
}

/** isLooking: the window is in front and focused, so the user sees the open thread. */
function isLooking(): boolean {
  return document.visibilityState === "visible" && document.hasFocus();
}

function useLooking(): boolean {
  const [looking, setLooking] = useState(isLooking);
  useEffect(() => {
    const update = () => setLooking(isLooking());
    window.addEventListener("focus", update);
    window.addEventListener("blur", update);
    document.addEventListener("visibilitychange", update);
    return () => {
      window.removeEventListener("focus", update);
      window.removeEventListener("blur", update);
      document.removeEventListener("visibilitychange", update);
    };
  }, []);
  return looking;
}

function notify(ev: AttentionEvent, openThread: (id: string) => void) {
  const title = ev.kind === "question" ? "An agent has a question" : "Turn finished";
  const body = ev.thread.title || "Untitled thread";
  if (inDesktopShell()) {
    try {
      void Promise.resolve(Events.Emit("at:notify", { title, body, threadId: ev.thread.id })).catch(() => undefined);
    } catch {
      /* not running under the desktop shell */
    }
    return;
  }
  if (!browserNotifyOn()) return;
  try {
    // The tag replaces an older notification for the same thread.
    const n = new Notification(title, { body, tag: ev.thread.id });
    n.onclick = () => {
      window.focus();
      openThread(ev.thread.id);
      n.close();
    };
  } catch {
    /* some browsers only allow notifications from a service worker */
  }
}

/**
 * useAttention watches the thread list for turns that finish or questions
 * asked while the user is not looking, notifies once per event, and keeps the
 * tab title and dock badge on the count of threads waiting for the user. The
 * thread the user is looking at never counts. `openThread` shows a thread
 * when its notification is clicked.
 */
export function useAttention({
  threads,
  activeId,
  openThread,
}: {
  threads: AttentionThread[];
  activeId: string | null;
  openThread: (id: string) => void;
}) {
  const looking = useLooking();
  const openRef = useRef(openThread);
  openRef.current = openThread;
  const snapRef = useRef<Map<string, Snap> | null>(null);
  // Threads that finished or asked while away. The daemon marks the open
  // thread seen the moment its turn ends, so its own flag cannot carry this.
  const [away, setAway] = useState<ReadonlySet<string>>(() => new Set());

  // The list is replaced wholesale on every poll, so this runs per refresh;
  // the work is one pass over the threads.
  useEffect(() => {
    const prev = snapRef.current;
    const { snap, events } = attentionEvents(threads.length > 0 ? prev : null, threads);
    if (threads.length > 0) snapRef.current = snap;
    const fresh = events.filter((ev) => !(isLooking() && ev.thread.id === activeId));
    if (fresh.length === 0) return;
    setAway((s) => new Set([...s, ...fresh.map((ev) => ev.thread.id)]));
    if (!isLooking()) fresh.forEach((ev) => notify(ev, (id) => openRef.current(id)));
  }, [threads, activeId]);

  // Looking at a thread clears it; so does the daemon reporting it handled
  // (seen or answered from another client).
  useEffect(() => {
    setAway((s) => {
      if (s.size === 0) return s;
      const next = new Set(s);
      if (looking && activeId) next.delete(activeId);
      for (const t of threads) if (t.id !== activeId && !needsAttention(t)) next.delete(t.id);
      return next.size === s.size ? s : next;
    });
  }, [threads, activeId, looking]);

  // App re-renders on every keystroke and streamed chunk; count only when the inputs move.
  const count = useMemo(
    () =>
      threads.filter(
        (t) => watched(t) && (needsAttention(t) || away.has(t.id)) && !(looking && t.id === activeId),
      ).length,
    [threads, away, looking, activeId],
  );
  const activeTitle = useMemo(() => threads.find((t) => t.id === activeId)?.title || null, [threads, activeId]);

  useEffect(() => {
    document.title = attentionTitle(count, activeTitle);
  }, [count, activeTitle]);

  useEffect(() => {
    if (!inDesktopShell()) return;
    try {
      void Promise.resolve(Events.Emit("at:badge", { count })).catch(() => undefined);
    } catch {
      /* not running under the desktop shell */
    }
  }, [count]);

  // A native notification click arrives from the shell with its thread.
  useEffect(() => {
    if (!inDesktopShell()) return;
    const off = Events.On("at:notification-open", (event: { data?: { threadId?: string } }) => {
      const id = event.data?.threadId;
      if (id) openRef.current(id);
    });
    return () => {
      if (typeof off === "function") off();
    };
  }, []);
}

/** Browser notification state for the settings toggle. */
export function useBrowserNotify(): {
  supported: boolean;
  on: boolean;
  denied: boolean;
  toggle: () => void;
} {
  const supported = browserNotifySupported();
  const [on, setOn] = useState(browserNotifyOn);
  const denied = supported && Notification.permission === "denied";
  const toggle = () => {
    if (!supported) return;
    if (on) {
      setPref(BROWSER_NOTIFY_KEY, "0");
      setOn(false);
      return;
    }
    // Permission must be asked from this click; the browser ignores it otherwise.
    void Notification.requestPermission().then((perm) => {
      const granted = perm === "granted";
      setPref(BROWSER_NOTIFY_KEY, granted ? "1" : "0");
      setOn(granted);
    });
  };
  return { supported, on, denied, toggle };
}
