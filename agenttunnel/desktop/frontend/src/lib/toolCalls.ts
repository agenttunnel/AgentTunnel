// Structured tool calls on agent log lines (contracts.ToolCall). A call and
// its result arrive as two lines sharing tool.id: the call (kind "tool") says
// what ran, the result (kind "raw"/"error") brings the final status with its
// output as text. Lines without `tool` render as plain log lines.

export type ToolStatus = "running" | "done" | "failed";

export type ToolCall = {
  id: string;
  name?: string;
  command?: string;
  path?: string;
  status: ToolStatus;
  exitCode?: number;
  durationMs?: number;
  /** Unified-diff body lines ("-", "+", " ", "@@"), capped by the daemon. */
  diff?: string;
};

/** What a folded tool line carries beside agentId/kind/text. */
export type ToolFields = {
  tool?: ToolCall;
  /** The result's output, folded in from the line that finished the call. */
  output?: string;
  /** When the call and its result were seen (ms), for a duration the
   * harness did not report. */
  startedAt?: number;
  endedAt?: number;
};

type Line = { agentId?: string; kind: string; text: string; at?: string } & ToolFields;

/** How far back a result looks for its call. Calls run in short bursts. */
const FOLD_WINDOW = 200;

/** Folds one line into `out` in place: a result merges into its call, any
 *  other line is appended. */
export function foldToolLine<T extends Line>(out: T[], line: T): void {
  const tool = line.tool;
  if (!tool) {
    out.push(line);
    return;
  }
  const seen = line.at ? Date.parse(line.at) : Date.now();
  const finished = tool.status !== "running";
  for (let i = out.length - 1; i >= Math.max(0, out.length - FOLD_WINDOW); i--) {
    const p = out[i];
    if (p.tool?.id !== tool.id || p.agentId !== line.agentId) continue;
    out[i] = {
      ...p,
      tool: { ...p.tool, ...tool },
      endedAt: finished ? seen : p.endedAt,
      output: line.kind === "tool" ? p.output : joinOutput(p.output, line.text),
    };
    return;
  }
  // A result whose call is gone (trimmed, or before this view) still shows
  // as a card, with its text as the output.
  out.push({
    ...line,
    output: line.kind === "tool" ? undefined : line.text,
    startedAt: seen,
    endedAt: finished ? seen : undefined,
  });
}

/** Appends to an immutable list, folding results, capped at `limit`. */
export function pushToolLine<T extends Line>(prev: T[], line: T, limit: number): T[] {
  const next = prev.slice();
  foldToolLine(next, line);
  return next.length > limit ? next.slice(next.length - limit) : next;
}

/** Folds a whole fetched log in one pass. */
export function foldToolLines<T extends Line>(lines: T[]): T[] {
  const out: T[] = [];
  for (const l of lines) foldToolLine(out, l);
  return out;
}

function joinOutput(prev: string | undefined, text: string): string {
  return prev ? `${prev}\n${text}` : text;
}

/** An agent whose run ended cannot still be running a tool. Unknown agents
 *  (state not loaded yet) are given the benefit of the doubt. */
export function agentStopped(status: string | undefined): boolean {
  return status !== undefined && status !== "running" && status !== "pending";
}

/** The call's duration in ms, preferring what the harness reported. */
export function toolDuration(l: ToolFields): number | null {
  if (l.tool?.durationMs) return l.tool.durationMs;
  if (l.startedAt === undefined || l.endedAt === undefined) return null;
  const ms = l.endedAt - l.startedAt;
  return ms > 0 ? ms : null;
}

export function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)}s`;
  return `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
}

export type ToolDiffLine = { kind: "add" | "delete" | "context" | "hunk" | "meta"; text: string };

/** Most diff lines a card renders; the daemon already caps the bytes. */
const MAX_DIFF_LINES = 600;

/** Parses a tool diff. A full unified diff (opencode) has file headers before
 *  its first hunk; the daemon's own edit diffs start straight at the body. */
export function parseToolDiff(diff: string): ToolDiffLine[] {
  const lines = diff.split("\n").slice(0, MAX_DIFF_LINES);
  const headed = /^(Index:|diff |--- )/.test(lines[0] ?? "") && lines.some((l) => l.startsWith("@@"));
  let inBody = !headed;
  return lines.map((l): ToolDiffLine => {
    if (l.startsWith("@@")) {
      inBody = true;
      return { kind: "hunk", text: l };
    }
    if (!inBody) return { kind: "meta", text: l };
    switch (l[0]) {
      case "+":
        return { kind: "add", text: l.slice(1) };
      case "-":
        return { kind: "delete", text: l.slice(1) };
      case " ":
        return { kind: "context", text: l.slice(1) };
      default:
        return { kind: "meta", text: l };
    }
  });
}
