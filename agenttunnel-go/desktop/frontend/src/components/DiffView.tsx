import { Fragment, useMemo, useRef, useState } from "react";
import { wordDiff, type TokSpan } from "../lib/worddiff";
import {
  ReviewThreads,
  reviewAnchorKey,
  type ReviewPeople,
  type ReviewThreadLike,
} from "./ReviewThreads";

// Local structural types matching the review.diff wire shape (same approach
// as AgentDetail / ThreadInfoDialog: no import of the app component).
export type DiffLineLike = { old?: number; new?: number; kind: string; text: string };
export type DiffFileLike = {
  path: string;
  oldPath?: string;
  additions: number;
  deletions: number;
  lines: DiffLineLike[] | null;
};
export type DiffLike = { files: DiffFileLike[] | null; raw: string; empty: boolean };
/** "old" numbers removed lines; "new" numbers added and context lines. */
export type Side = "old" | "new";
export type CommentLike = {
  id: string;
  file: string;
  line: number;
  endLine: number;
  side?: Side;
  body: string;
};
export type DraftLike = { file: string; line: number; endLine: number; side: Side; body: string };

type FileStatus = "added" | "deleted" | "modified";

const statusLetter: Record<FileStatus, string> = { added: "A", deleted: "D", modified: "M" };

/** Derives added/deleted/modified from the diff headers, like a review panel. */
export function fileStatus(f: DiffFileLike): FileStatus {
  for (const l of f.lines ?? []) {
    if (l.kind !== "meta") continue;
    if (l.text.startsWith("--- /dev/null")) return "added";
    if (l.text.startsWith("+++ /dev/null")) return "deleted";
  }
  return "modified";
}

/** "12" for a single line, "12-14" for a range. */
export function lineRange(line: number, endLine: number): string {
  return endLine > line ? `${line}-${endLine}` : `${line}`;
}

/** A pointer selection in progress: one file and side, numbered from..to. */
type Picking = { file: string; side: Side; from: number; to: number };

const sideOf = (l: DiffLineLike): Side => (l.kind === "delete" ? "old" : "new");
const numberOf = (l: DiffLineLike): number => (sideOf(l) === "old" ? l.old : l.new) ?? 0;

/** Whether a comment or draft span covers line n on this side of file. */
function covers(
  s: { file: string; side?: Side; line: number; endLine: number },
  file: string,
  side: Side,
  n: number,
): boolean {
  return s.file === file && (s.side ?? "new") === side && n >= s.line && n <= s.endLine;
}

function renderSpans(spans: TokSpan[], side: "add" | "del") {
  return spans.map((s, i) =>
    s.changed ? (
      <span key={i} className={`tok-${side}`}>
        {s.text || " "}
      </span>
    ) : (
      <span key={i}>{s.text || " "}</span>
    ),
  );
}

function scrollToFile(prefix: string, index: number) {
  document.getElementById(`${prefix}diff-file-${index}`)?.scrollIntoView({ block: "start" });
}

type HunkGroup = { header: DiffLineLike | null; lines: { line: DiffLineLike; index: number }[] };

function groupHunks(lines: DiffLineLike[]): { meta: DiffLineLike[]; hunks: HunkGroup[] } {
  const meta: DiffLineLike[] = [];
  const hunks: HunkGroup[] = [];
  lines.forEach((line, index) => {
    if (line.kind === "meta") {
      meta.push(line);
      return;
    }
    if (line.kind === "hunk") {
      hunks.push({ header: line, lines: [] });
      return;
    }
    if (hunks.length === 0) hunks.push({ header: null, lines: [] });
    hunks[hunks.length - 1].lines.push({ line, index });
  });
  return { meta, hunks };
}

/**
 * DiffView renders a working-tree diff opencode-style: a file jump list with
 * status badges and counts, then unified hunks with dual line-number gutters
 * and word-level change highlighting. Press a code line and drag (or
 * shift-click) to pick a range on one side of one file; releasing opens the
 * comment draft for review comments back to the agents.
 */
