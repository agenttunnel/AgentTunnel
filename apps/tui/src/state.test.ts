import { describe, expect, it } from "vite-plus/test";
import { ProviderInstanceId } from "@t3tools/contracts";

import type { TuiProjectRow, TuiProviderRow, TuiServerSettingRow } from "./format.ts";
import {
  activeProvider,
  commandSuggestions,
  initialTuiState,
  moveSelection,
  pickerItems,
  reduceKey,
  resolveModelSelection,
  selectedThread,
  visibleLines,
  withCatalogs,
  withModelSelection,
  withOpenThread,
  withPreferences,
  withThreadEvent,
  withThreadSnapshot,
  withThread,
  withThreads,
  type TuiState,
} from "./state.ts";

const threads = [
  { id: "a", title: "First thread", projectTitle: "AgentTunnel", branch: "main", fleet: null },
  { id: "b", title: "Second thread", projectTitle: "AgentTunnel", branch: null, fleet: null },
];

const codex = ProviderInstanceId.make("codex");
const claude = ProviderInstanceId.make("claude");

const projects: ReadonlyArray<TuiProjectRow> = [
  { id: "p1", title: "Alpha", workspaceRoot: "/code/alpha", defaultModelSelection: null },
  { id: "p2", title: "Beta", workspaceRoot: "/code/beta", defaultModelSelection: null },
];

const projectWithDefault: TuiProjectRow = {
  id: "p3",
  title: "Gamma",
  workspaceRoot: "/code/gamma",
  defaultModelSelection: { instanceId: claude, model: "sonnet" },
};

const providers: ReadonlyArray<TuiProviderRow> = [
  {
    instanceId: "codex",
    label: "Codex",
    driver: "codex",
    models: [
      { slug: "gpt-5", name: "GPT-5" },
      { slug: "gpt-5-mini", name: "GPT-5 mini" },
    ],
  },
  {
    instanceId: "claude",
    label: "Claude",
    driver: "claudeAgent",
    models: [{ slug: "sonnet", name: "Sonnet" }],
  },
];

const serverSettings: ReadonlyArray<TuiServerSettingRow> = [
  { key: "enableLegacyTokenStreaming", label: "stream tokens as they arrive", value: true },
  { key: "enableProviderUpdateChecks", label: "check for provider updates", value: "auto" },
];

const catalogs = withCatalogs(initialTuiState, { projects, providers, serverSettings });

/** Deliver a run of characters one key press at a time, as a terminal would. */
function type(state: TuiState, text: string): TuiState {
  let next = state;
  for (const character of text) {
    next = reduceKey(next, { text: character }).state;
  }
  return next;
}

/** A thread view with nothing typed and no draft. */
function threadView(): TuiState {
  return withThread(catalogs, { id: "a", title: "First thread", recap: null, lines: [] });
}

