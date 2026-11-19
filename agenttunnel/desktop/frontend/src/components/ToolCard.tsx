import { memo, useMemo, useState } from "react";
import { wordDiff, type TokSpan } from "../lib/worddiff";
import {
  formatDuration,
  parseToolDiff,
  toolDuration,
  type ToolDiffLine,
  type ToolFields,
} from "../lib/toolCalls";

export type ToolLine = { kind: string; text: string } & ToolFields;

type Props = {
  line: ToolLine;
  /** The agent's run is over, so a call still marked running never finished. */
  stopped: boolean;
};

/** One structured tool call: a one-line summary that expands to its
 *  arguments, diff, and output. Finished successful calls start collapsed. */
function ToolCardView({ line, stopped }: Props) {
  const tool = line.tool!;
  const status = tool.status === "running" && stopped ? "stopped" : tool.status;
  const [userOpen, setUserOpen] = useState<boolean | null>(null);
  const open = userOpen ?? (status === "running" || status === "failed");

  // A "tool" line's text is the call as the harness prints it: summary first,
  // then remaining arguments. A result-only card falls back to the tool name.
  const isCall = line.kind === "tool";
  const nl = line.text.indexOf("\n");
  const first = isCall ? (nl < 0 ? line.text : line.text.slice(0, nl)) : tool.name || "tool";
  const summary = first.replace(/^(\$|→)\s+/, "");
  const detail = isCall && nl >= 0 ? line.text.slice(nl + 1) : "";
  const icon = tool.command ? "$" : tool.diff || /edit|write|patch/i.test(tool.name ?? "") ? "±" : "→";
  const ms = status === "running" ? null : toolDuration(line);

  // One glyph per status, plus a word, so state never rests on colour alone.
  const glyph = { running: "●", done: "✓", failed: "✕", stopped: "■" }[status] ?? "·";
  const state =
    status === "failed" && tool.exitCode !== undefined ? `exit ${tool.exitCode}` : status === "done" ? "" : status;
  const stats = useMemo(() => (tool.diff ? diffStats(tool.diff) : null), [tool.diff]);

  return (
    <div className={`tool-card ${status}`}>
      <button
        type="button"
        className="tool-head"
        onClick={() => setUserOpen(!open)}
        aria-expanded={open}
        title={first}
      >
        <span className="tool-glyph" aria-hidden="true">
          {glyph}
        </span>
        <span className="tool-icon" aria-hidden="true">
          {icon}
        </span>
        <span className="tool-summary">{summary}</span>
        {stats && (
          <span className="tool-stats">
            <span className="tc-add">+{stats.add}</span> <span className="tc-del">−{stats.del}</span>
          </span>
        )}
        {state && <span className="tool-state">{state}</span>}
        {ms !== null && <span className="tool-time">{formatDuration(ms)}</span>}
        <span className="tool-caret" aria-hidden="true">
          {open ? "▾" : "▸"}
        </span>
      </button>
      {open && (
        <div className="tool-body">
          {tool.diff ? (
            <ToolDiff diff={tool.diff} />
          ) : (
            detail && <div className="tool-args">{detail}</div>
          )}
          {line.output && <div className="tool-output">{line.output}</div>}
        </div>
      )}
    </div>
  );
}

function diffStats(diff: string): { add: number; del: number } {
  let add = 0;
  let del = 0;
  for (const l of parseToolDiff(diff)) {
    if (l.kind === "add") add++;
    else if (l.kind === "delete") del++;
  }
  return { add, del };
}

/** Logs are refetched wholesale, so lines are compared by content. */
function same(a: Props, b: Props): boolean {
  const x = a.line;
  const y = b.line;
  return (
    a.stopped === b.stopped &&
    x.kind === y.kind &&
    x.text === y.text &&
    x.output === y.output &&
    x.startedAt === y.startedAt &&
    x.endedAt === y.endedAt &&
    x.tool?.status === y.tool?.status &&
    x.tool?.exitCode === y.tool?.exitCode &&
    x.tool?.durationMs === y.tool?.durationMs &&
    x.tool?.diff === y.tool?.diff
  );
}

export const ToolCard = memo(ToolCardView, same);

/** An edit's diff, word-highlighted where a removed line pairs with an added
 *  one. Rendered only while its card is open. */
function ToolDiff({ diff }: { diff: string }) {
  const lines = useMemo(() => parseToolDiff(diff), [diff]);
  // A big diff renders its head first; the rest mounts on request, so opening
  // a card never builds hundreds of rows at once.
  const [all, setAll] = useState(false);
  const hidden = all ? 0 : Math.max(0, lines.length - DIFF_WINDOW);
  const rows = useMemo(
    () => renderDiff(hidden > 0 ? lines.slice(0, DIFF_WINDOW) : lines),
    [lines, hidden > 0],
  );
  return (
    <div className="tool-diff">
      {rows}
      {hidden > 0 && (
        <button type="button" className="tool-more" onClick={() => setAll(true)}>
          Show {hidden} more lines
        </button>
      )}
    </div>
  );
}
const DIFF_WINDOW = 120;

function renderDiff(lines: ToolDiffLine[]) {
  const out: React.ReactNode[] = [];
  const row = (l: ToolDiffLine, content: React.ReactNode = null) => (
    <div key={out.length} className={`diff-line ${l.kind}`}>
      <span className="code">{content ?? (l.text || " ")}</span>
    </div>
  );
  let k = 0;
  while (k < lines.length) {
    if (lines[k].kind !== "delete") {
      out.push(row(lines[k++]));
      continue;
    }
    const dels: ToolDiffLine[] = [];
    while (k < lines.length && lines[k].kind === "delete") dels.push(lines[k++]);
    const adds: ToolDiffLine[] = [];
    while (k < lines.length && lines[k].kind === "add") adds.push(lines[k++]);
    const n = Math.min(dels.length, adds.length);
    for (let p = 0; p < n; p++) {
      const { oldSpans, newSpans } = wordDiff(dels[p].text, adds[p].text);
      out.push(row(dels[p], spans(oldSpans, "del")));
      out.push(row(adds[p], spans(newSpans, "add")));
    }
    for (let p = n; p < dels.length; p++) out.push(row(dels[p]));
    for (let p = n; p < adds.length; p++) out.push(row(adds[p]));
  }
  return out;
}

function spans(list: TokSpan[], side: "add" | "del") {
  return list.map((s, i) =>
    s.changed ? (
      <span key={i} className={`tok-${side}`}>
        {s.text || " "}
      </span>
    ) : (
      <span key={i}>{s.text || " "}</span>
    ),
  );
}