export function DiffView({
  diff,
  title,
  comments,
  draft,
  emptyText,
  idPrefix,
  onLineClick,
  onDraftBody,
  onAddComment,
  onCancelDraft,
  onRefresh,
  threads,
  people,
}: {
  diff: DiffLike | null;
  title: string;
  comments: CommentLike[];
  draft: DraftLike | null;
  emptyText: string;
  /** Prefix for file anchor ids, so two mounted views never share one. */
  idPrefix: string;
  onLineClick: (file: string, line: number, endLine: number, side: Side) => void;
  onDraftBody: (body: string) => void;
  onAddComment: () => void;
  onCancelDraft: () => void;
  onRefresh: () => void;
  /** Review threads keyed by reviewAnchorKey; each renders under its anchor line. */
  threads: Map<string, ReviewThreadLike[]>;
  people: ReviewPeople;
}) {
  const files = useMemo(() => diff?.files ?? [], [diff]);
  const totals = useMemo(
    () =>
      files.reduce((s, f) => ({ add: s.add + f.additions, del: s.del + f.deletions }), {
        add: 0,
        del: 0,
      }),
    [files],
  );
  // The ref is read by the window mouseup handler; state only drives the highlight.
  const pickRef = useRef<Picking | null>(null);
  const [picking, setPicking] = useState<Picking | null>(null);

  const press = (file: string, side: Side, n: number, e: React.MouseEvent) => {
    if (e.button !== 0) return;
    // Keep the browser from selecting code text while the user picks lines.
    e.preventDefault();
    if (e.shiftKey && draft && draft.file === file && draft.side === side) {
      onLineClick(file, Math.min(draft.line, n), Math.max(draft.line, n), side);
      return;
    }
    const start: Picking = { file, side, from: n, to: n };
    pickRef.current = start;
    setPicking(start);
    const up = () => {
      window.removeEventListener("mouseup", up);
      const p = pickRef.current;
      pickRef.current = null;
      setPicking(null);
      if (p) onLineClick(p.file, Math.min(p.from, p.to), Math.max(p.from, p.to), p.side);
    };
    window.addEventListener("mouseup", up);
  };

  const enter = (file: string, side: Side, n: number) => {
    const p = pickRef.current;
    if (!p || p.file !== file || p.side !== side) return;
    const next = { ...p, to: n };
    pickRef.current = next;
    setPicking(next);
  };

  return (
    <div className="diff-view">
      <div className="diff-bar">
        <span className="chat-title">{title}</span>
        <span className="diff-totals">
          <span style={{ color: "var(--ok)" }}>+{totals.add}</span>{" "}
          <span style={{ color: "var(--err)" }}>-{totals.del}</span>
        </span>
        <button onClick={onRefresh}>Refresh</button>
      </div>

      {diff && !diff.empty && files.length > 0 && (
        <div className="diff-files">
          {files.map((f, i) => {
            const st = fileStatus(f);
            return (
              <button
                key={f.path}
                className="diff-file-row"
                onClick={() => scrollToFile(idPrefix, i)}
              >
                <span className={`badge ${st}`}>{statusLetter[st]}</span>
                <span className="diff-file-name">{f.path}</span>
                <span className="diff-file-counts">
                  <span style={{ color: "var(--ok)" }}>+{f.additions}</span>{" "}
                  <span style={{ color: "var(--err)" }}>-{f.deletions}</span>
                </span>
              </button>
            );
          })}
        </div>
      )}

      {diff && diff.empty && <div className="empty">{emptyText}</div>}

      {files.map((f, fi) => {
        const { hunks } = groupHunks(f.lines ?? []);
        const ctx: LineCtx = {
          file: f.path,
          comments,
          draft,
          picking,
          threads,
          people,
          onPress: (side, n, e) => press(f.path, side, n, e),
          onEnter: (side, n) => enter(f.path, side, n),
        };
        return (
          <div className="diff-file" id={`${idPrefix}diff-file-${fi}`} key={f.path}>
            <div className="diff-head">
              <span className={`badge ${fileStatus(f)}`}>{statusLetter[fileStatus(f)]}</span>
              <span className="path">{f.path}</span>
              <span className="counts">
                <span style={{ color: "var(--ok)" }}>+{f.additions}</span>{" "}
                <span style={{ color: "var(--err)" }}>-{f.deletions}</span>
              </span>
            </div>
            {hunks.map((hunk, hi) => (
              <div className="diff-hunk" key={hi}>
                {hunk.header && (
                  <div className="diff-line hunk">
                    <span className="ln" />
                    <span className="ln" />
                    <span className="code">{hunk.header.text}</span>
                  </div>
                )}
                {renderHunkLines(hunk.lines, ctx)}
              </div>
            ))}
            {draft && draft.file === f.path && (
              <div className="comment-box">
                <div className="cb-head">
                  Commenting on {draft.file}:{lineRange(draft.line, draft.endLine)}
                </div>
                <textarea
                  rows={2}
                  autoFocus
                  placeholder="Leave a review comment for the agents…"
                  value={draft.body}
                  onChange={(e) => onDraftBody(e.target.value)}
                />
                <div className="cb-actions">
                  <button onClick={onCancelDraft}>Cancel</button>
                  <button className="primary" onClick={onAddComment}>
                    Add comment
                  </button>
                </div>
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

type LineCtx = {
  file: string;
  comments: CommentLike[];
  draft: DraftLike | null;
  picking: Picking | null;
  threads: Map<string, ReviewThreadLike[]>;
  people: ReviewPeople;
  onPress: (side: Side, n: number, e: React.MouseEvent) => void;
  onEnter: (side: Side, n: number) => void;
};

/** Renders one hunk's lines, pairing delete/add blocks for word highlighting. */
function renderHunkLines(lines: { line: DiffLineLike; index: number }[], ctx: LineCtx) {
  const out: React.ReactNode[] = [];
  let k = 0;
  while (k < lines.length) {
    const { line } = lines[k];
    if (line.kind === "delete") {
      // A delete run optionally followed by an add run: zip them pairwise so
      // changed words light up on both sides.
      const dels: typeof lines = [];
      while (k < lines.length && lines[k].line.kind === "delete") dels.push(lines[k++]);
      const adds: typeof lines = [];
      while (k < lines.length && lines[k].line.kind === "add") adds.push(lines[k++]);
      const n = Math.min(dels.length, adds.length);
      for (let p = 0; p < n; p++) {
        const { oldSpans, newSpans } = wordDiff(dels[p].line.text, adds[p].line.text);
        out.push(renderLine(dels[p], ctx, renderSpans(oldSpans, "del")));
        out.push(renderLine(adds[p], ctx, renderSpans(newSpans, "add")));
      }
      for (let p = n; p < dels.length; p++) out.push(renderLine(dels[p], ctx, null));
      for (let p = n; p < adds.length; p++) out.push(renderLine(adds[p], ctx, null));
    } else {
      out.push(renderLine(lines[k], ctx, null));
      k++;
    }
  }
  return out;
}

function renderLine(
  entry: { line: DiffLineLike; index: number },
  ctx: LineCtx,
  content: React.ReactNode,
) {
  const { line, index } = entry;
  const side = sideOf(line);
  const n = numberOf(line);
  const commentable = line.kind !== "meta" && line.kind !== "hunk" && n > 0;
  const covering = ctx.comments.filter((c) => covers(c, ctx.file, side, n));
  const drafting = ctx.draft !== null && covers(ctx.draft, ctx.file, side, n);
  const p = ctx.picking;
  const picked =
    p !== null &&
    p.file === ctx.file &&
    p.side === side &&
    n >= Math.min(p.from, p.to) &&
    n <= Math.max(p.from, p.to);
  const classes = [
    "diff-line",
    line.kind,
    covering.length > 0 && "ranged",
    drafting && "drafting",
    picked && "picking",
  ]
    .filter(Boolean)
    .join(" ");
  const anchored = commentable ? ctx.threads.get(reviewAnchorKey(ctx.file, side, n)) : undefined;
  return (
    <Fragment key={index}>
      <div className={classes}>
        <span className="ln">{line.old ?? ""}</span>
        <span className="ln">{line.new ?? ""}</span>
        <span
          className="code"
          onMouseDown={(e) => commentable && ctx.onPress(side, n, e)}
          onMouseEnter={() => commentable && ctx.onEnter(side, n)}
        >
          {content ?? (line.text || " ")}
        </span>
        {covering.some((c) => c.endLine === n) && <span className="chip">●</span>}
        {drafting && ctx.draft?.endLine === n && (
          <span className="chip draft">○</span>
        )}
      </div>
      {anchored && <ReviewThreads threads={anchored} people={ctx.people} />}
    </Fragment>
  );
}
