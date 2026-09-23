import type { OrchestrationThreadActivity } from "@t3tools/contracts";
import { foldSubagentActivities } from "@t3tools/client-runtime/state/subagentRuntime";

import { agentRows, agentUsageLines } from "./agents.ts";
import { lineGlyph, statusGlyph, threadRows, threadView, type TuiLine } from "./format.ts";
import {
  agentTunnelScreenRows,
  commandSuggestions,
  isThreadRunning,
  highlightedSuggestion,
  pickerItems,
  settingsRows,
  type TuiState,
} from "./state.ts";
import { pad, paint, truncate, visibleWidth, type TuiColor } from "./theme.ts";

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

const HEADER_ROWS = 1;
const COMPOSER_ROWS = 1;
const HINTS_ROWS = 1;
const MAX_PALETTE_ROWS = 8;
const CARD_MAX_COLS = 76;
const LABEL_COLS = 22;

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

function bar(fraction: number, width: number): string {
  const filled = Math.max(0, Math.min(width, Math.round(fraction * width)));
  return "▓".repeat(filled) + "░".repeat(Math.max(0, width - filled));
}

/** Composer cursor: a block, so it reads as an input even when empty. */
const CURSOR = "█";

const color = (kind: TuiLine["kind"]): TuiColor =>
  kind === "user" ? "primary" : kind === "agent" ? "faint" : kind === "system" ? "warn" : "text";

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
    threads: `AgentTunnel · threads (${state.threads.length})`,
    thread: `AgentTunnel · ${state.title}`,
    agents: `AgentTunnel · agents (${state.agents.length})`,
    agentLog: `AgentTunnel · ${state.agentLog?.label ?? "agent"} · log`,
    usage: "AgentTunnel · token usage",
    settings: "AgentTunnel · settings",
    agenttunnel: "AgentTunnel · workflow roles",
  };
  const title = isSplit(state) ? `AgentTunnel · ${state.title}` : titles[state.view];
  const right = fleetText(state) ?? state.status;
  const gap = Math.max(1, cols - visibleWidth(title) - visibleWidth(right) - 1);
  return `${truncate(title, cols)}${" ".repeat(gap)}${paint(truncate(right, cols), "muted")}`;
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

/** The transcript: physical lines, wrapped, newest at the bottom. */
function transcriptLines(state: TuiState, cols: number): string[] {
  const out: string[] = [];
  for (const line of state.lines) {
    const wrapped = wrap(line.text, cols - 2);
    wrapped.forEach((text, index) => {
      const marker = index === 0 ? `${lineGlyph(line.kind)} ` : "  ";
      out.push(paint(marker + text, color(line.kind)));
    });
  }
  return out;
}

function listLines(state: TuiState, cols: number): string[] {
  return threadRowsFrom(state).map((row, index) => {
    const selected = index === state.selectedIndex;
    const text = `${selected ? "▸" : " "} ${row.title}${row.branch === null ? "" : ` · ${row.branch}`}  (${row.projectTitle})`;
    return paint(truncate(text, cols), selected ? "primary" : "text");
  });
}

/** Running, settled, failed, and spend for the open thread. */
function fleetCounts(state: TuiState): {
  readonly total: number;
  readonly running: number;
  readonly tokens: number;
} {
  const activities = state.threadSnapshot?.thread.activities ?? [];
  const agents = foldSubagentActivities(activities, {
    sessionLive: state.threadSnapshot?.thread.session?.status === "running",
  });
  return {
    total: agents.length,
    running: agents.filter((agent) => ["pending", "running", "waiting"].includes(agent.status))
      .length,
    tokens: agents.reduce((total, agent) => total + (agent.usage?.totalTokens ?? 0), 0),
  };
}

/** The shell snapshot the list renders; empty until one arrives. */
function threadRowsFrom(state: TuiState) {
  return state.shellSnapshot === null ? state.threads : threadRows(state.shellSnapshot);
}

