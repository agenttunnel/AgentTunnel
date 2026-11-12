/**
 * Command palette (Cmd/Ctrl+P): pure data, fuzzy matching, and grouping. No
 * React. Items are built per open from the handlers App already has, so a
 * palette row never starts or steers a turn on its own.
 */

import type { CenterTab, SlashCommand } from "./commands";

export type PaletteKind = "command" | "composer" | "thread" | "project";

export type PaletteItem = {
  id: string;
  kind: PaletteKind;
  /** Section heading; items sharing a group render under one heading. */
  group: string;
  label: string;
  detail?: string;
  /** Right-aligned key hint, e.g. a panel's single-letter shortcut. */
  hint?: string;
  run: () => void;
};

export type PaletteSection = { title: string; items: PaletteItem[] };

export type PaletteActions<P extends string> = {
  hasProject: boolean;
  hasThread: boolean;
  busy: boolean;
  autoMode: boolean;
  peerMailbox: boolean;
  panels: Record<P, { label: string; key: string }>;
  /** Panels that are always open; they get no toggle entry. */
  pinned: P[];
  isPanelOpen: (id: P) => boolean;
  togglePanel: (id: P) => void;
  setTab: (tab: CenterTab) => void;
  newThread: () => void;
  createProject: () => void;
  openSettings: () => void;
  refresh: () => void;
  stopTurn: () => void;
  toggleAutoMode: () => void;
  togglePeerMailbox: () => void;
};

const CENTER_TABS: { tab: CenterTab; label: string }[] = [
  { tab: "chat", label: "Show chat" },
  { tab: "review", label: "Show review" },
  { tab: "graph", label: "Show agent graph" },
  { tab: "board", label: "Show task board" },
];

/** Generic over the aside panel ids, like commands.ts buildCommands. Callers
 * pass arrow-wrapped actions, so handlers resolve at call time. */
export function buildCommands<P extends string>(a: PaletteActions<P>): PaletteItem[] {
  const out: PaletteItem[] = [];
  const cmd = (id: string, group: string, label: string, run: () => void, extra?: Partial<PaletteItem>) =>
    out.push({ id, kind: "command", group, label, run, ...extra });

  if (a.hasThread) {
    for (const { tab, label } of CENTER_TABS) cmd(`tab:${tab}`, "Navigate", label, () => a.setTab(tab));
    for (const id of Object.keys(a.panels) as P[]) {
      if (a.pinned.includes(id)) continue;
      const open = a.isPanelOpen(id);
      cmd(`panel:${id}`, "Panels", `${open ? "Hide" : "Show"} ${a.panels[id].label}`, () => a.togglePanel(id), {
        hint: a.panels[id].key,
      });
    }
    if (a.hasProject) cmd("thread:new", "Thread", "Create thread", a.newThread);
    if (a.busy) cmd("thread:stop", "Thread", "Stop running turn", a.stopTurn);
    cmd("thread:auto", "Thread", "Toggle auto mode", a.toggleAutoMode, {
      detail: a.autoMode ? "on" : "off",
    });
    cmd("thread:peers", "Thread", "Toggle agent-to-agent messages", a.togglePeerMailbox, {
      detail: a.peerMailbox ? "on" : "off",
    });
  } else if (a.hasProject) {
    cmd("thread:new", "Thread", "Create thread", a.newThread);
  }

  cmd("project:new", "Project", "Create project", a.createProject);
  cmd("app:refresh", "App", "Refresh", a.refresh);
  cmd("app:settings", "App", "Open settings", a.openSettings);
  return out;
}

/** The composer's own features, shown in place of the thread list when the
 * palette opens from the composer. Slash commands come from commands.ts so the
 * palette and the slash popup never drift apart. */
export function composerItems(a: {
  attach: () => void;
  modelConfig: () => void;
  slash: SlashCommand[];
  runSlash: (cmd: SlashCommand) => void;
}): PaletteItem[] {
  const out: PaletteItem[] = [
    { id: "composer:attach", kind: "composer", group: "Composer", label: "Attach file or folder", hint: "@", run: a.attach },
    { id: "composer:models", kind: "composer", group: "Composer", label: "Model config", run: a.modelConfig },
  ];
  for (const c of a.slash) {
    out.push({
      id: `slash:${c.name}`,
      kind: "composer",
      group: "Slash commands",
      label: `/${c.name}${c.args ? ` ${c.args}` : ""}`,
      detail: c.description,
      hint: c.hint,
      run: () => a.runSlash(c),
    });
  }
  return out;
}