/** The project picker, opened the way a user opens it: `/new` then Enter. */
function projectPicker(): TuiState {
  return reduceKey(type(withThreads(catalogs, threads), "/new"), { name: "enter" }).state;
}

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

  it("offers the palette for a bare slash and moves the highlight with the arrows", () => {
    const state = type(withThreads(catalogs, threads), "/");
    expect(commandSuggestions(state).map((row) => row.id)).toEqual([
      "new",
      "model",
      "provider",
      "settings",
      "agenttunnel",
      "add-project",
      "help",
      "refresh",
      "quit",
    ]);
    const moved = reduceKey(state, { name: "down" });
    expect(moved.state.commandIndex).toBe(1);
    // The arrows belong to the suggestions while they are showing; the thread
    // cursor must not move underneath them.
    expect(moved.state.selectedIndex).toBe(0);
    expect(reduceKey(moved.state, { name: "up" }).state.commandIndex).toBe(0);
  });

  it("asks for a path before registering a project", () => {
    const missing = reduceKey(type(threadView(), "/add-project"), { name: "enter" });
    expect(missing.effect).toBeNull();
    expect(missing.state.status).toBe("usage: /add-project <path>");

    const given = reduceKey(type(threadView(), "/add-project /tmp/scratch"), { name: "enter" });
    expect(given.effect).toEqual({ kind: "addProject", workspaceRoot: "/tmp/scratch" });
  });

  it("keeps every matching command and wraps the highlight at both ends", () => {
    const state = type(threadView(), "/");
    const ids = commandSuggestions(state).map((row) => row.id);
    // The palette lists them all: nothing is trimmed off the end.
    expect(ids.at(-1)).toBe("quit");
    expect(ids.length).toBeGreaterThan(1);

    expect(reduceKey(state, { name: "up" }).state.commandIndex).toBe(ids.length - 1);
    const atEnd = { ...state, commandIndex: ids.length - 1 };
    expect(reduceKey(atEnd, { name: "down" }).state.commandIndex).toBe(0);
  });

  it("lets a streamed snapshot update content without stealing the view", () => {
    const opening = {
      thread: { id: "t1", title: "explore", activities: [], messages: [], session: null },
      snapshotSequence: 1,
    } as unknown as Parameters<typeof withThreadSnapshot>[1];
    const onAgents = { ...withThreadSnapshot(initialTuiState, opening), view: "agents" as const };
    const later = { ...opening, snapshotSequence: 2 } as typeof opening;
    expect(withThreadSnapshot(onAgents, later).view).toBe("agents");
    // A project event is one the thread reducer leaves alone; the point here is
    // that folding it in does not move the user off the panel they opened.
    const unrelated = { sequence: 2, type: "project.created", payload: {} } as never;
    expect(withThreadEvent(onAgents, unrelated).view).toBe("agents");
  });

  it("starts a thread on the workflow provider, not the first in the catalog", () => {
    // A catalog where a bare agent provider sits first, as it does in practice.
    const withCodex = withCatalogs(initialTuiState, {
      providers: [
        {
          instanceId: "codex",
          label: "Codex",
          driver: "codex",
          models: [{ slug: "gpt-5", name: "GPT-5" }],
        },
        {
          instanceId: "agenttunnel",
          label: "AgentTunnel",
          driver: "agenttunnel",
          models: [{ slug: "opencode/big-pickle", name: "big pickle", isDefault: true }],
        },
      ],
    });
    expect(activeProvider(withCodex)?.instanceId).toBe("agenttunnel");
    expect(resolveModelSelection(withCodex, null)).toEqual({
      instanceId: ProviderInstanceId.make("agenttunnel"),
      model: "opencode/big-pickle",
    });
    // an explicit choice still wins
    const chosen = withModelSelection(withCodex, {
      instanceId: ProviderInstanceId.make("codex"),
      model: "gpt-5",
    });
    expect(resolveModelSelection(chosen, null)?.instanceId).toBe("codex");
  });

  it("ignores a stored default whose instance is not configured", () => {
    // Projects carry defaults written by other clients; this one names codex,
    // which this environment does not have. Sending it makes the thread
    // unroutable, so it must be skipped rather than honoured.
    const catalog = withCatalogs(initialTuiState, {
      providers: [
        {
          instanceId: "agenttunnel",
          label: "AgentTunnel",
          driver: "agenttunnel",
          models: [{ slug: "opencode/big-pickle", name: "big pickle", isDefault: true }],
        },
      ],
    });
    const project = {
      id: "p1",
      title: "cai_api",
      workspaceRoot: "/tmp/cai_api",
      defaultModelSelection: {
        instanceId: ProviderInstanceId.make("codex"),
        model: "gpt-5.4",
      },
    };
    expect(resolveModelSelection(catalog, project)).toEqual({
      instanceId: ProviderInstanceId.make("agenttunnel"),
      model: "opencode/big-pickle",
    });

    // a default this environment can route is still honoured
    const routable = withCatalogs(catalog, {
      providers: [
        ...catalog.providers,
        {
          instanceId: "codex",
          label: "Codex",
          driver: "codex",
          models: [{ slug: "gpt-5.4", name: "GPT-5.4" }],
        },
      ],
    });
    expect(resolveModelSelection(routable, project)?.instanceId).toBe("codex");
  });

  it("opens a thread only when the caller says so", () => {
    expect(withOpenThread(initialTuiState, "t1")).toMatchObject({
      view: "thread",
      openThreadId: "t1",
    });
  });

  it("wraps picker navigation at both ends", () => {
    const opened = reduceKey(type(threadView(), "/new"), { name: "enter" }).state;
    const items = opened.picker?.items ?? [];
    expect(items.length).toBeGreaterThan(1);

    expect(reduceKey(opened, { name: "up" }).state.picker?.index).toBe(items.length - 1);
    const atEnd = {
      ...opened,
      picker: opened.picker === null ? null : { ...opened.picker, index: items.length - 1 },
    };
    expect(reduceKey(atEnd, { name: "down" }).state.picker?.index).toBe(0);
  });

  it("runs the highlighted suggestion on enter instead of sending the text", () => {
    const state = type(withThreads(catalogs, threads), "/new");
    const result = reduceKey(state, { name: "enter" });
    expect(result.effect).toBeNull();
    expect(result.state.input).toBe("");
    expect(result.state.picker?.kind).toBe("project");
    expect(result.state.picker?.items.map((item) => item.id)).toEqual(["p1", "p2"]);
  });
});

