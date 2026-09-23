import type { OrchestrationThreadActivity } from "@t3tools/contracts";
import { foldSubagentActivities } from "@t3tools/client-runtime/state/subagentRuntime";

import { agentRows, agentUsageLines } from "./agents.ts";
import { lineGlyph, statusGlyph, threadRows, threadView, type TuiLine } from "./format.ts";
import {
  agentTunnelScreenRows,
  commandSuggestions,
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
}

const HEADER_ROWS = 1;
const COMPOSER_ROWS = 1;
const HINTS_ROWS = 1;
const MAX_PALETTE_ROWS = 8;
const CARD_MAX_COLS = 76;
const LABEL_COLS = 22;

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
  const right = fleetText(state) ?? state.status;
  const gap = Math.max(1, cols - visibleWidth(titles[state.view]) - visibleWidth(right) - 1);
  return `${truncate(titles[state.view], cols)}${" ".repeat(gap)}${paint(truncate(right, cols), "muted")}`;
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
      return listLines(state, cols);
    case "thread":
      return transcriptLines(state, cols);
    case "agents":
      return agentLines(state, cols);
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

function hints(state: TuiState, cols: number): string {
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
  const showComposer = state.view === "thread" || state.draft !== null || state.input.length > 0;
  const overlay = paletteLines(state, cols, MAX_PALETTE_ROWS);
  const picking = pickerLinesFor(state, cols, MAX_PALETTE_ROWS);
  const overlayRows = picking.length > 0 ? picking.length : overlay.length;

  const recap =
    state.recap === null ? [] : [`${paint("※ ", "faint")}${paint(state.recap, "muted")}`];
  const draft =
    state.draft === null
      ? []
      : [paint(`new thread in ${state.draft.projectTitle} — type the first message`, "muted")];

  const chrome =
    HEADER_ROWS +
    (showComposer ? COMPOSER_ROWS : 0) +
    HINTS_ROWS +
    recap.length +
    draft.length +
    overlayRows;
  const bodyHeight = Math.max(1, rows - chrome);

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
  out.push(...fill(body, bodyHeight, cols));
  out.push(...fill(recap, recap.length, cols));
  out.push(...fill(draft, draft.length, cols));
  out.push(...fill(picking.length > 0 ? picking : overlay, overlayRows, cols));
  if (showComposer) out.push(composerLine(state, cols));
  out.push(pad(hints(state, cols), cols));
  return fill(out, rows, cols);
}