export type ThreadLike = { id: string; title: string; projectId?: string };

export function threadItems(
  list: ThreadLike[],
  projectNames: Map<string, string>,
  showProject: boolean,
  goTo: (t: ThreadLike) => void,
): PaletteItem[] {
  return list.map((t) => ({
    id: `thread:${t.id}`,
    kind: "thread",
    group: "Go to thread",
    label: t.title || "Untitled thread",
    detail: showProject && t.projectId ? projectNames.get(t.projectId) : undefined,
    run: () => goTo(t),
  }));
}

export function projectItems(
  projects: { id: string; name: string; path: string }[],
  goTo: (id: string) => void,
): PaletteItem[] {
  return projects.map((p) => ({
    id: `project:${p.id}`,
    kind: "project",
    group: "Go to project",
    label: p.name,
    detail: p.path,
    run: () => goTo(p.id),
  }));
}

/** Groups keep the order of their first item. */
export function groupItems(items: PaletteItem[]): PaletteSection[] {
  const byTitle = new Map<string, PaletteItem[]>();
  for (const item of items) {
    const list = byTitle.get(item.group);
    if (list) list.push(item);
    else byTitle.set(item.group, [item]);
  }
  return [...byTitle].map(([title, list]) => ({ title, items: list }));
}

/** ">" limits the palette to commands, "@" to threads and projects. */
export type PaletteScope = "all" | "commands" | "targets";

export function parseScope(raw: string): { scope: PaletteScope; text: string } {
  const t = raw.trimStart();
  if (t.startsWith(">")) return { scope: "commands", text: t.slice(1).trim().toLowerCase() };
  if (t.startsWith("@")) return { scope: "targets", text: t.slice(1).trim().toLowerCase() };
  return { scope: "all", text: t.trim().toLowerCase() };
}

/** -1 when the query is not a subsequence of text. Substring hits outrank
 * scattered ones; consecutive and word-start hits score higher. */
export function fuzzyScore(query: string, text: string): number {
  if (!query) return 0;
  const t = text.toLowerCase();
  const at = t.indexOf(query);
  if (at >= 0) return 1000 - at;
  let score = 0;
  let from = 0;
  let prev = -2;
  for (const ch of query) {
    const idx = t.indexOf(ch, from);
    if (idx < 0) return -1;
    score += idx === prev + 1 ? 3 : 1;
    if (idx === 0 || /[\s\-_/.]/.test(t[idx - 1])) score += 2;
    prev = idx;
    from = idx + 1;
  }
  return score;
}

const KIND_IN_SCOPE: Record<PaletteScope, (k: PaletteKind) => boolean> = {
  all: () => true,
  commands: (k) => k === "command" || k === "composer",
  targets: (k) => k === "thread" || k === "project",
};

/** Filters and ranks sections, rendering at most `cap` rows across them. With
 * a query, rows sort by score; without one they keep their given order. */
export function filterSections(
  sections: PaletteSection[],
  scope: PaletteScope,
  text: string,
  cap: number,
): { sections: PaletteSection[]; hidden: number } {
  let budget = cap;
  let hidden = 0;
  const out: PaletteSection[] = [];
  for (const s of sections) {
    const scored: { item: PaletteItem; score: number }[] = [];
    for (const item of s.items) {
      if (!KIND_IN_SCOPE[scope](item.kind)) continue;
      const score = fuzzyScore(text, `${item.label} ${item.detail ?? ""}`);
      if (score >= 0) scored.push({ item, score });
    }
    if (text) scored.sort((a, b) => b.score - a.score);
    const shown = scored.slice(0, Math.max(budget, 0)).map((x) => x.item);
    hidden += scored.length - shown.length;
    budget -= shown.length;
    if (shown.length > 0) out.push({ title: s.title, items: shown });
  }
  return { sections: out, hidden };
}
