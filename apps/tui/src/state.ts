import { ProviderInstanceId, type ModelSelection } from "@t3tools/contracts";

import {
  commandByName,
  matchCommands,
  parseCommandInput,
  type TuiCommandName,
} from "./commands.ts";
import type {
  TuiLine,
  TuiProjectRow,
  TuiProviderRow,
  TuiServerSettingRow,
  TuiThreadRow,
} from "./format.ts";
import type { TuiPreferences } from "./preferences.ts";

/**
 * The TUI's state and the keys that move it. Kept free of terminal and RPC
 * concerns: `reduceKey` returns the effect the caller has to perform (send a
 * message, start a thread, interrupt, refresh, quit) instead of performing it,
 * so the whole interaction model is testable without a PTY or a server.
 *
 * Everything the palette shows comes from this state: projects, providers, and
 * models are folded in from the server, so opening a picker or running a
 * command is a pure transition like any other.
 */

export type TuiEffect =
  | { readonly kind: "send"; readonly text: string }
  | {
      readonly kind: "createThread";
      readonly projectId: string;
      readonly title: string;
      readonly text: string;
    }
  | { readonly kind: "interrupt" }
  | { readonly kind: "refresh" }
  | { readonly kind: "open" }
  | { readonly kind: "quit" }
  | { readonly kind: "savePreferences"; readonly patch: Partial<TuiPreferences> }
  | { readonly kind: "saveServerSetting"; readonly key: string; readonly value: boolean | string }
  | null;

export type TuiView = "threads" | "thread" | "settings";

export type TuiPickerKind = "command" | "project" | "provider" | "model" | "help";

export interface TuiPickerItem {
  readonly id: string;
  /** What the row shows: `/new`, a project title, a model name. */
  readonly label: string;
  readonly detail: string | null;
}

export interface TuiPicker {
  readonly kind: TuiPickerKind;
  readonly title: string;
  readonly items: ReadonlyArray<TuiPickerItem>;
  readonly index: number;
  /** Typed filter; commands match on name, the rest on label. */
  readonly query: string;
}

/** A thread being composed but not yet created. */
export interface TuiDraft {
  readonly projectId: string;
  readonly projectTitle: string;
  readonly modelSelection: ModelSelection | null;
}

export interface TuiState {
  readonly view: TuiView;
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
  /** Server-derived catalogs the palette offers. */
  readonly projects: ReadonlyArray<TuiProjectRow>;
  readonly providers: ReadonlyArray<TuiProviderRow>;
  readonly serverSettings: ReadonlyArray<TuiServerSettingRow>;
  /** The user's own settings, loaded at boot and written back on change. */
  readonly preferences: TuiPreferences;
  /** The model new turns use; null means "whatever the project or server defaults to". */
  readonly modelSelection: ModelSelection | null;
  readonly picker: TuiPicker | null;
  readonly draft: TuiDraft | null;
  /** Row the settings screen has highlighted. */
  readonly settingsIndex: number;
  /** Row the inline `/` suggestions have highlighted. */
  readonly commandIndex: number;
}

export const emptyPreferences: TuiPreferences = {
  defaultProvider: null,
  defaultModel: null,
  showAgentDetails: true,
};

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
  projects: [],
  providers: [],
  serverSettings: [],
  preferences: emptyPreferences,
  modelSelection: null,
  picker: null,
  draft: null,
  settingsIndex: 0,
  commandIndex: 0,
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
    draft: null,
    status: `${thread.lines.length} entries`,
  };
}

/** Fold the server's catalogs into state; the palette reads only from here. */
export function withCatalogs(
  state: TuiState,
  catalogs: {
    readonly projects?: ReadonlyArray<TuiProjectRow>;
    readonly providers?: ReadonlyArray<TuiProviderRow>;
    readonly serverSettings?: ReadonlyArray<TuiServerSettingRow>;
  },
): TuiState {
  return {
    ...state,
    projects: catalogs.projects ?? state.projects,
    providers: catalogs.providers ?? state.providers,
    serverSettings: catalogs.serverSettings ?? state.serverSettings,
  };
}

export function withPreferences(state: TuiState, preferences: TuiPreferences): TuiState {
  return { ...state, preferences };
}

