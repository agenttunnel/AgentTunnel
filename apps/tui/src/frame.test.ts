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
        harness: null,
        inputTokens: null,
        cachedTokens: null,
        outputTokens: null,
        costUsd: null,
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
        harness: null,
        inputTokens: null,
        cachedTokens: null,
        outputTokens: null,
        costUsd: null,
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

    // a running row is amber (38;2;245;167;66), a failed row red (38;2;255;107;129)
    const runningRow = frame.find((line) => line.includes("reading files")) ?? "";
    const failedRow = frame.find((line) => line.includes("ENOENT")) ?? "";
    // SGR triple, bold or not: 245;167;66 is amber, 255;107;129 is red.
    expect(runningRow).toContain("38;2;245;167;66m");
    expect(failedRow).toContain("38;2;255;107;129m");
    // live work spins, settled work does not
    expect(runningRow).toContain(SPINNER_GLYPHS.at(1) ?? "◓");
    // the retry says where it came from
    expect(strip(failedRow)).toContain("↳ retry of planner");
  });

  it("names the failure and shows the orchestrator when a run dies early", () => {
    const snapshot = {
      snapshotSequence: 1,
      thread: {
        id: "t1",
        title: "Explore the repo",
        activities: [],
        messages: [{ id: "m1", role: "user", text: "Explore the repo", attachments: [] }],
        session: {
          threadId: "t1",
          status: "error",
          providerName: "agenttunnel",
          runtimeMode: "full-access",
          lastError: "opencode exited with code 1: model not available",
          updatedAt: "2026-01-01T00:00:00.000Z",
        },
      },
    } as unknown as Parameters<typeof withThreadSnapshot>[1];

    const opened = withOpenThread(withThreadSnapshot(catalogs, snapshot), "t1");
    const frame = renderFrame(opened, { rows: 20, cols: 100, now: 0 }).map(strip).join("\n");

    // the reason is in the transcript, not just an "error" label
    expect(frame).toContain("opencode exited with code 1: model not available");
    // and in the strip, where the state word is
    expect(frame).toContain("✗ · error");
    // the run itself is on the roster, so `l` has something to open
    const panel = renderFrame(
      { ...opened, view: "agents" as const },
      {
        rows: 20,
        cols: 100,
        now: 0,
      },
    )
      .map(strip)
      .join("\n");
    expect(panel).toContain("orchestrator · failed");
  });

  it("reads a finished fleet as complete, not as zero of six", () => {
    const tasks = Array.from({ length: 6 }, (_, index) => [
      {
        id: `start-${index}`,
        kind: "task.started",
        createdAt: "2026-01-01T00:00:00.000Z",
        payload: {
          taskId: `task_${index}`,
          taskType: "local_agent",
          agentKind: "agent",
          detail: `agent ${index}`,
        },
      },
      {
        id: `done-${index}`,
        kind: "task.completed",
        createdAt: "2026-01-01T00:01:00.000Z",
        payload: { taskId: `task_${index}`, agentKind: "agent", status: "completed" },
      },
    ]).flat();
    const snapshot = {
      snapshotSequence: 1,
      thread: {
        id: "t1",
        title: "explore the repo",
        activities: tasks,
        messages: [{ id: "m1", role: "user", text: "explore the repo", attachments: [] }],
        session: { threadId: "t1", status: "ready", providerName: "agenttunnel" },
      },
    } as unknown as Parameters<typeof withThreadSnapshot>[1];

    const opened = withOpenThread(withThreadSnapshot(catalogs, snapshot), "t1");
    const frame = renderFrame(opened, { rows: 20, cols: 110, now: 0 }).map(strip);
    const fleetLine = frame.at(-2) ?? "";
    expect(fleetLine).toContain("6/6 settled");
    expect(fleetLine).not.toContain("0/6");
    // Every cell of the ribbon is a settled agent, so the fleet reads all green.
    expect(fleetLine).toContain("▰▰▰▰▰▰");
  });

  it("greets on open, and stands aside once you move", () => {
    const frame = renderFrame(
      { ...catalogs, threads: catalogs.threads, welcome: true },
      { rows: 24, cols: 100, now: Date.parse("2026-01-01T00:00:00.000Z") },
    ).map(strip);
    const joined = frame.join("\n");
    expect(joined).toContain("Welcome back!");
    expect(joined).toContain("AgentTunnel");
    expect(joined).toContain("Tips");
    expect(joined).toContain("/     commands, /new starts a thread");
    expect(joined).toContain("Workspace");
    expect(joined).toContain("cai_api");

    // a deliberate move puts it away for good
    const moving = reduceKey({ ...catalogs, welcome: true }, { name: "down" });
    expect(moving.state.welcome).toBe(false);
    const after = renderFrame(moving.state, { rows: 24, cols: 100, now: 0 }).map(strip).join("\n");
    expect(after).not.toContain("Welcome back!");
  });

  it("says why a picker is empty instead of showing a blank card", () => {
    const opening = reduceKey(type({ ...catalogs, projects: [] }, "/new"), { name: "enter" });
    const frame = renderFrame(opening.state, SIZE).map(strip).join("\n");
    expect(frame).toContain("nothing to choose yet");
    expect(frame).toContain("/add-project");
  });

  it("highlights the selected menu row in the accent colour", () => {
    // The picker's cursor row is the one about to be chosen.
    const opening = reduceKey(type({ ...catalogs, view: "threads" as const }, "/new"), {
      name: "enter",
    });
    const frame = renderFrame(opening.state, { rows: 20, cols: 100, now: 0 });
    const selected = frame.find((line) => line.includes("▸") && line.includes("cai_api")) ?? "";
    expect(selected).toContain("38;2;217;119;87m");
    expect(selected).toContain("1;38;2;217;119;87m");

    // and so is the palette's
    const palette = renderFrame(type(catalogs, "/"), { rows: 20, cols: 100, now: 0 });
    const command = palette.find((line) => line.includes("▸") && line.includes("/new")) ?? "";
    expect(command).toContain("1;38;2;217;119;87m");
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

  it("keeps the composer on every screen, home included", () => {
    const threadFrame = renderFrame({ ...catalogs, view: "thread" as const }, SIZE);
    expect(threadFrame.some((line) => strip(line).startsWith("│ >"))).toBe(true);
    // The home screen is where work starts, so it has the input too.
    const listFrame = renderFrame({ ...catalogs, welcome: false }, SIZE);
    expect(listFrame.some((line) => strip(line).startsWith("│ >"))).toBe(true);
  });

  it("centres the greeting instead of pinning it to the top", () => {
    const tall: TuiState = { ...catalogs, welcome: true, view: "threads" as const };
    const rows = renderFrame(tall, { rows: 40, cols: 100, now: 0 }).map(strip);
    const welcome = rows.findIndex((line) => line.includes("Welcome back!"));
    // Somewhere in the middle of the screen, not against the header.
    expect(welcome).toBeGreaterThan(6);
    expect(welcome).toBeLessThan(30);
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
      {
        id: "t1",
        title: long,
        projectTitle: long,
        branch: null,
        updatedAt: "2026-01-01T00:00:00.000Z",
        fleet: null,
      },
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
    expect(fleetLine).toContain("1/2 settled");
    expect(fleetLine).toContain("1 running");
    expect(frame.at(-1)).toContain("enter send");
  });

  it("believes the server's running session, so the glyph and the word agree", () => {
    // No local running state and no agents: only the session says work is live.
    const live = {
      snapshotSequence: 1,
      thread: {
        id: "t1",
        title: "hey",
        activities: [],
        messages: [{ id: "m1", role: "user", text: "hey", attachments: [] }],
        session: { threadId: "t1", status: "running", providerName: "agenttunnel" },
      },
    } as unknown as Parameters<typeof withThreadSnapshot>[1];
    const opened = withOpenThread(withThreadSnapshot(catalogs, live), "t1");
    const rows = renderFrame(opened, { rows: 20, cols: 100, now: 0 }).map(strip);
    // From the bottom: hints, the fleet ledger, then the state line.
    const stateLine = rows.at(-3) ?? "";
    expect(stateLine).toContain("working");
    // The amber spinner, not the pause glyph that used to sit beside "working".
    expect(SPINNER_GLYPHS.some((glyph) => stateLine.includes(glyph))).toBe(true);
    expect(stateLine).not.toContain("⏸");
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
