import { visibleLines, type TuiState } from "./state.ts";

/**
 * Painting the state into terminal lines. Pure: it takes the size it is drawing
 * into and returns the exact lines to write, so the layout can be asserted in
 * tests instead of eyeballed. The renderer in `main.ts` only moves the cursor
 * and writes these strings.
 */

export interface TuiSize {
  readonly columns: number;
  readonly rows: number;
}

const HEADER_ROWS = 1;
const RECAP_ROWS = 1;
const INPUT_ROWS = 1;
const FOOTER_ROWS = 1;

function clip(text: string, columns: number): string {
  return text.length <= columns ? text : text.slice(0, Math.max(0, columns - 1));
}

function pad(text: string, columns: number): string {
  const clipped = clip(text, columns);
  return clipped.length === columns ? clipped : clipped.padEnd(columns, " ");
}

const MARKERS: Record<string, string> = {
  user: "› ",
  assistant: "  ",
  agent: "· ",
  system: "! ",
};

export function renderTui(state: TuiState, size: TuiSize): ReadonlyArray<string> {
  const columns = Math.max(20, size.columns);
  const bodyRows = Math.max(1, size.rows - HEADER_ROWS - RECAP_ROWS - INPUT_ROWS - FOOTER_ROWS);
  const lines: string[] = [];

  lines.push(
    pad(
      state.view === "threads"
        ? `AgentTunnel · threads (${state.threads.length})`
        : `AgentTunnel · ${state.title}`,
      columns,
    ),
  );

  if (state.view === "threads") {
    const rows = state.threads.slice(0, bodyRows);
    for (let index = 0; index < bodyRows; index += 1) {
      const row = rows[index];
      if (row === undefined) {
        lines.push(pad("", columns));
        continue;
      }
      const cursor = index === state.selectedIndex ? "▸ " : "  ";
      const branch = row.branch === null ? "" : ` · ${row.branch}`;
      lines.push(pad(`${cursor}${row.title}${branch}  (${row.projectTitle})`, columns));
    }
  } else {
    for (const line of visibleLines(state, bodyRows)) {
      lines.push(pad(`${MARKERS[line.kind] ?? "  "}${line.text}`, columns));
    }
    for (let index = visibleLines(state, bodyRows).length; index < bodyRows; index += 1) {
      lines.push(pad("", columns));
    }
  }

  lines.push(pad(state.recap === null ? "" : `※ recap: ${state.recap}`, columns));
  lines.push(pad(state.view === "threads" ? "" : `❯ ${state.input}`, columns));
  lines.push(
    pad(
      state.view === "threads"
        ? `${state.status}   ↑/↓ move · enter open · ctrl-r refresh · ctrl-c quit`
        : `${state.status}   enter send · ctrl-i interrupt · ↑/↓ scroll · esc back · ctrl-c quit`,
      columns,
    ),
  );

  return lines;
}
