import type { TuiLine, TuiThreadRow } from "./format.ts";

/**
 * The TUI's state and the keys that move it. Kept free of terminal and RPC
 * concerns: `reduceKey` returns the effect the caller has to perform (send a
 * message, interrupt, refresh, quit) instead of performing it, so the whole
 * interaction model is testable without a PTY or a server.
 */

export type TuiEffect =
  | { readonly kind: "send"; readonly text: string }
  | { readonly kind: "interrupt" }
  | { readonly kind: "refresh" }
  | { readonly kind: "open" }
  | { readonly kind: "quit" }
  | null;

export interface TuiState {
  readonly view: "threads" | "thread";
  readonly threads: ReadonlyArray<TuiThreadRow>;
  readonly selectedIndex: number;
  readonly openThreadId: string | null;
  readonly title: string;
  readonly recap: string | null;
  readonly lines: ReadonlyArray<TuiLine>;
  readonly input: string;
  /** Last thing that happened, shown in the status line (errors included). */
  readonly status: string;
  readonly scrollback: number;
}

export const initialTuiState: TuiState = {
  view: "threads",
  threads: [],
  selectedIndex: 0,
  openThreadId: null,
  title: "",
  recap: null,
  lines: [],
  input: "",
  status: "connecting…",
  scrollback: 0,
};

export function withThreads(state: TuiState, threads: ReadonlyArray<TuiThreadRow>): TuiState {
  const selectedIndex = Math.min(state.selectedIndex, Math.max(0, threads.length - 1));
  return { ...state, threads, selectedIndex };
}

export function withThread(
  state: TuiState,
  thread: {
    readonly id: string;
    readonly title: string;
    readonly recap: string | null;
    readonly lines: ReadonlyArray<TuiLine>;
  },
): TuiState {
  return {
    ...state,
    view: "thread",
    openThreadId: thread.id,
    title: thread.title,
    recap: thread.recap,
    lines: thread.lines,
    scrollback: 0,
    status: `${thread.lines.length} entries`,
  };
}

export function withStatus(state: TuiState, status: string): TuiState {
  return { ...state, status };
}

export function moveSelection(state: TuiState, delta: number): TuiState {
  if (state.view !== "threads" || state.threads.length === 0) {
    const scrollback = Math.max(0, state.scrollback + delta);
    return { ...state, scrollback };
  }
  const next = Math.min(Math.max(0, state.selectedIndex + delta), state.threads.length - 1);
  return { ...state, selectedIndex: next };
}

/** The thread row the cursor is on, or null when the list is empty. */
export function selectedThread(state: TuiState): TuiThreadRow | null {
  return state.threads[state.selectedIndex] ?? null;
}

/**
 * One key press. `text` is the printable input when the terminal delivered a
 * character; named keys arrive as `name` (`enter`, `escape`, `up`, `down`,
 * `backspace`, `ctrl-c`, `ctrl-r`, `tab`).
 */
export function reduceKey(
  state: TuiState,
  key: { readonly name?: string; readonly text?: string },
): { readonly state: TuiState; readonly effect: TuiEffect } {
  // Terminals report the Enter key as `return`; the reducer speaks one name.
  const name = key.name === "return" ? "enter" : (key.name ?? "");
  if (name === "ctrl-c") {
    return { state, effect: { kind: "quit" } };
  }
  if (state.view === "threads") {
    switch (name) {
      case "up":
        return { state: moveSelection(state, -1), effect: null };
      case "down":
        return { state: moveSelection(state, 1), effect: null };
      case "enter":
        return { state, effect: selectedThread(state) === null ? null : { kind: "open" } };
      case "ctrl-r":
        return { state, effect: { kind: "refresh" } };
      default:
        return { state, effect: null };
    }
  }

  switch (name) {
    case "escape":
      return {
        state: { ...state, view: "threads", openThreadId: null },
        effect: { kind: "refresh" },
      };
    case "ctrl-r":
      return { state, effect: { kind: "refresh" } };
    case "up":
      return { state: moveSelection(state, -1), effect: null };
    case "down":
      return { state: moveSelection(state, 1), effect: null };
    case "backspace":
      return { state: { ...state, input: state.input.slice(0, -1) }, effect: null };
    case "ctrl-i":
      return { state, effect: { kind: "interrupt" } };
    case "enter": {
      const text = state.input.trim();
      if (text.length === 0) return { state, effect: null };
      return { state: { ...state, input: "" }, effect: { kind: "send", text } };
    }
    default:
      return key.text === undefined || key.text.length === 0
        ? { state, effect: null }
        : { state: { ...state, input: `${state.input}${key.text}` }, effect: null };
  }
}

/** The last `rows` transcript lines, newest at the bottom. */
export function visibleLines(state: TuiState, rows: number): ReadonlyArray<TuiLine> {
  const window = Math.max(1, rows);
  const end = Math.max(window, state.lines.length - state.scrollback);
  return state.lines.slice(Math.max(0, end - window), end);
}
