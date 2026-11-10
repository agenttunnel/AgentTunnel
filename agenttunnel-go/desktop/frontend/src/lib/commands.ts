/**
 * Slash commands: the composer's `/name args` shortcuts. Pure data and matching,
 * no React. Every feature with a panel or a tab gets a command here, so the
 * popup is the feature index. Commands reuse the handlers App already has; a
 * command never starts or steers a turn.
 */

export type CenterTab = "chat" | "review" | "graph" | "board";

export type SlashCommand = {
  name: string;
  description: string;
  /** Right-aligned key hint, e.g. the panel's single-letter shortcut. */
  hint?: string;
  /** Argument placeholder. Present means the command takes text after its name. */
  args?: string;
  run: (arg: string) => void;
};

export type SlashActions<P extends string> = {
  panels: Record<P, { label: string; key: string }>;
  openPanel: (id: P) => void;
  setTab: (tab: CenterTab) => void;
  openSettings: () => void;
  askBtw: (question: string) => void;
  toggleAutoMode: () => void;
  togglePeerMailbox: () => void;
  stopTurn: () => void;
  newThread: () => void;
  saveDefaults: () => void;
};

const CENTER_TABS: { tab: CenterTab; description: string }[] = [
  { tab: "chat", description: "Show the chat" },
  { tab: "review", description: "Show the review view" },
  { tab: "graph", description: "Show the agent graph" },
  { tab: "board", description: "Show the task board" },
];

export function buildCommands<P extends string>(a: SlashActions<P>): SlashCommand[] {
  const panels = (Object.keys(a.panels) as P[]).map<SlashCommand>((id) => ({
    name: id,
    description: `Open ${a.panels[id].label} in the side panel`,
    hint: a.panels[id].key,
    run: () => a.openPanel(id),
  }));
  const tabs = CENTER_TABS.map<SlashCommand>(({ tab, description }) => ({
    name: tab,
    description,
    run: () => a.setTab(tab),
  }));
  return [
    ...panels,
    ...tabs,
    {
      name: "btw",
      description: "Ask a side question; the answer shows apart from the running turn",
      args: "<question>",
      run: (arg) => a.askBtw(arg),
    },
    { name: "auto", description: "Toggle auto mode for this thread", run: () => a.toggleAutoMode() },
    { name: "peers", description: "Toggle agent-to-agent messages for this thread", run: () => a.togglePeerMailbox() },
    { name: "stop", description: "Stop the turn running on this thread", run: () => a.stopTurn() },
    { name: "new", description: "Start a new thread in this project", run: () => a.newThread() },
    { name: "defaults", description: "Save this view as the default for new threads", run: () => a.saveDefaults() },
    { name: "settings", description: "Open settings", run: () => a.openSettings() },
  ];
}

/** The partial command name being typed, or null when the composer is not in
 * slash mode (no leading "/", or the user already typed a space). */
export function slashQuery(input: string): string | null {
  if (!input.startsWith("/") || /\s/.test(input)) return null;
  return input.slice(1).toLowerCase();
}

/** Prefix matches first, then substring matches; both keep registry order. */
export function matchCommands(commands: SlashCommand[], query: string): SlashCommand[] {
  const prefix: SlashCommand[] = [];
  const contains: SlashCommand[] = [];
  for (const cmd of commands) {
    if (cmd.name.startsWith(query)) prefix.push(cmd);
    else if (cmd.name.includes(query)) contains.push(cmd);
  }
  return prefix.concat(contains);
}

/** Splits `/name rest` into its name and argument; null for anything else. */
export function parseSlash(text: string): { name: string; arg: string } | null {
  const m = /^\/(\S+)\s*([\s\S]*)$/.exec(text.trim());
  return m ? { name: m[1], arg: m[2].trim() } : null;
}
