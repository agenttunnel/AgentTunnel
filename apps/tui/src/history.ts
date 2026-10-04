// @effect-diagnostics nodeBuiltinImport:off - the TUI history file is read and written outside any Effect runtime, and the module stays importable without one.
import * as NodeFSP from "node:fs/promises";

import { preferencesPath } from "./preferences.ts";

/**
 * The prompts this client has sent, per thread. A shell's history is the
 * cheapest form of muscle memory, and it belongs to the conversation: reaching
 * for ↑ in a thread should offer what *that* thread was told, not the last thing
 * typed anywhere.
 *
 * It lives beside the settings rather than inside them, because it is a log the
 * client appends to itself, not something a person edits.
 */

export const HISTORY_LIMIT = 100;

/** The bucket a prompt lands in before the thread it starts exists. */
export const DRAFT_THREAD = "";

export type HistoryMap = Readonly<Record<string, ReadonlyArray<string>>>;

const CONFIG_ROOT = { XDG_CONFIG_HOME: process.env.XDG_CONFIG_HOME, HOME: process.env.HOME };

/** `$XDG_CONFIG_HOME/agenttunnel/history.json`, beside `tui.json`. */
export function historyPath(
  env: {
    readonly XDG_CONFIG_HOME?: string | undefined;
    readonly HOME?: string | undefined;
  } = CONFIG_ROOT,
): string {
  return preferencesPath(env).replace(/tui\.json$/u, "history.json");
}

/** Readable entries only: a log with a blank line in it is a bug, not a prompt. */
const clean = (entries: ReadonlyArray<unknown>): ReadonlyArray<string> =>
  entries
    .filter((entry): entry is string => typeof entry === "string")
    .map((entry) => entry.trim())
    .filter((entry) => entry.length > 0)
    .slice(-HISTORY_LIMIT);

/** A map of thread id to prompts; anything else is discarded rather than argued with. */
export function parseHistory(text: string): HistoryMap {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return {};
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return {};
  const map: Record<string, ReadonlyArray<string>> = {};
  for (const [threadId, entries] of Object.entries(parsed as Record<string, unknown>)) {
    if (Array.isArray(entries)) {
      const cleaned = clean(entries);
      if (cleaned.length > 0) map[threadId] = cleaned;
    }
  }
  return map;
}

export function serializeHistory(map: HistoryMap): string {
  const ordered: Record<string, ReadonlyArray<string>> = {};
  // Stable key order, so the file diffs cleanly.
  for (const threadId of Object.keys(map).sort()) {
    ordered[threadId] = map[threadId] ?? [];
  }
  return `${JSON.stringify(ordered, null, 2)}\n`;
}

/** What ↑ should walk back through in this thread. */
export const entriesFor = (map: HistoryMap, threadId: string | null): ReadonlyArray<string> =>
  map[threadId ?? DRAFT_THREAD] ?? [];

/**
 * Add one prompt to a thread's log. A repeat of that thread's newest entry is
 * not recorded twice (the common "send it again" case), and the log is capped so
 * it cannot grow without bound.
 */
export function remember(map: HistoryMap, threadId: string | null, text: string): HistoryMap {
  const trimmed = text.trim();
  if (trimmed.length === 0) return map;
  const key = threadId ?? DRAFT_THREAD;
  const existing = map[key] ?? [];
  if (existing.at(-1) === trimmed) return map;
  return { ...map, [key]: [...existing, trimmed].slice(-HISTORY_LIMIT) };
}

/**
 * The first message of a new thread is typed before the thread exists, then the
 * thread is created with an id. This moves that log onto the real id, so ↑ works
 * in the new thread straight away instead of only after a reload.
 */
export function renameThread(map: HistoryMap, from: string, to: string): HistoryMap {
  const entries = map[from];
  if (entries === undefined || entries.length === 0 || from === to) return map;
  const { [from]: _moved, ...rest } = map;
  return { ...rest, [to]: [...(rest[to] ?? []), ...entries].slice(-HISTORY_LIMIT) };
}

/** Read the history, or nothing when the file is missing or unreadable. */
export async function loadHistory(path: string = historyPath()): Promise<HistoryMap> {
  try {
    return parseHistory(await NodeFSP.readFile(path, "utf8"));
  } catch {
    return {};
  }
}

/** Best effort: a history that cannot be written must never stop a send. */
export async function saveHistory(map: HistoryMap, path: string = historyPath()): Promise<void> {
  try {
    await NodeFSP.mkdir(path.slice(0, path.lastIndexOf("/")), { recursive: true });
    const temporary = `${path}.tmp`;
    await NodeFSP.writeFile(temporary, serializeHistory(map));
    await NodeFSP.rename(temporary, path);
  } catch {
    // Ignored on purpose: the prompt has already been sent.
  }
}
