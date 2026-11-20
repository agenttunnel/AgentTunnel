// Live, per-agent reads for the multi-agent views, kept out of App so a tick
// or a streamed chunk re-renders only the row that shows it.
//
// - useNow: one shared one-second clock, running only while some live timer
//   is mounted, so ten running agents cost one interval, and none once they end.
// - useLastAction: an agent's newest log line as a one-liner, read from the
//   live log the thread already streams (no extra requests).
// - agentFocusStore: the agent the user selected in the tree or graph; the
//   live log filters to it.

import { useCallback, useSyncExternalStore } from "react";
import { liveLogStore, type LiveLine } from "./liveLog";
import { createStore, useStoreField } from "./store";

const tickers = new Set<() => void>();
let timer = 0;
let now = Date.now();

function subscribeTick(listener: () => void) {
  tickers.add(listener);
  if (!timer) {
    now = Date.now();
    timer = window.setInterval(() => {
      now = Date.now();
      for (const l of tickers) l();
    }, 1000);
  }
  return () => {
    tickers.delete(listener);
    if (tickers.size === 0 && timer) {
      window.clearInterval(timer);
      timer = 0;
    }
  };
}
const noSubscribe = () => () => {};
const readNow = () => now;

/** The current time, ticking each second while `live`. Not live, it does not
 * tick: callers then measure to a fixed end instead. */
export function useNow(live: boolean): number {
  return useSyncExternalStore(live ? subscribeTick : noSubscribe, readNow);
}

// The newest line per agent, computed once per log update and shared by every row.
let lastLines: LiveLine[] | null = null;
let lastByAgent = new Map<string, LiveLine>();
function lastLineOf(agentId: string): LiveLine | undefined {
  const lines = liveLogStore.get().lines;
  if (lines !== lastLines) {
    lastLines = lines;
    lastByAgent = new Map();
    for (const l of lines) if (l.text.trim() !== "" || l.tool) lastByAgent.set(l.agentId, l);
  }
  return lastByAgent.get(agentId);
}

/** A log line as one short sentence: a tool call's summary, or prose's last line. */
export function actionText(l: Pick<LiveLine, "kind" | "text" | "tool">): string {
  let text: string;
  if (l.tool) {
    const first = l.text.split("\n", 1)[0].replace(/^(\$|→)\s+/, "");
    text = first || l.tool.name || "tool";
    if (l.tool.command) text = `$ ${text}`;
  } else {
    const rows = l.text.split("\n").map((s) => s.trim()).filter(Boolean);
    text = rows[rows.length - 1] ?? "";
    // Markdown markers read as noise in a one-liner.
    text = text.replace(/^(#+|[-*>]|\d+\.)\s+/, "").replace(/[*_`]/g, "");
  }
  return text.length > 140 ? `${text.slice(0, 139)}…` : text;
}

/** The agent's newest action, or "" when the log holds nothing from it. The
 * line object is stable until that agent writes, so other agents' output does
 * not re-render this row. */
export function useLastAction(agentId: string): string {
  const get = useCallback(() => lastLineOf(agentId), [agentId]);
  const line = useSyncExternalStore(liveLogStore.subscribe, get);
  return line ? actionText(line) : "";
}

/** The selected agent, scoped to the thread it was picked in. */
export const agentFocusStore = createStore({
  focus: null as { threadId: string; agentId: string } | null,
});
export const setAgentFocus = agentFocusStore.setter("focus");

/** The selected agent's id when it belongs to `threadId`. */
export function useFocusedAgent(threadId: string | null | undefined): string | null {
  const f = useStoreField(agentFocusStore, "focus");
  return f && threadId && f.threadId === threadId ? f.agentId : null;
}