function agentLines(state: TuiState, cols: number): string[] {
  const out: string[] = [];
  state.agents.forEach((agent, index) => {
    const selected = index === state.agentIndex;
    const tone = selected ? "primary" : statusTone(agent.status);
    out.push(
      paint(
        truncate(
          `${selected ? "▸" : " "} ${statusGlyph(agent.status)} ${agent.label} · ${agent.status}${
            agent.activity.length === 0 ? "" : ` — ${agent.activity}`
          }`,
          cols,
        ),
        tone,
      ),
    );
    const meta = [
      agent.model,
      agent.tokens === null ? null : `${agent.tokens} tokens`,
      agent.tools === null ? null : `${agent.tools} tools`,
      agent.elapsed,
      agent.attempt === null ? null : `run ${agent.attempt}`,
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
  if (state.agentLog?.error != null) return [paint(truncate(state.agentLog.error, cols), "err")];
  return lines.map((line) =>
    paint(
      truncate(line.startsWith("#") ? line : `  ${line}`, cols),
      line.startsWith("#") ? "muted" : "text",
    ),
  );
}

/** The body for the current view, before windowing. */
function bodyLines(state: TuiState, cols: number): string[] {
  switch (state.view) {
    case "threads":
      return listLines(state, cols).length > 0
        ? listLines(state, cols)
        : [paint("no threads yet — /new starts one, /add-project registers a directory", "muted")];
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
      const marker = row.id === selected ? "▸" : " ";
      const label = pad(truncate(row.label, 22), 22);
      return `${marker} ${label} ${row.detail ?? ""}`;
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
      const marker = index === picker.index ? "▸" : " ";
      const label = pad(truncate(row.label, 26), 26);
      return `${marker} ${label} ${row.detail ?? ""}`;
    }),
    cols,
  );
}

function composerLine(state: TuiState, cols: number): string {
  return pad(
    truncate(`${paint("❯ ", "primary")}${state.input}${paint(CURSOR, "faint")}`, cols),
    cols,
  );
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
  const running = isThreadRunning(state);
  const stateWord =
    session === null ? "no session" : session.status === "running" ? "working" : session.status;

  const left = [
    running ? paint(phase, "warn") : paint("⏸", "faint"),
    paint(stateWord, running ? "warn" : "muted"),
    elapsed(state.runningSince, now) === null
      ? null
      : paint(elapsed(state.runningSince, now) ?? "", "muted"),
    session?.providerName === null || session?.providerName === undefined
      ? null
      : paint(session.providerName, "muted"),
  ]
    .filter((part): part is string => part !== null)
    .join(paint(" · ", "faint"));

  const goal = state.recap === null ? state.title : state.recap;
  const goalGap = Math.max(1, cols - visibleWidth(left) - visibleWidth(goal) - 1);
  const first = `${left}${" ".repeat(goalGap)}${paint(truncate(goal, cols), "faint")}`;

  const counts = fleetCounts(state);
  const progress =
    counts.total === 0
      ? null
      : `${paint(bar(counts.running / counts.total, 6), "primary")} ${counts.running}/${counts.total}`;
  const second = [
    row?.projectTitle === null || row?.projectTitle === undefined
      ? null
      : paint(row.projectTitle, "muted"),
    row?.branch === null || row?.branch === undefined ? null : paint(row.branch, "muted"),
    counts.total === 0 ? paint("no agents yet", "faint") : paint(`${counts.total} agents`, "muted"),
    progress,
    counts.tokens === 0 ? null : paint(`${formatTokens(counts.tokens)} tok`, "muted"),
  ]
    .filter((part): part is string => part !== null)
    .join(paint(" · ", "faint"));

  return [pad(truncate(first, cols), cols), pad(truncate(second, cols), cols)];
}

function hints(state: TuiState, cols: number): string {
  if (isSplit(state)) {
    const panel: Partial<Record<TuiState["view"], string>> = {
      agents: "↑/↓ move · enter open log · x stop turn · esc close panel",
      agentLog: "esc back to agents · ctrl-r refresh",
      settings: "↑/↓ move · enter change · esc close panel",
      agenttunnel: "↑/↓ move · enter change · esc close panel",
    };
    return paint(truncate(panel[state.view] ?? "esc close panel", cols), "faint");
  }
  const byView: Record<TuiState["view"], string> = {
    threads: "↑/↓ move · enter open · / commands · ctrl-p palette · ctrl-c quit",
    thread: "enter send · / commands · ctrl-a agents · ctrl-i interrupt · esc back",
    agents: "↑/↓ move · enter open log · x stop turn · esc back",
    agentLog: "esc back to agents · ctrl-r refresh",
    usage: "esc back",
    settings: "↑/↓ move · enter change · esc back",
    agenttunnel: "↑/↓ move · enter change · esc back",
  };
  return paint(truncate(byView[state.view], cols), "faint");
}

/**
 * The whole frame: exactly `size.rows` lines, each exactly `size.cols` wide.
 * The composer only exists where typing means something.
 */
export function renderFrame(state: TuiState, size: FrameSize): string[] {
  const { rows, cols } = size;
  const showComposer =
    !isSplit(state) && (state.view === "thread" || state.draft !== null || state.input.length > 0);
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
    state.view === "thread"
      ? Number.MAX_SAFE_INTEGER
      : state.view === "agents"
        ? state.agentIndex * 2
        : state.view === "settings"
          ? state.settingsIndex
          : state.view === "agenttunnel"
            ? state.agentTunnelIndex
            : state.selectedIndex;

  const body = windowed(bodyLines(state, cols), bodyHeight, cursor);
  const out: string[] = [pad(header(state, cols), cols)];
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
  out.push(...fill(body, bodyHeight, cols));
  out.push(...fill(recap, recap.length, cols));
  out.push(...fill(draft, draft.length, cols));
  out.push(...fill(picking.length > 0 ? picking : overlay, overlayRows, cols));
  if (showComposer) out.push(composerLine(state, cols));
  // The app passes the clock; a frame rendered without one has no elapsed row.
  out.push(...statusStrip(state, cols, size.now ?? 0));
  out.push(pad(hints(state, cols), cols));
  return fill(out, rows, cols);
}
