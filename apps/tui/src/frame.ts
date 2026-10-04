import type { OrchestrationThreadActivity } from "@t3tools/contracts";
import { foldSubagentActivities } from "@t3tools/client-runtime/state/subagentRuntime";

import { agentRows, agentUsageLines, formatCost } from "./agents.ts";
import { renderMarkdown } from "./markdown.ts";
import {
  lineGlyph,
  relativeAge,
  statusGlyph,
  threadRows,
  threadView,
  type TuiLine,
} from "./format.ts";
import {
  agentTunnelScreenRows,
  commandSuggestions,
  activeProvider,
  isThreadRunning,
  highlightedSuggestion,
  pickerItems,
  settingsRows,
  type TuiState,
} from "./state.ts";
import {
  chip,
  fleetHue,
  keycap,
  pad,
  paint,
  truncate,
  visibleWidth,
  type TuiColor,
  type TuiSurface,
} from "./theme.ts";

/**
 * The whole screen as lines of text.
 *
 * The frame is composed rather than laid out: every section knows how many rows
 * it takes, the sections are concatenated, and the result is padded or cut to
 * exactly the terminal's size. Nothing can overlap, because nothing is
 * positioned — the array order *is* the layout. That is the contract pi-tui
 * enforces (`render(width) -> string[]`, every line within width) and the
 * reason this file exists instead of a component tree.
 */

export interface FrameSize {
  readonly rows: number;
  readonly cols: number;
  /** Wall clock, so the elapsed time in the status strip is the caller's. */
  readonly now?: number;
}

const HEADER_ROWS = 2;
const COMPOSER_ROWS = 4;
const HINTS_ROWS = 1;
const MAX_PALETTE_ROWS = 8;
const CARD_MAX_COLS = 76;
const LABEL_COLS = 22;

/** The brand mark on the left of the header, the way every reference signs its chrome. */
const BRAND = "AgentTunnel";

/** The keys each surface answers to, written as keycaps rather than a sentence. */
const KEY_HINTS: Record<TuiState["view"], ReadonlyArray<readonly [string, string]>> = {
  threads: [
    ["↑/↓", "move"],
    ["enter", "open"],
    ["type", "starts a thread"],
    ["/", "commands"],
    ["ctrl-a", "agents"],
    ["ctrl-c", "quit"],
  ],
  thread: [
    ["↑/↓", "history"],
    ["enter", "send"],
    ["/", "commands"],
    ["ctrl-a", "agents"],
    ["ctrl-i", "interrupt"],
    ["esc", "back"],
  ],
  agents: [
    ["↑/↓", "move"],
    ["l", "log"],
    ["x", "stop"],
    ["r", "restart"],
    ["esc", "back"],
  ],
  agentLog: [
    ["esc", "back"],
    ["ctrl-r", "refresh"],
  ],
  usage: [["esc", "back"]],
  settings: [
    ["↑/↓", "move"],
    ["enter", "change"],
    ["esc", "back"],
  ],
  agenttunnel: [
    ["↑/↓", "move"],
    ["enter", "change"],
    ["esc", "back"],
  ],
};

/** The same, for a command panel opened under a running thread. */
const PANEL_HINTS: Partial<Record<TuiState["view"], ReadonlyArray<readonly [string, string]>>> = {
  agents: [
    ["↑/↓", "move"],
    ["l", "log"],
    ["x", "stop agent"],
    ["r", "restart agent"],
    ["esc", "close panel"],
  ],
  agentLog: [
    ["esc", "back to agents"],
    ["ctrl-r", "refresh"],
  ],
  settings: [
    ["↑/↓", "move"],
    ["enter", "change"],
    ["esc", "close panel"],
  ],
  agenttunnel: [
    ["↑/↓", "move"],
    ["enter", "change"],
    ["esc", "close panel"],
  ],
};

/** Spinner phases, advanced by the app while work runs. */
const SPINNER = ["◐", "◓", "◑", "◒"] as const;

const STATUS_ROWS = 2;

