import { memo, useEffect, useRef, useState } from "react";
import { liveLogStore, useStreamingLine, type LiveLine, type ReplySource } from "../lib/liveLog";
import { useStoreField } from "../lib/store";
import { Markdown } from "./Markdown";
import { ToolCard } from "./ToolCard";
import { agentStopped } from "../lib/toolCalls";

// The log box is much shorter than the transcript, so its pill shows sooner.
const LOGS_TOP_SHOW = 80;
// Output this close to the end counts as "at the bottom" for the log pills.
const LOGS_BOTTOM_SHOW = 48;

/** How many of the newest lines the activity box renders. */
const SHOWN = 60;

type Agents = Record<string, { status?: string } | undefined> | undefined;

/** One log line. Memoized on the line object: only the line a chunk grew gets a
 * new object, so only its markdown is parsed again. */
const LiveLogEntry = memo(function LiveLogEntry({ line, agents }: { line: LiveLine; agents: Agents }) {
  if (line.tool) return <ToolCard line={line} stopped={agentStopped(agents?.[line.agentId]?.status)} />;
  return line.kind === "text" || line.kind === "reasoning" ? (
    <div className={`act-md ${line.kind}`}>
      <Markdown text={line.text} />
    </div>
  ) : (
    <div className={`log-line ${line.kind}`}>{line.text}</div>
  );
});

/**
 * The reply row inside the current turn, where the final message lands. It
 * uses the message row's markup, so when the final message replaces it the
 * transcript keeps its place. `onGrow` lets the transcript follow it.
 */
export const StreamingReply = memo(function StreamingReply({
  source,
  onGrow,
}: {
  source: ReplySource;
  onGrow: () => void;
}) {
  const line = useStreamingLine(source);
  const text = line?.text;
  useEffect(() => {
    if (text !== undefined) onGrow();
  }, [text, onGrow]);
  if (!line) return null;
  return (
    <div className="msg streaming">
      <div className="who">
        <span className="role">{source.author}</span>
      </div>
      <div className="body">
        <Markdown text={line.text} />
      </div>
    </div>
  );
});

/**
 * The body of the "Working" box: the open thread's streamed output. It reads
 * liveLogStore itself, so a streamed chunk re-renders this log and not App.
 * `onLines` runs after the line count changes, so the transcript can follow.
 * `reply` is the reply streaming into the transcript, which is not repeated here.
 */
export const LiveLog = memo(function LiveLog({
  onLines,
  agents,
  reply,
}: {
  onLines: () => void;
  reply?: ReplySource | null;
  /** The open thread's agents, so a card still marked running can show "stopped". */
  agents?: Record<string, { status?: string } | undefined>;
}) {
  const live = useStoreField(liveLogStore, "lines");
  const replyId = useStreamingLine(reply)?.id;
  // Live log follows the newest output, unless the user scrolled up to read.
  // Opening the box jumps to the newest output.
  const actBodyRef = useRef<HTMLDivElement | null>(null);
  const opened = useRef(true);
  // Only the edges are stored, so scrolling re-renders when a jump pill should
  // mount or unmount — not on every pixel of the log.
  const [logsAtTop, setLogsAtTop] = useState(false);
  const [logsAtBottom, setLogsAtBottom] = useState(true);
  useEffect(() => {
    const opening = opened.current;
    opened.current = false;
    const el = actBodyRef.current;
    if (!el) return;
    if (opening || el.scrollHeight - el.scrollTop - el.clientHeight < LOGS_BOTTOM_SHOW) {
      el.scrollTop = el.scrollHeight;
    }
    setLogsAtTop(el.scrollTop > LOGS_TOP_SHOW);
    setLogsAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < LOGS_BOTTOM_SHOW);
  }, [live]);
  useEffect(() => {
    onLines();
  }, [live.length, onLines]);

  const logsScrollToTop = () => {
    const el = actBodyRef.current;
    if (el) el.scrollTo({ top: 0, behavior: "smooth" });
  };

  const logsScrollToBottom = () => {
    const el = actBodyRef.current;
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
  };

  return (
    <div
      className="act-body"
      ref={actBodyRef}
      onScroll={(e) => {
        const el = e.currentTarget;
        setLogsAtTop(el.scrollTop > LOGS_TOP_SHOW);
        setLogsAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < LOGS_BOTTOM_SHOW);
      }}
    >
      {/* Zero-height sticky anchor: the jump-to-top pill
          overlays the log instead of pushing lines down. */}
      <div className="scroll-top-anchor">
        {logsAtTop && (
          <button
            type="button"
            className="scroll-top"
            onClick={logsScrollToTop}
            aria-label="Jump to start of logs"
            title="Jump to start of logs"
          >
            ↑
          </button>
        )}
      </div>
      {live.length === 0 && <div className="log-line raw">Waiting for output…</div>}
      {live.slice(-SHOWN).map((l) =>
        l.id === replyId ? null : (
          <LiveLogEntry key={l.tool ? `tool:${l.agentId}:${l.tool.id}` : l.id} line={l} agents={agents} />
        ),
      )}
      {!logsAtBottom && (
        <button
          type="button"
          className="scroll-bottom"
          onClick={logsScrollToBottom}
          aria-label="Jump to latest log output"
          title="Jump to latest log output"
        >
          ↓
        </button>
      )}
    </div>
  );
});
