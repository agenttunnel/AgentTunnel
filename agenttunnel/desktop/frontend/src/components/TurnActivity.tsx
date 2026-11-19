import { memo, useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { liveLogStore, streamingReply, type LiveLine, type ReplySource } from "../lib/liveLog";
import { LiveLog } from "./LiveLog";

type Agents = Record<string, { status?: string } | undefined> | undefined;

/** What the lead is doing now, read from its newest log line this turn. */
type LeadStatus = { kind: "starting" | "thinking" | "writing" | "command" | "edit" | "tool"; detail: string };

const EDIT_TOOL = /edit|write|patch|apply/i;

export function leadStatus(lines: LiveLine[], leads: ReadonlySet<string>, since: number): LeadStatus {
  for (let i = lines.length - 1; i >= 0; i--) {
    const l = lines[i];
    const at = l.at ? Date.parse(l.at) : l.startedAt;
    if (at !== undefined && at < since) break;
    if (!leads.has(l.agentId)) continue;
    const t = l.tool;
    if (t) {
      // A finished call means the model is deciding what comes next.
      if (t.status !== "running") return { kind: "thinking", detail: "" };
      if (t.command) return { kind: "command", detail: t.command };
      if (t.diff || EDIT_TOOL.test(t.name ?? "")) return { kind: "edit", detail: t.path || t.name || "" };
      return { kind: "tool", detail: t.name || l.text.split("\n")[0] };
    }
    if (l.kind === "reasoning") return { kind: "thinking", detail: "" };
    if (l.kind === "text" && l.text.trim()) return { kind: "writing", detail: "" };
  }
  return { kind: "starting", detail: "" };
}

const KIND_LABEL: Record<LeadStatus["kind"], string> = {
  starting: "Starting",
  thinking: "Thinking",
  writing: "Writing the reply",
  command: "Running a command",
  edit: "Editing",
  tool: "Using a tool",
};

function clip(s: string, n: number): string {
  const one = s.replace(/\s+/g, " ").trim();
  return one.length > n ? `${one.slice(0, n - 1)}…` : one;
}

/** Ticks once a second; only this node re-renders. */
function Elapsed({ since }: { since: number }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const timer = window.setInterval(() => tick((n) => n + 1), 1000);
    return () => window.clearInterval(timer);
  }, []);
  if (Number.isNaN(since)) return null;
  const secs = Math.max(0, Math.round((Date.now() - since) / 1000));
  const label = secs < 60 ? `${secs}s` : `${Math.floor(secs / 60)}m ${String(secs % 60).padStart(2, "0")}s`;
  return (
    <span className="act-elapsed" aria-hidden="true">
      {label}
    </span>
  );
}

/** Announces the coarse status to screen readers, at most every few seconds,
 * so a burst of tool calls is one announcement, not a stream. */
function useThrottledAnnouncement(text: string, everyMs = 4000): string {
  const [said, setSaid] = useState(text);
  const last = useRef(0);
  useEffect(() => {
    const wait = Math.max(0, last.current + everyMs - Date.now());
    const timer = window.setTimeout(() => {
      last.current = Date.now();
      setSaid(text);
    }, wait);
    return () => window.clearTimeout(timer);
  }, [text, everyMs]);
  return said;
}

export type TurnActivityProps = {
  /** Running lead agent ids joined by \u001f: whose log lines are "the lead". */
  leads: string;
  /** Turn start (ms): lines before it belong to an earlier turn. */
  since: number;
  /** Running agents that are not leads. */
  helpers: number;
  /** Pending questions waiting on the user. */
  waiting: number;
  logsOpen: boolean;
  onToggleLogs: () => void;
  onStop: () => void;
  onJumpToQuestion: () => void;
  onLines: () => void;
  /** The open thread, so the log can narrow to the agent selected in the tree. */
  threadId?: string | null;
  agents: Agents;
  reply: ReplySource | null;
};

/**
 * The running turn's status line and its Working log. The line says what the
 * lead is doing now, with an elapsed timer and one indicator; it unmounts the
 * moment no agent runs. It reads the live log through narrow selectors, so a
 * streamed chunk re-renders it only when the status itself changes. The log
 * starts collapsed once the reply streams into the transcript, unless the
 * user opens it for this turn. Mount it keyed by turn so that choice resets.
 */
export const TurnActivity = memo(function TurnActivity({
  leads,
  since,
  helpers,
  waiting,
  logsOpen,
  onToggleLogs,
  onStop,
  onJumpToQuestion,
  onLines,
  threadId,
  agents,
  reply,
}: TurnActivityProps) {
  const leadSet = useRef<{ key: string; set: Set<string> }>({ key: "", set: new Set() });
  if (leadSet.current.key !== leads) leadSet.current = { key: leads, set: new Set(leads ? leads.split("\u001f") : []) };
  const set = leadSet.current.set;

  const statusKey = useSyncExternalStore(
    liveLogStore.subscribe,
    useCallback(() => {
      const s = leadStatus(liveLogStore.get().lines, set, since);
      return `${s.kind}\u001f${s.detail}`;
    }, [set, since]),
  );
  const [kind, detail] = statusKey.split("\u001f") as [LeadStatus["kind"], string];
  const streaming = useSyncExternalStore(
    liveLogStore.subscribe,
    useCallback(
      () => (reply ? streamingReply(liveLogStore.get().lines, reply.leads, reply.since) !== null : false),
      [reply],
    ),
  );
  const [manual, setManual] = useState<boolean | null>(null);
  const open = manual ?? (logsOpen && !streaming);
  const toggle = () => {
    setManual(!open);
    if (!open !== logsOpen) onToggleLogs();
  };

  const label =
    waiting > 0
      ? `Waiting on you — ${waiting} ${waiting === 1 ? "question" : "questions"}`
      : KIND_LABEL[kind];
  const announced = useThrottledAnnouncement(label);

  return (
    <div className={`activity agents${open ? "" : " collapsed"}${waiting > 0 ? " waiting" : ""}`}>
      <div className="act-head">
        <span className="act-led" aria-hidden="true" />
        <span className="act-label">
          <span className="act-kind">{label}</span>
          {waiting === 0 && detail && (
            <code className="act-detail" title={detail}>
              {clip(detail, 80)}
            </code>
          )}
          {helpers > 0 && (
            <span className="act-helpers">
              · {helpers} {helpers === 1 ? "agent" : "agents"} working
            </span>
          )}
        </span>
        <span className="visually-hidden" role="status" aria-live="polite">
          {announced}
        </span>
        {waiting > 0 && (
          <button type="button" className="act-answer" onClick={onJumpToQuestion}>
            Answer
          </button>
        )}
        <Elapsed since={since} />
        <button
          type="button"
          className="act-toggle"
          onClick={toggle}
          aria-expanded={open}
          title={open ? "Hide the working log" : "Show the working log"}
        >
          {open ? "▾" : "▸"} Log
        </button>
        <button
          type="button"
          className="stop-btn"
          onClick={onStop}
          title="Stop this turn — running agents are cancelled"
        >
          Stop
        </button>
      </div>
      {open && <LiveLog onLines={onLines} agents={agents} reply={reply} threadId={threadId} />}
    </div>
  );
});
