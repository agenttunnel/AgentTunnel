import { describe, expect, it } from "vite-plus/test";

import { renderFrame } from "./frame.ts";
import { visibleWidth } from "./theme.ts";
import {
  initialTuiState,
  withCatalogs,
  withThreadSnapshot,
  withThreads,
  type TuiState,
} from "./state.ts";

/**
 * The frame is the contract: exactly `rows` lines, each exactly `cols` wide, and
 * every surface in its own band. These are the assertions that keep the screen
 * honest — they are what the layout bugs would have failed.
 */

const SIZE = { rows: 24, cols: 80 } as const;

const catalogs = withCatalogs(initialTuiState, {
  projects: [
    { id: "p1", title: "cai_api", workspaceRoot: "/tmp/cai_api", defaultModelSelection: null },
    {
      id: "p2",
      title: "rate-limiting",
      workspaceRoot: "/tmp/rate-limiting",
      defaultModelSelection: null,
    },
  ],
  providers: [
    {
      instanceId: "agenttunnel",
      label: "AgentTunnel",
      driver: "agenttunnel",
      models: [{ slug: "opencode/big-pickle", name: "big pickle" }],
    },
  ],
});

const strip = (line: string): string => line.replace(/\u001b\[[0-9;]*m/gu, "");

const type = (state: TuiState, text: string): TuiState => {
  let next = state;
  for (const character of text) {
    next = { ...next, input: next.input + character };
  }
  return next;
};

describe("renderFrame", () => {
  it("always fills the terminal exactly", () => {
    for (const size of [SIZE, { rows: 10, cols: 40 }, { rows: 50, cols: 120 }]) {
      for (const state of [
        catalogs,
        type(catalogs, "/"),
        type(catalogs, "/new"),
        { ...catalogs, view: "settings" as const },
        { ...catalogs, view: "agenttunnel" as const },
        { ...catalogs, agents: [], view: "agents" as const },
      ]) {
        const frame = renderFrame(state, size);
        expect(frame).toHaveLength(size.rows);
        for (const line of frame) expect(visibleWidth(line)).toBe(size.cols);
      }
    }
  });

  it("puts the palette on its own line per command, inside a card", () => {
    const frame = renderFrame(type(catalogs, "/"), SIZE);
    const text = frame.map(strip);
    const commandRows = text.filter((line) =>
      /[▸ ] \/(new|model|provider|settings|help|quit)/u.test(line),
    );
    expect(commandRows.length).toBeGreaterThan(3);
    const cardTop = text.findIndex((line) => line.includes("╭"));
    const cardBottom = text.findIndex((line) => line.includes("╰"));
    expect(cardTop).toBeGreaterThan(0);
    expect(cardBottom).toBeGreaterThan(cardTop);
    // every command row sits inside the card, one per line
    for (const row of commandRows) expect(text.indexOf(row)).toBeGreaterThan(cardTop);
    for (const row of commandRows) expect(text.indexOf(row)).toBeLessThan(cardBottom);
  });

  it("draws the card's borders intact on every row", () => {
    const frame = renderFrame(type(catalogs, "/"), SIZE).map(strip);
    const cardRows = frame.filter((line) => line.includes("│"));
    expect(cardRows.length).toBeGreaterThan(3);
    for (const row of cardRows) {
      expect(row.trimEnd().endsWith("│")).toBe(true);
      expect(row).not.toContain("…│");
    }
  });

  it("shows the composer only where typing means something", () => {
    const threadFrame = renderFrame({ ...catalogs, view: "thread" as const }, SIZE);
    expect(threadFrame.some((line) => strip(line).startsWith("❯"))).toBe(true);
    const listFrame = renderFrame(catalogs, SIZE);
    expect(listFrame.some((line) => strip(line).startsWith("❯"))).toBe(false);
  });

  it("keeps every section in its own band, with the hints last", () => {
    const frame = renderFrame(
      { ...catalogs, view: "thread" as const, recap: "Goal: ship it" },
      SIZE,
    );
    const text = frame.map(strip);
    expect(text[0]).toContain("AgentTunnel ·");
    expect(text.at(-1)).toContain("enter send");
    const recapRow = text.findIndex((line) => line.includes("※"));
    expect(recapRow).toBeGreaterThan(0);
    expect(recapRow).toBeLessThan(SIZE.rows - 2);
  });

  it("truncates instead of wrapping, so no line ever exceeds the width", () => {
    const long = "x".repeat(500);
    const state: TuiState = withThreads({ ...catalogs, view: "threads" as const }, [
      { id: "t1", title: long, projectTitle: long, branch: null, fleet: null },
    ]);
    const frame = renderFrame(state, SIZE);
    for (const line of frame) expect(visibleWidth(line)).toBeLessThanOrEqual(SIZE.cols);
    expect(frame).toHaveLength(SIZE.rows);
  });

  it("survives a narrow terminal without losing the cursor row", () => {
    const narrow = { rows: 12, cols: 32 } as const;
    const frame = renderFrame(type(catalogs, "/"), narrow);
    expect(frame).toHaveLength(12);
    for (const line of frame) expect(visibleWidth(line)).toBe(32);
    expect(frame.map(strip).join("\n")).toContain("/");
  });

  it("summarises the fleet in the header once a thread has agents", () => {
    const snapshot = {
      snapshotSequence: 1,
      thread: {
        id: "t1",
        title: "explore",
        activities: [
          {
            id: "a1",
            kind: "task.started",
            at: "2026-01-01T00:00:00.000Z",
            payload: {
              taskId: "task_1",
              taskType: "local_agent",
              agentKind: "agent",
              detail: "recon",
              role: "planner",
            },
          },
        ],
        messages: [],
        session: null,
      },
    } as unknown as Parameters<typeof withThreadSnapshot>[1];
    const state = withThreadSnapshot({ ...catalogs, view: "thread" as const }, snapshot);
    const header = strip(renderFrame(state, SIZE)[0] ?? "");
    expect(header).toContain("1 agent");
  });
});
