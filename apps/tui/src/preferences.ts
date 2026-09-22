// @effect-diagnostics nodeBuiltinImport:off - the TUI settings file is read and written outside any Effect runtime, and the module stays importable without one.
import * as NodeFSP from "node:fs/promises";
import * as NodePath from "node:path";

/**
 * The TUI's own settings: the provider and model new threads start from, and
 * whether agent activity is expanded by default. They live as a small JSON file
 * under the XDG config directory so the terminal client keeps its own choices
 * without touching the server or the desktop app.
 *
 * Reading is deliberately forgiving: a missing file, truncated JSON, or a field
 * of the wrong type falls back to `defaultPreferences` field by field, so a
 * hand-edited file can never make the client fail to start.
 */

export interface TuiPreferences {
  readonly defaultProvider: string | null;
  readonly defaultModel: string | null;
  readonly showAgentDetails: boolean;
}

export const defaultPreferences: TuiPreferences = {
  defaultProvider: null,
  defaultModel: null,
  showAgentDetails: true,
};

const CONFIG_DIRECTORY = "agenttunnel";
const CONFIG_FILE = "tui.json";

/** The two environment variables the settings path depends on, read once. */
const processEnvironment: {
  readonly XDG_CONFIG_HOME?: string | undefined;
  readonly HOME?: string | undefined;
} = { XDG_CONFIG_HOME: process.env.XDG_CONFIG_HOME, HOME: process.env.HOME };

/**
 * `$XDG_CONFIG_HOME/agenttunnel/tui.json`, falling back to
 * `$HOME/.config/agenttunnel/tui.json` when `XDG_CONFIG_HOME` is unset or empty
 * (the XDG spec treats an empty value as unset). The environment is a parameter
 * so tests can resolve paths without mutating `process.env`.
 */
export function preferencesPath(
  env: {
    readonly XDG_CONFIG_HOME?: string | undefined;
    readonly HOME?: string | undefined;
  } = processEnvironment,
): string {
  const configHome = env.XDG_CONFIG_HOME?.trim();
  const root =
    configHome !== undefined && configHome.length > 0
      ? configHome
      : env.HOME !== undefined && env.HOME.length > 0
        ? `${env.HOME}/.config`
        : null;
  if (root === null) {
    throw new Error("Cannot locate the TUI preferences file: set XDG_CONFIG_HOME or HOME.");
  }
  return `${root}/${CONFIG_DIRECTORY}/${CONFIG_FILE}`;
}

/**
 * Parse settings text. Anything unreadable (not JSON, not an object) yields the
 * defaults; within an object each field is validated on its own, so one bad
 * value does not discard the rest.
 */
export function parsePreferences(text: string): TuiPreferences {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return defaultPreferences;
  }

  const record: unknown = parsed;
  if (typeof record !== "object" || record === null || Array.isArray(record)) {
    return defaultPreferences;
  }

  // A settings string: a present, non-blank string survives; anything else is null.
  const fields = record as Record<string, unknown>;
  const provider = fields["defaultProvider"];
  const model = fields["defaultModel"];
  const details = fields["showAgentDetails"];
  return {
    defaultProvider:
      typeof provider === "string" && provider.trim().length > 0 ? provider.trim() : null,
    defaultModel: typeof model === "string" && model.trim().length > 0 ? model.trim() : null,
    showAgentDetails: typeof details === "boolean" ? details : defaultPreferences.showAgentDetails,
  };
}

/** Settings text with a stable key order and a trailing newline, so diffs stay readable. */
export function serializePreferences(value: TuiPreferences): string {
  return `${JSON.stringify(
    {
      defaultProvider: value.defaultProvider,
      defaultModel: value.defaultModel,
      showAgentDetails: value.showAgentDetails,
    },
    null,
    2,
  )}\n`;
}

/** Read settings, or the defaults when the file is missing or unreadable. */
export async function loadPreferences(path: string = preferencesPath()): Promise<TuiPreferences> {
  try {
    return parsePreferences(await NodeFSP.readFile(path, "utf8"));
  } catch {
    return defaultPreferences;
  }
}

/**
 * Write settings next to the target path and rename over it, so a crash mid-write
 * leaves the previous file intact. Creates the parent directory on first save.
 */
export async function savePreferences(
  value: TuiPreferences,
  path: string = preferencesPath(),
): Promise<void> {
  await NodeFSP.mkdir(NodePath.dirname(path), { recursive: true });
  const temporaryPath = `${path}.tmp`;
  await NodeFSP.writeFile(temporaryPath, serializePreferences(value));
  await NodeFSP.rename(temporaryPath, path);
}
