import { useMemo } from "react";
import { wordDiff, type TokSpan } from "../lib/worddiff";

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
export type CommentLike = { id: string; file: string; line: number; body: string };
export type DraftLike = { file: string; line: number; body: string };

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

const lineNumber = (l: DiffLineLike) => l.new ?? l.old ?? 0;

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
 * and word-level change highlighting. Clicking a code line opens the comment
 * draft for review comments back to the agents.
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
}: {
  diff: DiffLike | null;
  title: string;
  comments: CommentLike[];
  draft: DraftLike | null;
  emptyText: string;
  /** Prefix for file anchor ids, so two mounted views never share one. */
  idPrefix: string;
  onLineClick: (file: string, line: number) => void;
  onDraftBody: (body: string) => void;
  onAddComment: () => void;
  onCancelDraft: () => void;
  onRefresh: () => void;
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
                {renderHunkLines(hunk.lines, f.path, comments, draft, onLineClick)}
              </div>
            ))}
            {draft && draft.file === f.path && (
              <div className="comment-box">
                <div className="cb-head">
                  Commenting on {draft.file}:{draft.line}
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

/** Renders one hunk's lines, pairing delete/add blocks for word highlighting. */
function renderHunkLines(
  lines: { line: DiffLineLike; index: number }[],
  file: string,
  comments: CommentLike[],
  draft: DraftLike | null,
  onLineClick: (file: string, line: number) => void,
) {
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
        out.push(
          renderLine(dels[p], file, comments, draft, onLineClick, renderSpans(oldSpans, "del")),
        );
        out.push(
          renderLine(adds[p], file, comments, draft, onLineClick, renderSpans(newSpans, "add")),
        );
      }
      for (let p = n; p < dels.length; p++)
        out.push(renderLine(dels[p], file, comments, draft, onLineClick, null));
      for (let p = n; p < adds.length; p++)
        out.push(renderLine(adds[p], file, comments, draft, onLineClick, null));
    } else {
      out.push(renderLine(lines[k], file, comments, draft, onLineClick, null));
      k++;
    }
  }
  return out;
}

function renderLine(
  entry: { line: DiffLineLike; index: number },
  file: string,
  comments: CommentLike[],
  draft: DraftLike | null,
  onLineClick: (file: string, line: number) => void,
  content: React.ReactNode,
) {
  const { line, index } = entry;
  const n = lineNumber(line);
  const commented = comments.some((c) => c.file === file && c.line === n);
  const commentable = line.kind !== "meta" && line.kind !== "hunk";
  return (
    <div key={index} className={`diff-line ${line.kind}`}>
      <span className="ln">{line.old ?? ""}</span>
      <span className="ln">{line.new ?? ""}</span>
      <span className="code" onClick={() => commentable && n > 0 && onLineClick(file, n)}>
        {content ?? (line.text || " ")}
      </span>
      {commented && <span className="chip">●</span>}
      {draft && draft.file === file && draft.line === n && <span className="chip draft">○</span>}
    </div>
  );
}