export function withModelSelection(
  state: TuiState,
  modelSelection: ModelSelection | null,
): TuiState {
  return { ...state, modelSelection };
}

export function withStatus(state: TuiState, status: string): TuiState {
  return { ...state, status };
}

export function moveSelection(state: TuiState, delta: number): TuiState {
  if (state.view === "settings") {
    const count = settingsRows(state).length;
    if (count === 0) return state;
    return {
      ...state,
      settingsIndex: Math.min(Math.max(0, state.settingsIndex + delta), count - 1),
    };
  }
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

/** The provider new turns should use: the picked one, the default, or the first. */
export function activeProvider(state: TuiState): TuiProviderRow | null {
  const wanted = state.modelSelection?.instanceId ?? state.preferences.defaultProvider;
  if (wanted !== null && wanted !== undefined) {
    const match = state.providers.find((provider) => provider.instanceId === wanted);
    if (match !== undefined) return match;
  }
  return state.providers[0] ?? null;
}

/** The selection a new turn should carry, resolved from everything known. */
export function resolveModelSelection(
  state: TuiState,
  project: TuiProjectRow | null,
): ModelSelection | null {
  if (state.modelSelection !== null) return state.modelSelection;
  const provider = activeProvider(state);
  if (provider !== null && state.preferences.defaultModel !== null) {
    const model = provider.models.find((entry) => entry.slug === state.preferences.defaultModel);
    if (model !== undefined) {
      return { instanceId: ProviderInstanceId.make(provider.instanceId), model: model.slug };
    }
  }
  if (project !== null && project.defaultModelSelection !== null) {
    return project.defaultModelSelection;
  }
  if (provider === null) return null;
  const preferred =
    provider.models.find((model) => model.name === provider.label) ?? provider.models[0];
  return preferred === undefined
    ? null
    : { instanceId: ProviderInstanceId.make(provider.instanceId), model: preferred.slug };
}

/**
 * The command rows the composer offers for the text typed so far. A bare `/`
 * already opens the list — that is the whole point of typing it — so the query
 * is whatever follows the slash, up to the first space.
 */
export function commandSuggestions(state: TuiState): ReadonlyArray<TuiPickerItem> {
  if (!state.input.startsWith("/")) return [];
  const query = state.input.slice(1).split(/\s+/u)[0] ?? "";
  return matchCommands(query, state.view === "threads" ? "threads" : "thread").map((command) => ({
    id: command.name,
    label: `/${command.name}${command.argument === undefined ? "" : ` ${command.argument}`}`,
    detail: command.description,
  }));
}

/** The `/` suggestion the composer has highlighted, if any. */
export function highlightedSuggestion(state: TuiState): TuiPickerItem | null {
  const suggestions = commandSuggestions(state);
  return suggestions[Math.min(state.commandIndex, suggestions.length - 1)] ?? null;
}

/** The rows the settings screen lists: the user's settings first, then the server's. */
export function settingsRows(
  state: TuiState,
): ReadonlyArray<{ readonly key: string; readonly label: string; readonly value: string }> {
  const provider = activeProvider(state);
  const rows: Array<{ key: string; label: string; value: string }> = [
    { key: "defaultProvider", label: "provider", value: provider?.label ?? "not configured" },
    {
      key: "defaultModel",
      label: "model",
      value: state.modelSelection?.model ?? state.preferences.defaultModel ?? "provider default",
    },
    {
      key: "showAgentDetails",
      label: "agent details",
      value: state.preferences.showAgentDetails ? "shown" : "hidden",
    },
  ];
  for (const setting of state.serverSettings) {
    rows.push({
      key: setting.key,
      label: setting.key,
      value: typeof setting.value === "boolean" ? (setting.value ? "on" : "off") : setting.value,
    });
  }
  return rows;
}

function picker(
  kind: TuiPickerKind,
  title: string,
  items: ReadonlyArray<TuiPickerItem>,
  index = 0,
): TuiPicker {
  return { kind, title, items, index, query: "" };
}

function projectItems(state: TuiState): ReadonlyArray<TuiPickerItem> {
  return state.projects.map((project) => ({
    id: project.id,
    label: project.title,
    detail: project.workspaceRoot,
  }));
}

function providerItems(state: TuiState): ReadonlyArray<TuiPickerItem> {
  return state.providers.map((provider) => ({
    id: provider.instanceId,
    label: provider.label,
    detail: `${provider.driver} · ${provider.models.length} models`,
  }));
}

function modelItems(state: TuiState): ReadonlyArray<TuiPickerItem> {
  const provider = activeProvider(state);
  if (provider === null) return [];
  return provider.models.map((model) => ({
    id: model.slug,
    label: model.name,
    detail: provider.label,
  }));
}

/** Items matching the picker's typed query. */
export function pickerItems(state: TuiState): ReadonlyArray<TuiPickerItem> {
  const current = state.picker;
  if (current === null) return [];
  const query = current.query.trim().toLowerCase();
  if (query.length === 0) return current.items;
  return current.items.filter((item) => item.label.toLowerCase().includes(query));
}

/** Entering a picker keeps the state machine honest: the rows come from state. */
function openPickerFor(state: TuiState, kind: TuiPickerKind): TuiState {
  switch (kind) {
    case "command":
      return {
        ...state,
        picker: picker(
          "command",
          "commands",
          matchCommands("", state.view === "threads" ? "threads" : "thread").map((command) => ({
            id: command.name,
            label: `/${command.name}`,
            detail: command.description,
          })),
        ),
      };
    case "project":
      return { ...state, picker: picker("project", "start a thread in", projectItems(state)) };
    case "provider":
      return { ...state, picker: picker("provider", "provider", providerItems(state)) };
    case "model":
      return { ...state, picker: picker("model", "model", modelItems(state)) };
    case "help":
      return { ...state, picker: picker("help", "keys", helpItems()) };
  }
}

function helpItems(): ReadonlyArray<TuiPickerItem> {
  return [
    { id: "keys-1", label: "↑ / ↓", detail: "move · scroll the transcript" },
    { id: "keys-2", label: "enter", detail: "send the message, or run the highlighted command" },
    { id: "keys-3", label: "ctrl-i", detail: "interrupt the running turn" },
    { id: "keys-4", label: "ctrl-r", detail: "reload from the server" },
    { id: "keys-5", label: "ctrl-p", detail: "open the command palette" },
    { id: "keys-6", label: "esc", detail: "close the palette · leave the thread" },
    { id: "keys-7", label: "/", detail: "type a command: /new, /model, /settings, …" },
    { id: "keys-8", label: "ctrl-c", detail: "quit" },
  ];
}

/** Starting a thread is a draft until the first message is sent. */
function startDraft(state: TuiState, projectId: string): TuiState {
  const project = state.projects.find((row) => row.id === projectId);
  if (project === undefined) return state;
  return {
    ...state,
    view: "thread",
    openThreadId: null,
    title: `new thread · ${project.title}`,
    recap: null,
    lines: [],
    input: "",
    scrollback: 0,
    picker: null,
    draft: {
      projectId: project.id,
      projectTitle: project.title,
      modelSelection: resolveModelSelection(state, project),
    },
    status: `new thread in ${project.title}`,
  };
}

/** Everything a command does, expressed as state plus at most one effect. */
function runCommand(
  state: TuiState,
  name: TuiCommandName,
  argument: string,
): { readonly state: TuiState; readonly effect: TuiEffect } {
  switch (name) {
    case "new": {
      const cleared = { ...state, input: "" };
      // `/new <project>` skips the picker when the argument names one.
      const wanted = argument.trim().toLowerCase();
      if (wanted.length > 0) {
        const project = state.projects.find(
          (row) => row.title.toLowerCase() === wanted || row.id === argument.trim(),
        );
        if (project !== undefined) return { state: startDraft(cleared, project.id), effect: null };
      }
      return { state: openPickerFor(cleared, "project"), effect: null };
    }
    case "threads":
      return {
        state: {
          ...state,
          input: "",
          view: "threads",
          openThreadId: null,
          draft: null,
          picker: null,
        },
        effect: { kind: "refresh" },
      };
    case "provider":
      return { state: openPickerFor({ ...state, input: "" }, "provider"), effect: null };
    case "model":
      return { state: openPickerFor({ ...state, input: "" }, "model"), effect: null };
    case "settings":
      return {
        state: { ...state, input: "", view: "settings", settingsIndex: 0, picker: null },
        effect: null,
      };
    case "help":
      return { state: openPickerFor({ ...state, input: "" }, "help"), effect: null };
    case "interrupt":
      return { state: { ...state, input: "" }, effect: { kind: "interrupt" } };
    case "refresh":
      return { state: { ...state, input: "" }, effect: { kind: "refresh" } };
    case "quit":
      return { state, effect: { kind: "quit" } };
  }
}

/** A project or model choice lands in the draft and the session's selection. */
function applyPick(
  state: TuiState,
  kind: TuiPickerKind,
  id: string,
): { readonly state: TuiState; readonly effect: TuiEffect } {
  const closed = { ...state, picker: null };
  switch (kind) {
    case "project":
      return { state: startDraft(closed, id), effect: null };
    case "provider": {
      const provider = state.providers.find((row) => row.instanceId === id);
      if (provider === undefined) return { state: closed, effect: null };
      const model = provider.models[0];
      return {
        state: {
          ...closed,
          modelSelection:
            model === undefined
              ? null
              : { instanceId: ProviderInstanceId.make(provider.instanceId), model: model.slug },
          draft:
            closed.draft === null
              ? null
              : {
                  ...closed.draft,
                  modelSelection:
                    model === undefined
                      ? closed.draft.modelSelection
                      : {
                          instanceId: ProviderInstanceId.make(provider.instanceId),
                          model: model.slug,
                        },
                },
        },
        effect: { kind: "savePreferences", patch: { defaultProvider: provider.instanceId } },
      };
    }
    case "model": {
      const provider = activeProvider(state);
      if (provider === null) return { state: closed, effect: null };
      const selection = { instanceId: ProviderInstanceId.make(provider.instanceId), model: id };
      return {
        state: {
          ...closed,
          modelSelection: selection,
          draft: closed.draft === null ? null : { ...closed.draft, modelSelection: selection },
        },
        effect: { kind: "savePreferences", patch: { defaultModel: id } },
      };
    }
    default:
      return { state: closed, effect: null };
  }
}

/** Enter on a settings row: toggles flip, picker rows open their picker. */
function activateSetting(
  state: TuiState,
  key: string,
): { readonly state: TuiState; readonly effect: TuiEffect } {
  if (key === "defaultProvider") {
    return { state: openPickerFor(state, "provider"), effect: null };
  }
  if (key === "defaultModel") {
    return { state: openPickerFor(state, "model"), effect: null };
  }
  if (key === "showAgentDetails") {
    const showAgentDetails = !state.preferences.showAgentDetails;
    return {
      state: { ...state, preferences: { ...state.preferences, showAgentDetails } },
      effect: { kind: "savePreferences", patch: { showAgentDetails } },
    };
  }
  const setting = state.serverSettings.find((row) => row.key === key);
  if (setting === undefined) return { state, effect: null };
  if (typeof setting.value !== "boolean") return { state, effect: null };
  const value = !setting.value;
  return {
    state: {
      ...state,
      serverSettings: state.serverSettings.map((row) =>
        row.key === key ? { ...row, value } : row,
      ),
    },
    effect: { kind: "saveServerSetting", key, value },
  };
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
  if (name === "ctrl-p") {
    return { state: openPickerFor(state, "command"), effect: null };
  }

  const current = state.picker;
  if (current !== null) {
    switch (name) {
      case "escape":
        return { state: { ...state, picker: null }, effect: null };
      case "up":
      case "down": {
        const count = pickerItems(state).length;
        if (count === 0) return { state, effect: null };
        const index = Math.min(Math.max(0, current.index + (name === "up" ? -1 : 1)), count - 1);
        return { state: { ...state, picker: { ...current, index } }, effect: null };
      }
      case "backspace":
        return {
          state: { ...state, picker: { ...current, query: current.query.slice(0, -1), index: 0 } },
          effect: null,
        };
      case "enter": {
        const item = pickerItems(state)[current.index];
        if (item === undefined) return { state: { ...state, picker: null }, effect: null };
        if (current.kind === "command") {
          const command = commandByName(item.id);
          if (command === null) return { state: { ...state, picker: null }, effect: null };
          return runCommand({ ...state, picker: null }, command.name, "");
        }
        if (current.kind === "help") return { state: { ...state, picker: null }, effect: null };
        return applyPick(state, current.kind, item.id);
      }
      default: {
        const typed = key.text;
        if (typed === undefined || typed.length === 0) return { state, effect: null };
        return {
          state: { ...state, picker: { ...current, query: `${current.query}${typed}`, index: 0 } },
          effect: null,
        };
      }
    }
  }

  if (state.view === "settings") {
    switch (name) {
      case "escape":
        return { state: { ...state, view: "threads" }, effect: { kind: "refresh" } };
      case "up":
        return { state: moveSelection(state, -1), effect: null };
      case "down":
        return { state: moveSelection(state, 1), effect: null };
      case "enter": {
        const row = settingsRows(state)[state.settingsIndex];
        return row === undefined ? { state, effect: null } : activateSetting(state, row.key);
      }
      case "ctrl-r":
        return { state, effect: { kind: "refresh" } };
      default:
        return { state, effect: null };
    }
  }

  // A `/` in the composer owns the arrows: they move through the suggestions,
  // and Enter runs the highlighted one instead of sending the text.
  const suggestions = commandSuggestions(state);
  if (suggestions.length > 0) {
    const parsed = parseCommandInput(state.input);
    switch (name) {
      case "up":
      case "down": {
        const index = Math.min(
          Math.max(0, state.commandIndex + (name === "up" ? -1 : 1)),
          suggestions.length - 1,
        );
        return { state: { ...state, commandIndex: index }, effect: null };
      }
      case "tab": {
        const highlighted = suggestions[Math.min(state.commandIndex, suggestions.length - 1)];
        if (highlighted === undefined) return { state, effect: null };
        return { state: { ...state, input: `/${highlighted.id} `, commandIndex: 0 }, effect: null };
      }
      case "escape":
        return { state: { ...state, input: "", commandIndex: 0 }, effect: null };
      case "enter": {
        const highlighted = suggestions[Math.min(state.commandIndex, suggestions.length - 1)];
        if (highlighted === undefined) return { state, effect: null };
        const command = commandByName(highlighted.id);
        if (command === null) return { state, effect: null };
        return runCommand(state, command.name, parsed?.argument ?? "");
      }
      default:
        break;
    }
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
      case "backspace":
        return { state: { ...state, input: state.input.slice(0, -1) }, effect: null };
      case "enter-command":
        return { state, effect: null };
      default: {
        const typed = key.text;
        if (typed === undefined || typed.length === 0) return { state, effect: null };
        return {
          state: { ...state, input: `${state.input}${typed}`, commandIndex: 0 },
          effect: null,
        };
      }
    }
  }

  switch (name) {
    case "escape":
      if (state.input.length > 0) {
        return { state: { ...state, input: "" }, effect: null };
      }
      return {
        state: { ...state, view: "threads", openThreadId: null, draft: null },
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
      const parsed = parseCommandInput(state.input);
      if (parsed !== null) {
        const command = commandByName(parsed.name);
        if (command !== null) return runCommand(state, command.name, parsed.argument);
        return {
          state: { ...state, input: "", status: `unknown command: /${parsed.name}` },
          effect: null,
        };
      }
      if (state.draft !== null) {
        const title = text.split("\n")[0]?.slice(0, 80).trim() ?? "";
        return {
          state: { ...state, input: "", status: `starting ${state.draft.projectTitle}…` },
          effect: {
            kind: "createThread",
            projectId: state.draft.projectId,
            title: title.length === 0 ? "New thread" : title,
            text,
          },
        };
      }
      if (state.openThreadId === null) return { state, effect: null };
      return { state: { ...state, input: "" }, effect: { kind: "send", text } };
    }
    default:
      return key.text === undefined || key.text.length === 0
        ? { state, effect: null }
        : {
            state: { ...state, input: `${state.input}${key.text}`, commandIndex: 0 },
            effect: null,
          };
  }
}

/** The last `rows` transcript lines, newest at the bottom. */
export function visibleLines(state: TuiState, rows: number): ReadonlyArray<TuiLine> {
  const window = Math.max(1, rows);
  const end = Math.max(window, state.lines.length - state.scrollback);
  return state.lines.slice(Math.max(0, end - window), end);
}
