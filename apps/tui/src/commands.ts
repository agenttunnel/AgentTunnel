/**
 * The `/` command palette's registry. Kept free of terminal, RPC and I/O so the
 * palette can be exercised from tests: `parseCommandInput` splits what the user
 * typed, `matchCommands` picks what the current view offers, and
 * `commandByName` resolves the parsed name to a command.
 */

export type TuiCommandName =
  | "new"
  | "threads"
  | "model"
  | "provider"
  | "settings"
  | "help"
  | "interrupt"
  | "refresh"
  | "quit";

export interface TuiCommand {
  readonly name: TuiCommandName;
  /** One short line for the palette. */
  readonly description: string;
  /** Shown after the name, e.g. `[model]`; omit when the command takes no argument. */
  readonly argument?: string;
  /** Where the command is offered; `any` shows in both views. */
  readonly scope: "threads" | "thread" | "any";
}

export const TUI_COMMANDS: ReadonlyArray<TuiCommand> = [
  { name: "new", description: "Start a new thread", scope: "any" },
  { name: "threads", description: "Back to the thread list", scope: "thread" },
  { name: "model", description: "Choose the model", argument: "[model]", scope: "any" },
  { name: "provider", description: "Choose the provider", argument: "[provider]", scope: "any" },
  { name: "settings", description: "Open settings", scope: "any" },
  { name: "help", description: "Key bindings and commands", scope: "any" },
  { name: "interrupt", description: "Stop the current turn", scope: "thread" },
  { name: "refresh", description: "Reload from the server", scope: "any" },
  { name: "quit", description: "Exit the TUI", scope: "any" },
];

/** `/mod foo` → `{ name: "mod", argument: "foo" }`; null when the input is not a command. */
export function parseCommandInput(
  input: string,
): { readonly name: string; readonly argument: string } | null {
  const trimmed = input.trim();
  if (!trimmed.startsWith("/")) {
    return null;
  }
  // `/` alone is the user starting to type a command, not a command; a name is
  // everything up to the first whitespace run, the argument is the rest.
  const match = /^(\S+)(?:\s+([\s\S]*))?$/.exec(trimmed.slice(1));
  if (match === null) {
    return null;
  }
  return { name: match[1]!, argument: (match[2] ?? "").trim() };
}

/** Commands offered in this view whose name starts with the typed query (empty query = all). */
export function matchCommands(
  query: string,
  view: "threads" | "thread",
): ReadonlyArray<TuiCommand> {
  const prefix = query.trim().toLowerCase();
  return TUI_COMMANDS.filter(
    (command) =>
      (command.scope === "any" || command.scope === view) &&
      command.name.toLowerCase().startsWith(prefix),
  );
}

/** Exact name lookup, case-insensitive; null when unknown. */
export function commandByName(name: string): TuiCommand | null {
  const wanted = name.trim().toLowerCase();
  return TUI_COMMANDS.find((command) => command.name.toLowerCase() === wanted) ?? null;
}
