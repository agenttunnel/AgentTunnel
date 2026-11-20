/**
 * Command palette (Cmd/Ctrl+K or P): pure data, fuzzy matching, and grouping.
 * No React. Items are built per open from the handlers App already has, so a
 * palette row never starts or steers a turn on its own.
 */

import type { CenterTab, SlashCommand } from "./commands";
import { fuzzyScore } from "./fuzzy";
import { ALT, centerTabHint, MOD } from "./navKeys";
import { STATUS_LABEL, threadStatus } from "./threadStatus";

export type PaletteKind = "command" | "composer" | "thread" | "project" | "file";

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
  compact: boolean;
  panels: Record<P, { label: string; key: string }>;
  /** Panels that are always open; they get no toggle entry. */
  pinned: P[];
  isPanelOpen: (id: P) => boolean;
  togglePanel: (id: P) => void;
  setTab: (tab: CenterTab) => void;
  newThread: () => void;
  createProject: () => void;
  openSettings: () => void;
  openSuperView: () => void;
  refresh: () => void;
  stopTurn: () => void;
  toggleAutoMode: () => void;
  togglePeerMailbox: () => void;
  toggleCompact: () => void;
  /** Opens an environment page: Projects, Orchestrator, Worktrees or Stats. */
  goToPage: (page: "projects" | "orchestrator" | "worktrees" | "stats") => void;
  openHelp: () => void;
};

const CENTER_TABS: { tab: CenterTab; label: string }[] = [
  { tab: "chat", label: "Show chat" },
  { tab: "review", label: "Show review" },
  { tab: "files", label: "Show files" },
  { tab: "graph", label: "Show agent graph" },
  { tab: "p2p", label: "Show agent chat" },
  { tab: "board", label: "Show task board" },
];

const PAGES = [
  { page: "projects", label: "Go to Projects" },
  { page: "orchestrator", label: "Go to Orchestrator" },
  { page: "worktrees", label: "Go to Worktrees" },
  { page: "stats", label: "Go to Stats" },
] as const;

/** Generic over the aside panel ids, like commands.ts buildCommands. Callers
 * pass arrow-wrapped actions, so handlers resolve at call time. */
export function buildCommands<P extends string>(a: PaletteActions<P>): PaletteItem[] {
  const out: PaletteItem[] = [];
  const cmd = (id: string, group: string, label: string, run: () => void, extra?: Partial<PaletteItem>) =>
    out.push({ id, kind: "command", group, label, run, ...extra });

  if (a.hasThread) {
    for (const { tab, label } of CENTER_TABS) {
      cmd(`tab:${tab}`, "Thread view", label, () => a.setTab(tab), { hint: centerTabHint(tab) });
    }
    cmd("view:compact", "Thread view", "Toggle compact mode", a.toggleCompact, {
      detail: a.compact ? "on" : "off",
      hint: `${MOD}S`,
    });
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

  for (const { page, label } of PAGES) cmd(`home:${page}`, "Go to", label, () => a.goToPage(page));
  cmd("view:super", "Go to", "All threads across projects", a.openSuperView, { hint: `${ALT}Tab` });
  cmd("project:new", "Project", "Open a folder as a project", a.createProject);
  cmd("app:refresh", "App", "Refresh", a.refresh);
  cmd("app:settings", "App", "Open settings", a.openSettings);
  cmd("app:help", "App", "Keyboard shortcuts and help", a.openHelp, { detail: "/help" });
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

export type ThreadLike = Parameters<typeof threadStatus>[0] & { id: string; title: string; projectId?: string };

/** Every thread, the current project's first; the detail names the project
 * (outside the current one) and any status worth a glance. */
export function threadItems(
  list: ThreadLike[],
  projectNames: Map<string, string>,
  currentProject: string | null,
  goTo: (t: ThreadLike) => void,
): PaletteItem[] {
  const here = list.filter((t) => t.projectId === currentProject);
  const there = list.filter((t) => t.projectId !== currentProject);
  return [...here, ...there].map((t) => {
    const status = STATUS_LABEL[threadStatus(t)];
    const project = t.projectId !== currentProject && t.projectId ? projectNames.get(t.projectId) : undefined;
    return {
      id: `thread:${t.id}`,
      kind: "thread",
      group: "Threads",
      label: t.title || "Untitled thread",
      detail: [status, project].filter(Boolean).join(" · ") || undefined,
      run: () => goTo(t),
    };
  });
}

export function projectItems(
  projects: { id: string; name: string; path: string }[],
  goTo: (id: string) => void,
): PaletteItem[] {
  return projects.map((p) => ({
    id: `project:${p.id}`,
    kind: "project",
    group: "Projects",
    label: p.name,
    detail: p.path,
    run: () => goTo(p.id),
  }));
}

/** Moves the recently opened items (by id, newest first) into a leading
 * "Recent" group; the rest keep their order. */
export function withRecent(items: PaletteItem[], recent: string[], max = 6): PaletteItem[] {
  const byId = new Map(items.map((i) => [i.id, i]));
  const lead: PaletteItem[] = [];
  for (const id of recent) {
    const item = byId.get(id);
    if (!item) continue;
    lead.push({ ...item, group: "Recent" });
    byId.delete(id);
    if (lead.length >= max) break;
  }
  return [...lead, ...items.filter((i) => byId.has(i.id))];
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

/** ">" limits the palette to commands, "@" to threads, projects and files. */
export type PaletteScope = "all" | "commands" | "targets";

export function parseScope(raw: string): { scope: PaletteScope; text: string } {
  const t = raw.trimStart();
  if (t.startsWith(">")) return { scope: "commands", text: t.slice(1).trim().toLowerCase() };
  if (t.startsWith("@")) return { scope: "targets", text: t.slice(1).trim().toLowerCase() };
  return { scope: "all", text: t.trim().toLowerCase() };
}

const KIND_IN_SCOPE: Record<PaletteScope, (k: PaletteKind) => boolean> = {
  all: () => true,
  commands: (k) => k === "command" || k === "composer",
  targets: (k) => k === "thread" || k === "project" || k === "file",
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
      // The label decides the rank; the detail (project, path) can still match.
      const score = fuzzyScore(text, item.label) ?? fuzzyScore(text, `${item.label} ${item.detail ?? ""}`);
      if (score !== null) scored.push({ item, score });
    }
    if (text) scored.sort((a, b) => b.score - a.score);
    const shown = scored.slice(0, Math.max(budget, 0)).map((x) => x.item);
    hidden += scored.length - shown.length;
    budget -= shown.length;
    if (shown.length > 0) out.push({ title: s.title, items: shown });
  }
  return { sections: out, hidden };
}