describe("the project picker", () => {
  it("closes on escape without touching the view", () => {
    const state = projectPicker();
    const closed = reduceKey(state, { name: "escape" });
    expect(closed.effect).toBeNull();
    expect(closed.state.picker).toBeNull();
    expect(closed.state.view).toBe("threads");
  });

  it("moves the index with the arrows and filters as the user types", () => {
    const state = projectPicker();
    const down = reduceKey(state, { name: "down" });
    expect(down.state.picker?.index).toBe(1);
    // Wrapping is asserted in its own test; here the middle of the list moves.
    expect(reduceKey(down.state, { name: "up" }).state.picker?.index).toBe(0);

    const filtered = type(down.state, "bet");
    expect(filtered.picker?.query).toBe("bet");
    expect(pickerItems(filtered).map((item) => item.id)).toEqual(["p2"]);
  });

  it("starts a draft on the chosen project and clears the picker", () => {
    const opened = reduceKey(projectPicker(), { name: "enter" });
    expect(opened.effect).toBeNull();
    expect(opened.state.picker).toBeNull();
    expect(opened.state.view).toBe("thread");
    expect(opened.state.lines).toEqual([]);
    expect(opened.state.draft?.projectId).toBe("p1");
    expect(opened.state.draft?.projectTitle).toBe("Alpha");
  });
});

describe("the draft composer", () => {
  it("creates the thread with the first line as the title and clears the input", () => {
    let state = reduceKey(type(withThreads(catalogs, threads), "/new"), { name: "enter" }).state;
    state = reduceKey(state, { name: "down" }).state;
    state = reduceKey(state, { name: "enter" }).state;
    expect(state.draft?.projectId).toBe("p2");

    const message = `${"a".repeat(100)}\nsecond line`;
    const sent = reduceKey(type(state, message), { name: "enter" });
    expect(sent.effect).toEqual({
      kind: "createThread",
      projectId: "p2",
      title: "a".repeat(80),
      text: message,
    });
    expect(sent.state.input).toBe("");
  });

  it("keeps the whole message while titling from the first line", () => {
    let state = reduceKey(type(withThreads(catalogs, threads), "/new"), { name: "enter" }).state;
    state = reduceKey(state, { name: "enter" }).state;
    const sent = reduceKey(type(state, "fix the parser\nand the tests"), { name: "enter" });
    expect(sent.effect).toEqual({
      kind: "createThread",
      projectId: "p1",
      title: "fix the parser",
      text: "fix the parser\nand the tests",
    });
  });
});

describe("unknown commands", () => {
  it("reports the command in the status line and sends nothing", () => {
    const state = type(threadView(), "/nope");
    const result = reduceKey(state, { name: "enter" });
    expect(result.effect).toBeNull();
    expect(result.state.status).toBe("unknown command: /nope");
    expect(result.state.input).toBe("");
  });
});

