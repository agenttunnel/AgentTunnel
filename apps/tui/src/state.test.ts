import { describe, expect, it } from "vite-plus/test";

import {
  initialTuiState,
  moveSelection,
  reduceKey,
  selectedThread,
  visibleLines,
  withThreads,
  type TuiState,
} from "./state.ts";

const threads = [
  { id: "a", title: "First thread", projectTitle: "AgentTunnel", branch: "main", fleet: null },
  { id: "b", title: "Second thread", projectTitle: "AgentTunnel", branch: null, fleet: null },
];

describe("reduceKey", () => {
  it("moves the cursor inside the list and opens the selected thread", () => {
    let state = withThreads(initialTuiState, threads);
    expect(reduceKey(state, { name: "down" }).state.selectedIndex).toBe(1);
    expect(reduceKey(state, { name: "down" }).state.selectedIndex).toBe(1);
    state = reduceKey(state, { name: "down" }).state;
    expect(selectedThread(state)?.id).toBe("b");
    expect(reduceKey(state, { name: "enter" }).effect).toEqual({ kind: "open" });
  });

  it("opens on the name terminals actually send for Enter", () => {
    const state = withThreads(initialTuiState, threads);
    expect(reduceKey(state, { name: "return" }).effect).toEqual({ kind: "open" });
  });

  it("offers nothing to open when the list is empty", () => {
    expect(reduceKey(initialTuiState, { name: "enter" }).effect).toBeNull();
  });

  it("collects input in the thread view and sends it on enter", () => {
    let state: TuiState = { ...initialTuiState, view: "thread", openThreadId: "a" };
    state = reduceKey(state, { text: "h" }).state;
    state = reduceKey(state, { text: "i" }).state;
    expect(state.input).toBe("hi");
    const sent = reduceKey(state, { name: "enter" });
    expect(sent.effect).toEqual({ kind: "send", text: "hi" });
    expect(sent.state.input).toBe("");
  });

  it("ignores an empty send and an empty backspace", () => {
    const state: TuiState = { ...initialTuiState, view: "thread", openThreadId: "a" };
    expect(reduceKey(state, { name: "enter" }).effect).toBeNull();
    expect(reduceKey(state, { name: "backspace" }).state.input).toBe("");
  });

  it("interrupts with ctrl-i, goes back with escape, quits with ctrl-c", () => {
    const state: TuiState = { ...initialTuiState, view: "thread", openThreadId: "a" };
    expect(reduceKey(state, { name: "ctrl-i" }).effect).toEqual({ kind: "interrupt" });
    expect(reduceKey(state, { name: "ctrl-c" }).effect).toEqual({ kind: "quit" });
    const back = reduceKey(state, { name: "escape" });
    expect(back.state.view).toBe("threads");
    expect(back.effect).toEqual({ kind: "refresh" });
  });

  it("scrolls the transcript with the arrows once a thread is open", () => {
    const state: TuiState = {
      ...initialTuiState,
      view: "thread",
      lines: Array.from({ length: 10 }, (_, index) => ({
        kind: "assistant" as const,
        text: `l${index}`,
      })),
    };
    const scrolled = moveSelection(state, 2);
    expect(scrolled.scrollback).toBe(2);
    expect(visibleLines(scrolled, 3).map((line) => line.text)).toEqual(["l5", "l6", "l7"]);
    expect(visibleLines(state, 3).map((line) => line.text)).toEqual(["l7", "l8", "l9"]);
  });
});
