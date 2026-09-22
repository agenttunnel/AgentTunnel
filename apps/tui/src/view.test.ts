import { describe, expect, it } from "vite-plus/test";

import { initialTuiState, withThread, withThreads } from "./state.ts";
import { renderTui } from "./view.ts";

const size = { columns: 60, rows: 12 };

describe("renderTui", () => {
  it("draws exactly one line per row, padded to the terminal width", () => {
    const frame = renderTui(withThreads(initialTuiState, []), size);
    expect(frame).toHaveLength(size.rows);
    expect(frame.every((line) => line.length === size.columns)).toBe(true);
  });

  it("lists threads with a cursor on the selection and the key hints below", () => {
    // Wide enough that the whole key hint fits: clipping is asserted separately.
    const frame = renderTui(
      withThreads(initialTuiState, [
        { id: "a", title: "First", projectTitle: "AgentTunnel", branch: "main", fleet: null },
        { id: "b", title: "Second", projectTitle: "AgentTunnel", branch: null, fleet: null },
      ]),
      { columns: 120, rows: 12 },
    );
    expect(frame[0]).toContain("threads (2)");
    expect(frame[1]).toContain("▸ First · main");
    expect(frame[2]).toContain("Second");
    expect(frame.at(-1)).toContain("↑/↓ move · enter open · ctrl-r refresh · ctrl-c quit");
  });

  it("shows the recap line and the composer once a thread is open", () => {
    const state = withThread(
      { ...initialTuiState, input: "keep going" },
      {
        id: "a",
        title: "Recap thread",
        recap: "Goal: ship the recap  ·  1 agent working · executor: wiring the row",
        lines: [
          { kind: "user", text: "do the thing" },
          { kind: "agent", text: "executor · running — wiring the row" },
        ],
      },
    );
    const frame = renderTui(state, size);
    expect(frame[0]).toContain("Recap thread");
    expect(frame[1]).toContain("› do the thing");
    expect(frame[2]).toContain("· executor · running");
    expect(frame.at(-3)).toContain("※ recap: Goal: ship the recap");
    expect(frame.at(-2)).toContain("❯ keep going");
  });

  it("clips long lines to the terminal width instead of wrapping", () => {
    const state = withThread(initialTuiState, {
      id: "a",
      title: "x".repeat(200),
      recap: null,
      lines: [{ kind: "assistant", text: "y".repeat(400) }],
    });
    const frame = renderTui(state, { columns: 40, rows: 8 });
    expect(frame.every((line) => line.length === 40)).toBe(true);
  });
});
