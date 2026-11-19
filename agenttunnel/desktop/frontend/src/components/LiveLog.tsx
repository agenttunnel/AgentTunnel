import { memo } from "react";
import { Markdown } from "./Markdown";
import type { LiveLine } from "../lib/liveLog";

/** How many of the newest lines the activity box renders. */
const SHOWN = 60;

/** One log line. Memoized on the line object: only the line a chunk grew gets a
 * new object, so only its markdown is parsed again. */
const LiveLogEntry = memo(function LiveLogEntry({ line }: { line: LiveLine }) {
  return line.kind === "text" || line.kind === "reasoning" ? (
    <div className={`act-md ${line.kind}`}>
      <Markdown text={line.text} />
    </div>
  ) : (
    <div className={`log-line ${line.kind}`}>{line.text}</div>
  );
});

/** The newest lines of the activity log, keyed by stable line id. `skip` is a
 * line shown elsewhere (the reply streaming into the transcript). */
export function LiveLogLines({ lines, skip }: { lines: LiveLine[]; skip?: number }) {
  if (lines.length === 0) return <div className="log-line raw">Waiting for output…</div>;
  return (
    <>
      {lines.slice(-SHOWN).map((l) => (l.id === skip ? null : <LiveLogEntry key={l.id} line={l} />))}
    </>
  );
}

/** The lead's reply while it is being written. */
export type StreamingText = { text: string; author: string };

/**
 * The reply row inside the current turn, where the final message lands. It
 * uses the message row's markup, so when the final message replaces it the
 * transcript keeps its place.
 */
export const StreamingReply = memo(function StreamingReply({ text, author }: StreamingText) {
  return (
    <div className="msg streaming">
      <div className="who">
        <span className="role">{author}</span>
      </div>
      <div className="body">
        <Markdown text={text} />
      </div>
    </div>
  );
});
