import { memo, useEffect, useRef, useState } from "react";
import { liveLogStore } from "../lib/liveLog";
import { useStoreField } from "../lib/store";
import { Markdown } from "./Markdown";

// The log box is much shorter than the transcript, so its pill shows sooner.
const LOGS_TOP_SHOW = 80;
// Output this close to the end counts as "at the bottom" for the log pills.
const LOGS_BOTTOM_SHOW = 48;

/**
 * The body of the "Working" box: the open thread's streamed output. It reads
 * liveLogStore itself, so a streamed chunk re-renders this log and not App.
 * `onLines` runs after the line count changes, so the transcript can follow.
 */
export const LiveLog = memo(function LiveLog({ onLines }: { onLines: () => void }) {
  const live = useStoreField(liveLogStore, "lines");
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
      {live.slice(-60).map((l, i) =>
        l.kind === "text" || l.kind === "reasoning" ? (
          <div key={i} className={`act-md ${l.kind}`}>
            <Markdown text={l.text} />
          </div>
        ) : (
          <div key={i} className={`log-line ${l.kind}`}>
            {l.text}
          </div>
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
