import { describe, expect, it } from "vite-plus/test";

import { renderFrame } from "./frame.ts";
import { visibleWidth } from "./theme.ts";
import {
  initialTuiState,
  reduceKey,
  withCatalogs,
  withOpenThread,
  withRunningState,
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

/** The spinner phases, so the test asserts the frame advances, not a glyph. */
const SPINNER_GLYPHS = ["◐", "◓", "◑", "◒"] as const;

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

  it("colours agents by state, spins the running ones, and links a retry", () => {
    const agents = [
      {
        id: "task_2",
        label: "planner",
        status: "running",
        activity: "reading files",
        model: "opencode/big-pickle",
        tokens: null,
        tools: null,
        elapsed: "1m 0s",
        attempt: null,
        outputPath: null,
        parentId: null,
      },
      {
        id: "task_3",
        label: "planner",
        status: "failed",
        activity: "ENOENT",
        model: null,
        tokens: null,
        tools: null,
        elapsed: null,
        attempt: 2,
        outputPath: null,
        parentId: "task_2",
      },
    ];
    const state: TuiState = {
      ...catalogs,
      view: "agents" as const,
      agents,
      agentIndex: 0,
      spinner: 1,
    };
    const frame = renderFrame(state, { rows: 18, cols: 100, now: 0 });

    // a running row is amber (38;2;224;175;104), a failed row red (38;2;247;118;142)
    const runningRow = frame.find((line) => line.includes("reading files")) ?? "";
    const failedRow = frame.find((line) => line.includes("ENOENT")) ?? "";
    // SGR triple, bold or not: 224;175;104 is amber, 247;118;142 is red.
    expect(runningRow).toContain("38;2;224;175;104m");
    expect(failedRow).toContain("38;2;247;118;142m");
    // live work spins, settled work does not
    expect(runningRow).toContain(SPINNER_GLYPHS.at(1) ?? "◓");
    // the retry says where it came from
    expect(strip(failedRow)).toContain("↳ retry of planner");
  });

  it("says why a picker is empty instead of showing a blank card", () => {
    const opening = reduceKey(type({ ...catalogs, projects: [] }, "/new"), { name: "enter" });
    const frame = renderFrame(opening.state, SIZE).map(strip).join("\n");
    expect(frame).toContain("nothing to choose yet");
    expect(frame).toContain("/add-project");
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

  it("keeps the running thread on top when a panel opens", () => {
    const opening = {
      snapshotSequence: 1,
      thread: {
        id: "t1",
        title: "explore",
        activities: [],
        messages: [
          { id: "m1", role: "user", text: "look into this repo", attachments: [] },
          { id: "m2", role: "assistant", text: "planning the exploration" },
        ],
        session: null,
      },
    } as unknown as Parameters<typeof withThreadSnapshot>[1];
    const opened = withThreadSnapshot(catalogs, opening);
    // The panel view is what the user asked for; the thread did not take it back.
    const split = { ...opened, view: "agents" as const, agents: [] };
    const frame = renderFrame(split, SIZE).map(strip);
    const joined = frame.join("\n");

    expect(joined).toContain("look into this repo");
    expect(frame[0]).toContain("explore");
    expect(joined).toContain("── agents ─");
    expect(joined).toContain("no sub-agents");
    // the thread band sits above the panel band
    const transcriptRow = frame.findIndex((line) => line.includes("look into this repo"));
    const separatorRow = frame.findIndex((line) => line.includes("── agents ─"));
    expect(transcriptRow).toBeLessThan(separatorRow);
    expect(frame.at(-1)).toContain("esc close panel");
    expect(frame).toHaveLength(SIZE.rows);
    for (const line of frame) expect(visibleWidth(line)).toBe(SIZE.cols);
  });

  it("tells you the workflow is running, and how far it has got", () => {
    const snapshot = {
      snapshotSequence: 1,
      thread: {
        id: "t1",
        title: "explore the repo",
        activities: [
          {
            id: "a1",
            kind: "task.started",
            createdAt: "2026-01-01T00:00:00.000Z",
            payload: {
              taskId: "task_1",
              taskType: "local_agent",
              agentKind: "agent",
              detail: "recon",
            },
          },
          {
            id: "a2",
            kind: "task.started",
            createdAt: "2026-01-01T00:00:01.000Z",
            payload: {
              taskId: "task_2",
              taskType: "local_agent",
              agentKind: "agent",
              detail: "second",
            },
          },
          {
            id: "a3",
            kind: "task.completed",
            createdAt: "2026-01-01T00:00:30.000Z",
            payload: {
              taskId: "task_2",
              agentKind: "agent",
              status: "completed",
              totalTokens: 1200,
            },
          },
        ],
        messages: [{ id: "m1", role: "user", text: "explore the repo", attachments: [] }],
        session: { status: "running", providerName: "opencode", threadId: "t1" },
      },
    } as unknown as Parameters<typeof withThreadSnapshot>[1];

    // Opening is the caller's decision now, so the test states it too.
    const opened = withOpenThread(withThreadSnapshot(catalogs, snapshot), "t1");
    const running = withRunningState(opened, "2026-01-01T00:00:00.000Z");
    const frame = renderFrame(running, {
      rows: 20,
      cols: 100,
      now: Date.parse("2026-01-01T00:02:14.000Z"),
    }).map(strip);
    // Last line is the hints; above it the two strip lines: state, then fleet.
    const stateLine = frame.at(-3) ?? "";
    const fleetLine = frame.at(-2) ?? "";
    expect(stateLine).toContain("working");
    expect(stateLine).toContain("2m 14s");
    expect(stateLine).toContain("opencode");
    expect(fleetLine).toContain("2 agents");
    expect(fleetLine).toContain("1/2");
    expect(frame.at(-1)).toContain("enter send");
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