/** `2m 14s`, the way omp counts a running turn. */
function elapsed(since: string | null, now: number): string | null {
  if (since === null || now === 0) return null;
  const seconds = Math.max(0, Math.round((now - Date.parse(since)) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

/** Composer cursor: a block, so it reads as an input even when empty. */
const CURSOR = "█";

const statusTone = (status: string): TuiColor => {
  if (status === "failed" || status === "cancelled" || status === "interrupted") return "err";
  if (status === "running" || status === "pending" || status === "waiting") return "warn";
  if (status === "completed") return "ok";
  return "muted";
};

/** Exactly `height` lines, each exactly `cols` wide. */
function fill(lines: ReadonlyArray<string>, height: number, cols: number): string[] {
  const out: string[] = [];
  for (let index = 0; index < height; index += 1) {
    out.push(pad(truncate(lines[index] ?? "", cols), cols));
  }
  return out;
}

/**
 * The same, but the content sits in the middle of its band. An idle screen with
 * a block pinned to the top and a screenful of nothing under it reads as a bug;
 * every reference client centres it and lets the space around it be deliberate.
 */
function fillCentered(lines: ReadonlyArray<string>, height: number, cols: number): string[] {
  const above = Math.max(0, Math.floor((height - lines.length) / 2));
  const below = Math.max(0, height - lines.length - above);
  return fill(
    [
      ...Array.from({ length: above }, () => ""),
      ...lines,
      ...Array.from({ length: below }, () => ""),
    ],
    height,
    cols,
  );
}

/** Centre one line horizontally, ignoring the colour it already carries. */
function center(line: string, cols: number): string {
  const margin = Math.max(0, Math.floor((cols - visibleWidth(line)) / 2));
  return `${" ".repeat(margin)}${line}`;
}
/** The same, but the content sits at the bottom of its band: a live transcript
 * belongs against the composer, the way Claude Code pins the newest line to the
 * input, so the eye never crosses empty screen to find it. */
function fillAbove(lines: ReadonlyArray<string>, height: number, cols: number): string[] {
  const blanks = Math.max(0, height - lines.length);
  return fill([...Array.from({ length: blanks }, () => ""), ...lines], height, cols);
}

/** Wrap to at most `cols` columns, so a transcript never loses text silently. */
function wrap(line: string, cols: number, indent = "  "): string[] {
  if (cols <= indent.length) return [truncate(line, cols)];
  const out: string[] = [];
  let rest = line;
  let prefix = "";
  while (rest.length > 0) {
    const room = cols - prefix.length;
    if (visibleWidth(rest) <= room) {
      out.push(prefix + rest);
      break;
    }
    // Break on a space when there is one, otherwise mid-word.
    const slice = truncate(rest, room);
    const cut = slice.lastIndexOf(" ");
    const head = cut > room / 2 ? slice.slice(0, cut) : slice.replace(/…$/u, "");
    out.push(prefix + head);
    rest = rest.slice(head.length).replace(/^ /u, "");
    prefix = indent;
  }
  return out.length === 0 ? [""] : out;
}

/** A bordered card, centred, every line exactly `cols` wide. */
function card(title: string, body: ReadonlyArray<string>, cols: number): string[] {
  const width = Math.max(24, Math.min(cols - 2, CARD_MAX_COLS));
  const inner = width - 4;
  const left = Math.max(0, Math.floor((cols - width) / 2));
  const margin = " ".repeat(left);
  // Rows are padded to the card's own width: truncating them to less would eat
  // the right border.
  const edge = (text: string): string => margin + pad(truncate(text, width), width);
  const top = `${margin}╭${"─".repeat(width - 2)}╮`;
  const bottom = `${margin}╰${"─".repeat(width - 2)}╯`;
  const rows = [paint(edge(`│ ${pad(title, inner)} │`), "muted")];
  for (const line of body) {
    rows.push(edge(`│ ${pad(truncate(line, inner), inner)} │`));
  }
  return [paint(top, "faint"), ...rows, paint(bottom, "faint")].map((line) => pad(line, cols));
}

const VIEW_TITLES: Record<TuiState["view"], string> = {
  threads: "threads",
  thread: "thread",
  agents: "agents",
  agentLog: "log",
  usage: "token usage",
  settings: "settings",
  agenttunnel: "workflow roles",
};

/**
 * A command view opens *below* the running thread instead of replacing it, the
 * way the reference TUIs keep the session visible while a panel is up. The
 * thread keeps streaming into the top half; esc returns to it full height.
 */
function isSplit(state: TuiState): boolean {
  return state.openThreadId !== null && state.view !== "thread" && state.view !== "threads";
}

/** `── agents ─────────` marks where the panel begins. */
function separator(state: TuiState, cols: number): string {
  const lead = paint("── ", "faint");
  const label = paint(`${VIEW_TITLES[state.view]} `, "muted");
  const remaining = Math.max(0, cols - visibleWidth(lead) - visibleWidth(label));
  return pad(lead + label + paint("─".repeat(remaining), "faint"), cols);
}

function header(state: TuiState, cols: number): string {
  const titles: Record<TuiState["view"], string> = {
    threads: `threads (${state.threads.length})`,
    thread: state.title,
    agents: `agents (${state.agents.length})`,
    agentLog: `${state.agentLog?.label ?? "agent"} · log`,
    usage: "token usage",
    settings: "settings",
    agenttunnel: "workflow roles",
  };
  const title = isSplit(state) ? state.title : titles[state.view];
  const left = `${paint("▌", "primary")} ${paint(BRAND, "text", { bold: true })} ${paint(
    "·",
    "faint",
  )} ${paint(title, "muted")}`;
  const right = fleetText(state) ?? state.status;
  const gap = Math.max(1, cols - visibleWidth(left) - visibleWidth(right) - 1);
  return `${left}${" ".repeat(gap)}${paint(truncate(right, cols), "muted")}`;
}

/** A dim rule, so the chrome reads as a frame instead of more transcript. */
function rule(cols: number): string {
  return paint("─".repeat(Math.max(0, cols)), "line");
}

/** Spend and posture for the open thread, the way the hub roster summarises it. */
function fleetText(state: TuiState): string | null {
  const activities: ReadonlyArray<OrchestrationThreadActivity> =
    state.threadSnapshot?.thread.activities ?? [];
  if (activities.length === 0) return null;
  const agents = foldSubagentActivities(activities, {
    sessionLive: state.threadSnapshot?.thread.session?.status === "running",
  });
  if (agents.length === 0) return null;
  const running = agents.filter((agent) =>
    ["pending", "running", "waiting"].includes(agent.status),
  ).length;
  const failed = agents.filter((agent) =>
    ["failed", "cancelled", "interrupted"].includes(agent.status),
  ).length;
  const settled = agents.length - running - failed;
  const tokens = agents.reduce((total, agent) => total + (agent.usage?.totalTokens ?? 0), 0);
  const parts = [`${agents.length} agent${agents.length === 1 ? "" : "s"}`];
  if (running > 0) parts.push(`${running} running`);
  if (failed > 0) parts.push(`${failed} failed`);
  if (settled > 0) parts.push(`${settled} settled`);
  if (tokens > 0) parts.push(`${formatTokens(tokens)} tok`);
  return parts.join(" · ");
}

function formatTokens(total: number): string {
  if (total >= 1_000_000) return `${(total / 1_000_000).toFixed(1)}M`;
  if (total >= 1_000) return `${(total / 1_000).toFixed(1)}k`;
  return `${total}`;
}

/**
 * The transcript in Claude Code's grammar: `>` for what you asked, `⏺` for the
 * assistant, `⎿` for nested tool and sub-agent output, two-space hangs under
 * every marker, and a blank line between turns so roles never run together.
 */
function transcriptLines(state: TuiState, cols: number): string[] {
  const out: string[] = [];
  let previous: TuiLine["kind"] | null = null;
  for (const line of state.lines) {
    if (previous !== null && previous !== line.kind) out.push("");
    previous = line.kind;
    if (line.kind === "user") {
      wrap(line.text, cols - 2).forEach((text, index) => {
        out.push(
          index === 0
            ? `${paint(">", "primary", { bold: true })} ${paint(text, "text")}`
            : `  ${paint(text, "text")}`,
        );
      });
      continue;
    }
    // Assistant and system turns are markdown: render the structure instead of
    // flattening it, and give the thinking blocks their own dimmed voice.
    const rendered = renderMarkdown(line.text, cols - 2, {
      thinkingTone: "faint",
      collapseThinking: 24,
    });
    const nested = line.kind === "agent";
    const lead = line.kind === "system" ? "warn" : nested ? "faint" : "primary";
    rendered.forEach((text, index) => {
      if (index !== 0) {
        out.push(`  ${text}`);
        return;
      }
      // Tool and sub-agent output hangs off the turn above it, Claude Code's
      // `⎿` corner, rather than starting a new paragraph.
      out.push(
        nested
          ? `  ${paint(lineGlyph(line.kind), lead)}  ${text}`
          : `${paint(lineGlyph(line.kind), lead)} ${text}`,
      );
    });
  }
  // A failed run's reason goes last, where the eye already is, and in full:
  // the status strip only has room for the beginning of it.
  const session = state.threadSnapshot?.thread.session ?? null;
  if (session?.status === "error" && session.lastError !== null) {
    out.push("");
    for (const line of wrap(session.lastError, cols - 2)) {
      out.push(paint(`! ${line}`, "err"));
    }
    out.push(paint("! the run stopped here — ctrl-r reloads, or send a follow-up", "muted"));
  }
  return out;
}

/**
 * The welcome panel: who this is, what to press, and what you were doing. It
 * stands in for the list until the first deliberate move, the way omp greets a
 * session.
 */
function welcomeLines(state: TuiState, cols: number, rows: number, now: number): string[] {
  const leftWidth = Math.max(22, Math.min(32, Math.floor(cols / 3)));
  const rightWidth = Math.max(20, cols - leftWidth - 1);

  const workflow = state.providers.find((provider) => provider.driver === "agenttunnel");
  const model = workflow?.models.find((entry) => entry.isDefault === true) ?? workflow?.models[0];
  const recent = state.threads.slice(0, Math.max(1, Math.min(5, rows - 12)));

  const left = [
    paint("Welcome back!", "primary", { bold: true }),
    "",
    paint("  AgentTunnel", "text", { bold: true }),
    paint("  terminal client", "faint"),
    "",
    paint(`${state.threads.length} thread${state.threads.length === 1 ? "" : "s"}`, "muted"),
    paint(`${state.projects.length} project${state.projects.length === 1 ? "" : "s"}`, "muted"),
    model === undefined ? paint("no workflow model", "warn") : paint(model.name, "muted"),
    workflow === undefined ? null : paint(workflow.label, "faint"),
  ].filter((line): line is string => line !== null);

  const right = [
    paint("Tips", "primary", { bold: true }),
    paint("/     commands, /new starts a thread", "muted"),
    paint("enter open · ↑/↓ move · ctrl-a agents", "muted"),
    paint("ctrl-r reload · ctrl-c quit", "muted"),
    "",
    paint("Workspace", "primary", { bold: true }),
    state.projects.length === 0
      ? paint("/add-project <path> registers a directory", "muted")
      : paint(state.projects.map((project) => project.title).join(", "), "muted"),
    "",
    paint("Recent sessions", "primary", { bold: true }),
    ...(recent.length === 0
      ? [paint("nothing yet — /new starts one", "faint")]
      : recent.map(
          (thread) =>
            `${paint("•", "faint")} ${truncate(thread.title, rightWidth - 12)} ${paint(
              relativeAge(thread.updatedAt, now),
              "faint",
            )}`,
        )),
  ];

  const height = Math.max(left.length, right.length);
  const panel: string[] = [];
  for (let index = 0; index < height; index += 1) {
    panel.push(
      `${pad(truncate(left[index] ?? "", leftWidth - 2), leftWidth - 2)}  ${right[index] ?? ""}`,
    );
  }
  return [
    pad(center(paint("✻ AgentTunnel", "primary", { bold: true }), cols), cols),
    pad(center(paint("terminal client for your workflows", "faint"), cols), cols),
    "",
    ...card("", panel, cols),
  ];
}

/**
 * The thread list as a table with stable lanes: cursor, title, project, branch,
 * fleet, age. Rows are things you scan, so the columns do not move between rows,
 * the state lives in a tone, and the row under the cursor is a filled band.
 */
function listLines(state: TuiState, cols: number, now: number): string[] {
  const rows = threadRowsFrom(state);
  const ages = rows.map((row) => relativeAge(row.updatedAt, now));
  const widthOf = (text: string | null): number => (text === null ? 0 : visibleWidth(text));
  const projectWidth = Math.max(0, ...rows.map((row) => widthOf(row.projectTitle)));
  const branchWidth = Math.max(0, ...rows.map((row) => widthOf(row.branch)));
  const fleetWidth = Math.max(0, ...rows.map((row) => widthOf(row.fleet)));
  const ageWidth = Math.max(0, ...ages.map(widthOf));
  // cursor + the four two-column gaps between the five lanes.
  const fixed = 2 + 8 + projectWidth + branchWidth + fleetWidth + ageWidth;
  const titleWidth = Math.max(8, cols - fixed);

  return rows.map((row, index) => {
    const selected = index === state.selectedIndex;
    const bg: { readonly bg?: TuiSurface } = selected ? { bg: "high" } : {};
    const cursor = paint(selected ? "▸" : " ", "primary", bg);
    const title = paint(
      pad(truncate(row.title, titleWidth), titleWidth),
      selected ? "text" : "muted",
      bg,
    );
    const project = paint(pad(truncate(row.projectTitle, projectWidth), projectWidth), "faint", bg);
    const branch = paint(pad(truncate(row.branch ?? "", branchWidth), branchWidth), "info", bg);
    const fleet = paint(pad(truncate(row.fleet ?? "", fleetWidth), fleetWidth), "ok", bg);
    const when = paint(ages[index] ?? "", "faint", bg);
    const line = `${cursor} ${title}  ${project}  ${branch}  ${fleet}  ${when}`;
    // A selected row is a surface, so the band reaches the right edge.
    const rest = Math.max(0, cols - visibleWidth(line));
    const tail = selected && rest > 0 ? paint(" ".repeat(rest), "text", bg) : "";
    return truncate(`${line}${tail}`, cols);
  });
}

/** Running, settled, failed, and spend for the open thread. */
function fleetCounts(state: TuiState): {
  readonly total: number;
  readonly running: number;
  readonly settled: number;
  readonly failed: number;
  readonly tokens: number;
} {
  const activities = state.threadSnapshot?.thread.activities ?? [];
  const agents = foldSubagentActivities(activities, {
    sessionLive: state.threadSnapshot?.thread.session?.status === "running",
  });
  const running = agents.filter((agent) =>
    ["pending", "running", "waiting"].includes(agent.status),
  ).length;
  const failed = agents.filter((agent) =>
    ["failed", "cancelled", "interrupted"].includes(agent.status),
  ).length;
  return {
    total: agents.length,
    running,
    // The bar fills on settled work: a finished fleet reads 6/6, not 0/6.
    settled: agents.length - running - failed,
    failed,
    tokens: agents.reduce((total, agent) => total + (agent.usage?.totalTokens ?? 0), 0),
  };
}

/** The shell snapshot the list renders; empty until one arrives. */
function threadRowsFrom(state: TuiState) {
  return state.shellSnapshot === null ? state.threads : threadRows(state.shellSnapshot);
}

/**
 * A task id is long and only means anything as an identity; when the agent it
 * points at is not in this roster, the tail keeps the link readable.
 */
function shortAgentId(id: string): string {
  const tail = id.split(/[-_]/u).at(-1) ?? id;
  return `#${tail.length > 6 ? tail.slice(-6) : tail}`;
}

function agentLines(state: TuiState, cols: number): string[] {
  const out: string[] = [];
  const byId = new Map(state.agents.map((agent) => [agent.id, agent.label]));
  state.agents.forEach((agent, index) => {
    const selected = index === state.agentIndex;
    const tone = statusTone(agent.status);
    // Live work spins, so the panel moves while the fleet does.
    const glyph =
      agent.status === "running" || agent.status === "pending"
        ? (SPINNER[state.spinner % SPINNER.length] ?? "◐")
        : statusGlyph(agent.status);
    const link =
      agent.parentId === null
        ? ""
        : ` ↳ retry of ${byId.get(agent.parentId) ?? shortAgentId(agent.parentId)}`;
    // The name carries the agent's own hue, the status word carries the state:
    // the roster reads as a fleet of individuals, not as a column of labels.
    const hue = fleetHue(agent.id);
    const head = `${selected ? paint("▸", "primary") : " "} ${paint(glyph, tone)} ${paint(
      agent.label,
      hue,
      selected ? { bold: true } : undefined,
    )} ${paint("·", "faint")} ${paint(agent.status, tone)}`;
    const tail = `${agent.activity.length === 0 ? "" : paint(` — ${agent.activity}`, "muted")}${paint(
      link,
      "faint",
    )}`;
    out.push(truncate(`${head}${tail}`, cols));
    const meta = [
      agent.model,
      agent.tokens === null ? null : `${agent.tokens} tokens`,
      agent.tools === null ? null : `${agent.tools} tools`,
      agent.elapsed,
      agent.attempt === null ? null : `run ${agent.attempt}`,
      selected ? "l log · x stop · r restart" : null,
    ]
      .filter((part): part is string => part !== null && part.length > 0)
      .join(" · ");
    out.push(paint(truncate(`    ${meta}`, cols), "faint"));
  });
  return out;
}

function settingsLines(state: TuiState, cols: number): string[] {
  return settingsRows(state).map((row, index) => {
    const selected = index === state.settingsIndex;
    const label = pad(truncate(row.label, LABEL_COLS), LABEL_COLS);
    return paint(
      truncate(`${selected ? "▸" : " "} ${label} ${row.value}`, cols),
      selected ? "primary" : "text",
    );
  });
}

function workflowLines(state: TuiState, cols: number): string[] {
  return agentTunnelScreenRows(state).map((row, index) => {
    const selected = index === state.agentTunnelIndex;
    const label = pad(truncate(row.label, LABEL_COLS), LABEL_COLS);
    return paint(
      truncate(`${selected ? "▸" : " "} ${label} ${row.value}`, cols),
      selected ? "primary" : "text",
    );
  });
}

function usageLines(state: TuiState, cols: number): string[] {
  const activities = state.threadSnapshot?.thread.activities ?? [];
  const agents = foldSubagentActivities(activities, {
    sessionLive: state.threadSnapshot?.thread.session?.status === "running",
  });
  return agentUsageLines(agents).map((line) => paint(truncate(line, cols), "text"));
}

function logLines(state: TuiState, cols: number): string[] {
  const lines = state.agentLog?.lines ?? [];
  if (state.agentLog?.error != null) {
    return [paint(truncate(state.agentLog.error, cols), "muted")];
  }
  if (lines.length === 0) return [paint("waiting for the agent to write something…", "faint")];
  return lines.map((line) =>
    paint(
      truncate(line.startsWith("#") ? line : `  ${line}`, cols),
      line.startsWith("#") ? "muted" : "text",
    ),
  );
}

/** The body for the current view, before windowing. */
function bodyLines(state: TuiState, cols: number, now: number): string[] {
  switch (state.view) {
    case "threads": {
      const list = listLines(state, cols, now);
      return list.length > 0
        ? list
        : [paint("no threads yet — /new starts one, /add-project registers a directory", "muted")];
    }
    case "thread":
      return transcriptLines(state, cols);
    case "agents":
      return state.agents.length > 0
        ? agentLines(state, cols)
        : [paint("no sub-agents yet for this thread", "muted")];
    case "agentLog":
      return logLines(state, cols);
    case "usage":
      return usageLines(state, cols);
    case "settings":
      return settingsLines(state, cols);
    case "agenttunnel":
      return workflowLines(state, cols);
  }
}

/** The top or bottom slice of a list, keeping the cursor visible. */
function windowed<A>(lines: ReadonlyArray<A>, height: number, cursor: number): ReadonlyArray<A> {
  if (lines.length <= height) return lines;
  const start = Math.min(Math.max(0, cursor - Math.floor(height / 2)), lines.length - height);
  return lines.slice(start, start + height);
}

/** The palette: one command per row, or the rows the reference TUIs call a menu. */
function paletteLines(state: TuiState, cols: number, budget: number): string[] {
  const rows = commandSuggestions(state);
  if (rows.length === 0) return [];
  const selected = highlightedSuggestion(state)?.id;
  const shown = rows.slice(0, Math.min(budget, MAX_PALETTE_ROWS, rows.length));
  return card(
    `commands · ${rows.length}`,
    shown.map((row) => {
      const chosen = row.id === selected;
      const marker = chosen ? "▸" : " ";
      const label = pad(truncate(row.label, 22), 22);
      const line = `${marker} ${label} ${row.detail ?? ""}`;
      // The cursor row is the one you are about to run: say so in colour.
      return chosen ? paint(line, "primary", { bold: true }) : line;
    }),
    cols,
  );
}

function pickerLinesFor(state: TuiState, cols: number, budget: number): string[] {
  const picker = state.picker;
  if (picker === null) return [];
  const rows = pickerItems(state).map((row, index) => ({ row, index }));
  // An empty picker should say why it is empty, not just be empty.
  if (rows.length === 0) {
    return card(picker.title, ["nothing to choose yet — /add-project <path>"], cols);
  }
  const shown = windowed(rows, Math.max(1, budget - 3), picker.index);
  return card(
    `${picker.title} · ${rows.length}`,
    shown.map(({ row, index }) => {
      const chosen = index === picker.index;
      const marker = chosen ? "▸" : " ";
      const label = pad(truncate(row.label, 26), 26);
      const line = `${marker} ${label} ${row.detail ?? ""}`;
      return chosen ? paint(line, "primary", { bold: true }) : line;
    }),
    cols,
  );
}

/**
 * The composer, in Claude Code's shape: a rounded box around the input, opened
 * by a chevron, with the mode and the model under it and the shortcut hint last.
 */
function composerLines(state: TuiState, cols: number): string[] {
  const width = Math.max(8, cols);
  const inner = width - 4;
  // Square corners on purpose: the rounded set (╭╮╰╯) is missing from plenty of
  // monospace fonts, and a box whose corners and rules vanish reads as a glitch.
  const top = `┌${"─".repeat(width - 2)}┐`;
  const bottom = `└${"─".repeat(width - 2)}┘`;
  const empty = state.input.length === 0;
  const text = empty
    ? paint("ask anything, or / for commands", "faint")
    : paint(state.input, "text");
  const cursor = empty ? "" : paint(CURSOR, "primary");
  const body = `${paint(">", "primary", { bold: true })} ${truncate(`${text}${cursor}`, inner - 2)}`;
  const prompt = `│ ${pad(body, inner)} │`;

  const provider = activeProvider(state);
  const model =
    state.modelSelection?.model ??
    state.preferences.defaultModel ??
    provider?.models[0]?.slug ??
    null;
  const left = [
    paint(state.draft === null ? "auto" : `new thread · ${state.draft.projectTitle}`, "muted"),
    model === null ? null : paint(model, "muted"),
    provider === null ? null : paint(provider.label, "muted"),
  ]
    .filter((part): part is string => part !== null)
    .join(paint(" · ", "faint"));
  // Claude Code's hint line, with the bindings this client actually answers to.
  const right = `? ${paint("for shortcuts", "faint")}`;
  const gap = Math.max(1, cols - 2 - visibleWidth(left) - visibleWidth(right));
  const meta = `  ${left}${" ".repeat(gap)}${right}`;
  // The frame is drawn brighter than a transcript rule: it is chrome, and chrome
  // that disappears into the background is worse than no chrome at all.
  return [paint(top, "faint"), prompt, paint(bottom, "faint"), meta];
}

/**
 * The status strip: what the workflow is doing right now, the way omp's footer
 * states it — state and elapsed on one line, the workspace and how far the
 * fleet has got on the next. Only facts the server actually sends.
 */
function statusStrip(state: TuiState, cols: number, now: number): string[] {
  const session = state.threadSnapshot?.thread.session ?? null;
  const row = state.threads.find((entry) => entry.id === state.openThreadId) ?? null;
  const phase = SPINNER[state.spinner % SPINNER.length] ?? "◐";
  // One predicate for the glyph, the tone and the word: a strip that says
  // "working" beside a pause glyph is a strip nobody can believe.
  const running = isThreadRunning(state) || session?.status === "running";
  const stateWord = session === null ? "no session" : running ? "working" : session.status;

  const failed = session?.status === "error";
  const stateTone: TuiColor = failed ? "err" : running ? "warn" : "muted";

  const left = [
    failed ? paint("✗", "err") : running ? paint(phase, "warn") : paint("⏸", "faint"),
    paint(stateWord, stateTone),
    elapsed(state.runningSince, now) === null
      ? null
      : paint(elapsed(state.runningSince, now) ?? "", "muted"),
    session?.providerName === null || session?.providerName === undefined
      ? null
      : paint(session.providerName, "muted"),
  ]
    .filter((part): part is string => part !== null)
    .join(paint(" · ", "faint"));

  const goal =
    failed && session?.lastError != null
      ? truncate(session.lastError, Math.max(20, Math.floor(cols / 2)))
      : state.recap === null
        ? state.title
        : state.recap;
  const goalGap = Math.max(1, cols - visibleWidth(left) - visibleWidth(goal) - 1);
  const first = `${left}${" ".repeat(goalGap)}${paint(truncate(goal, cols), "faint")}`;

  const counts = fleetCounts(state);
  // The ledger says who is where: a chip each for the workspace facts, then the
  // fleet ribbon, which is the one thing this client is about.
  const second = [
    row?.projectTitle === null || row?.projectTitle === undefined
      ? null
      : chip(row.projectTitle, "text"),
    row?.branch === null || row?.branch === undefined ? null : chip(row.branch, "info"),
    counts.total === 0 ? paint("no agents yet", "faint") : ribbon(counts),
    counts.running > 0 ? paint(`${counts.running} running`, "warn") : null,
    counts.failed > 0 ? paint(`${counts.failed} failed`, "err") : null,
    counts.tokens === 0 ? null : paint(`${formatTokens(counts.tokens)} tok`, "muted"),
  ]
    .filter((part): part is string => part !== null)
    .join(" ");

  return [pad(truncate(first, cols), cols), pad(truncate(second, cols), cols)];
}

/**
 * The fleet ribbon: one cell per agent, coloured by how it ended, so a settled
 * fleet reads green and a broken one shows red at the same glance. This is the
 * client's own instrument — the references track one agent, we track many.
 */
function ribbon(counts: {
  readonly settled: number;
  readonly running: number;
  readonly failed: number;
  readonly total: number;
}): string {
  const cells = [
    counts.settled === 0 ? "" : paint("▰".repeat(counts.settled), "ok"),
    counts.running === 0 ? "" : paint("▰".repeat(counts.running), "warn"),
    counts.failed === 0 ? "" : paint("▰".repeat(counts.failed), "err"),
  ].join("");
  return `${cells} ${paint(`${counts.settled}/${counts.total} settled`, "muted")}`;
}

function hints(state: TuiState, cols: number): string {
  const pairs = isSplit(state)
    ? (PANEL_HINTS[state.view] ?? ([["esc", "close panel"]] as const))
    : KEY_HINTS[state.view];
  const rendered = pairs.map(([key, label]) => keycap(key, label)).join(paint("   ", "faint"));
  return truncate(rendered, cols);
}

/**
 * The whole frame: exactly `size.rows` lines, each exactly `size.cols` wide.
 * The composer only exists where typing means something.
 */
/** The right rail: opencode shows one above 120 columns, 42 wide. */
const RAIL_COLS = 42;
const RAIL_MIN_WIDTH = 120;

export function renderFrame(state: TuiState, size: FrameSize): string[] {
  const rail =
    size.cols > RAIL_MIN_WIDTH && state.view === "thread" ? sidebarLines(state, size) : null;
  if (rail === null) return renderMain(state, size);
  const mainCols = Math.max(20, size.cols - RAIL_COLS - 1);
  const main = renderMain(state, { ...size, cols: mainCols });
  return main.map((line, row) => {
    const right = rail[row] ?? "";
    return `${pad(truncate(line, mainCols), mainCols)} ${pad(truncate(right, RAIL_COLS), RAIL_COLS)}`;
  });
}

/**
 * The context rail: what this thread has spent, on what, and where. A wide
 * terminal was leaving half the screen empty; this is what belongs there.
 */
function sidebarLines(state: TuiState, size: FrameSize): string[] {
  const counts = fleetCounts(state);
  const session = state.threadSnapshot?.thread.session ?? null;
  const row = state.threads.find((entry) => entry.id === state.openThreadId) ?? null;
  const provider = activeProvider(state);
  const model = state.modelSelection?.model ?? state.preferences.defaultModel ?? null;
  const spent = state.agents.reduce((total, agent) => total + (agent.costUsd ?? 0), 0);

  const heading = (text: string): string => paint(text, "text", { bold: true });
  const muted = (text: string): string => paint(text, "muted");

  const out: string[] = [""];
  const section = (title: string, lines: ReadonlyArray<string | null>): void => {
    out.push(heading(title));
    for (const line of lines) {
      if (line !== null) out.push(muted(line));
    }
    out.push("");
  };

  out.push(muted(session === null ? "no session" : `session · ${session.status}`));
  out.push("");
  section("Context", [
    `${counts.tokens === 0 ? 0 : formatTokens(counts.tokens)} tokens`,
    spent === 0 ? null : `${formatCost(spent)} spent`,
  ]);
  section("Model", [model ?? "provider default", provider?.label ?? "no provider"]);
  section("Workspace", [
    row?.projectTitle ?? null,
    row?.branch ?? null,
    state.draft === null ? null : `new thread · ${state.draft.projectTitle}`,
  ]);
  section("Agents", [
    counts.total === 0 ? "none yet" : `${counts.total} · ${counts.settled} settled`,
    counts.running === 0 ? null : `${counts.running} running`,
    counts.failed === 0 ? null : `${counts.failed} failed`,
  ]);

  // The rail's own footer, the way opencode pins the workspace at its bottom.
  const footer = [
    row?.projectTitle === null || row?.projectTitle === undefined
      ? null
      : paint(truncate(row.projectTitle, RAIL_COLS), "faint"),
    paint(truncate(BRAND, RAIL_COLS), "faint"),
  ].filter((line): line is string => line !== null);
  const height = Math.max(0, size.rows - out.length - footer.length);
  const padded = [...out, ...Array.from({ length: height }, () => ""), ...footer];
  return fill(padded, size.rows, RAIL_COLS);
}

/**
 * Everything but the rail: the frame in one column, exactly `size.rows` lines of
 * exactly `size.cols` columns.
 */
function renderMain(state: TuiState, size: FrameSize): string[] {
  const { rows, cols } = size;
  const showComposer = !isSplit(state);
  const overlay = paletteLines(state, cols, MAX_PALETTE_ROWS);
  const picking = pickerLinesFor(state, cols, MAX_PALETTE_ROWS);
  const overlayRows = picking.length > 0 ? picking.length : overlay.length;

  const recap =
    state.recap === null ? [] : [`${paint("※ ", "faint")}${paint(state.recap, "muted")}`];
  const draft =
    state.draft === null
      ? []
      : [paint(`new thread in ${state.draft.projectTitle} — type the first message`, "muted")];

  const split = isSplit(state);
  const chrome =
    HEADER_ROWS +
    STATUS_ROWS +
    (showComposer ? COMPOSER_ROWS : 0) +
    HINTS_ROWS +
    recap.length +
    draft.length +
    overlayRows +
    (split ? 1 : 0);
  const available = Math.max(2, rows - chrome);
  // The running thread keeps the larger share; the panel gets the rest.
  const threadHeight = split ? Math.max(3, Math.floor(available * 0.6)) : available;
  const bodyHeight = split ? Math.max(2, available - threadHeight) : available;

  const cursor =
    state.view === "thread" || state.view === "agentLog"
      ? // Both follow the newest line: a live log should not scroll away.
        Number.MAX_SAFE_INTEGER
      : state.view === "agents"
        ? state.agentIndex * 2
        : state.view === "settings"
          ? state.settingsIndex
          : state.view === "agenttunnel"
            ? state.agentTunnelIndex
            : state.selectedIndex;

  const greeting =
    state.welcome && state.view === "threads" && state.picker === null
      ? welcomeLines(state, cols, bodyHeight, size.now ?? 0)
      : null;
  const body = windowed(
    greeting === null ? bodyLines(state, cols, size.now ?? 0) : greeting,
    bodyHeight,
    greeting === null ? cursor : 0,
  );
  const out: string[] = [pad(header(state, cols), cols), rule(cols)];
  if (split) {
    // The thread stays on top, scrolled to its newest line.
    const transcript = windowed(
      transcriptLines(state, cols),
      threadHeight,
      Number.MAX_SAFE_INTEGER,
    );
    out.push(...fill(transcript, threadHeight, cols));
    out.push(pad(separator(state, cols), cols));
  }
  // A live transcript grows upward from the composer; an idle screen is a hero,
  // centred in the space it has rather than pinned to the top of it.
  const anchored = state.view === "thread" || state.view === "agentLog";
  out.push(
    ...(greeting !== null
      ? fillCentered(body, bodyHeight, cols)
      : anchored
        ? fillAbove(body, bodyHeight, cols)
        : fill(body, bodyHeight, cols)),
  );
  out.push(...fill(recap, recap.length, cols));
  out.push(...fill(draft, draft.length, cols));
  out.push(...fill(picking.length > 0 ? picking : overlay, overlayRows, cols));
  if (showComposer) out.push(...composerLines(state, cols));
  // The app passes the clock; a frame rendered without one has no elapsed row.
  out.push(...statusStrip(state, cols, size.now ?? 0));
  out.push(pad(hints(state, cols), cols));
  return fill(out, rows, cols);
}