describe("the settings screen", () => {
  const settingsState = (): TuiState => ({ ...catalogs, view: "settings" });

  it("opens from /settings in a thread", () => {
    const opened = reduceKey(type(threadView(), "/settings"), { name: "enter" });
    expect(opened.state.view).toBe("settings");
    expect(opened.effect).toBeNull();
  });

  it("flips the agent-details preference on its row", () => {
    // Rows: AgentTunnel roles, provider, model, agent details, then the server's.
    const state = { ...settingsState(), settingsIndex: 3 };
    const toggled = reduceKey(state, { name: "enter" });
    expect(toggled.state.preferences.showAgentDetails).toBe(false);
    expect(toggled.effect).toEqual({
      kind: "savePreferences",
      patch: { showAgentDetails: false },
    });
  });

  it("flips a boolean server setting and asks the server to save it", () => {
    const state: TuiState = { ...settingsState(), settingsIndex: 4 };
    const toggled = reduceKey(state, { name: "enter" });
    expect(toggled.state.serverSettings[0]?.value).toBe(false);
    expect(toggled.effect).toEqual({
      kind: "saveServerSetting",
      key: "enableLegacyTokenStreaming",
      value: false,
    });
  });

  it("leaves a server setting row alone when its value is not boolean", () => {
    // Rows: AgentTunnel roles, provider, model, agent details, the boolean
    // server setting, then the fixture's string-valued one.
    const state: TuiState = { ...settingsState(), settingsIndex: 5 };
    const result = reduceKey(state, { name: "enter" });
    expect(result.effect).toBeNull();
    expect(result.state.serverSettings).toEqual(serverSettings);
  });
});

describe("resolveModelSelection", () => {
  const preferences = {
    defaultProvider: null,
    defaultModel: null,
    showAgentDetails: true,
  };

  it("prefers the session's own selection over the preference and the project", () => {
    const state = withPreferences(withCatalogs(initialTuiState, { providers }), {
      ...preferences,
      defaultProvider: "codex",
      defaultModel: "gpt-5-mini",
    });
    const selection = resolveModelSelection(
      { ...state, modelSelection: { instanceId: claude, model: "sonnet" } },
      projectWithDefault,
    );
    expect(selection).toEqual({ instanceId: claude, model: "sonnet" });
  });

  it("falls back to the preferred model when the session has none", () => {
    const state = withPreferences(withCatalogs(initialTuiState, { providers }), {
      ...preferences,
      defaultProvider: "claude",
      defaultModel: "sonnet",
    });
    expect(resolveModelSelection(state, projectWithDefault)).toEqual({
      instanceId: claude,
      model: "sonnet",
    });
  });

  it("falls back to the project default when no preference matches", () => {
    const state = withPreferences(withCatalogs(initialTuiState, { providers }), preferences);
    expect(resolveModelSelection(state, projectWithDefault)).toEqual({
      instanceId: claude,
      model: "sonnet",
    });
  });

  it("falls back to the first provider's model when nothing else is known", () => {
    const state = withPreferences(withCatalogs(initialTuiState, { providers }), preferences);
    expect(resolveModelSelection(state, null)).toEqual({ instanceId: codex, model: "gpt-5" });
  });

  it("uses the first configured provider when the preference names an unknown one", () => {
    const state = withPreferences(withCatalogs(initialTuiState, { providers }), {
      ...preferences,
      defaultProvider: "not-configured",
    });
    expect(activeProvider(state)?.instanceId).toBe("codex");
  });
});

describe("escape in the thread view", () => {
  it("clears the composer first and only leaves when it is already empty", () => {
    const typed = type(threadView(), "hi");
    const cleared = reduceKey(typed, { name: "escape" });
    expect(cleared.state.input).toBe("");
    expect(cleared.state.view).toBe("thread");
    expect(cleared.effect).toBeNull();

    const left = reduceKey(cleared.state, { name: "escape" });
    expect(left.state.view).toBe("threads");
    expect(left.effect).toEqual({ kind: "refresh" });
  });
});
