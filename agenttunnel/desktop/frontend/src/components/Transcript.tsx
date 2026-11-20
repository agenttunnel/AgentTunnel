import { createContext, memo, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import type { ImageRef } from "../lib/rpc";
import { LIVE_LIMIT, liveLogStore, type LiveLine, type ReplySource } from "../lib/liveLog";
import { parseToolDiff } from "../lib/toolCalls";
import { Markdown } from "./Markdown";
import { StreamingReply } from "./LiveLog";
import { ThreadImage } from "./ThreadImage";

export type Message = {
  id: string;
  author: string;
  text: string;
  at: string;
  images?: ImageRef[];
  /** Set on a steering message once a pass has consumed it. */
  consumed?: boolean;
};

/** A shell block ends with its status line: `exit N`, `stopped`, or `timed out after …`. */
const SHELL_FAILED = /\n(?:exit [1-9]\d*|stopped|timed out after .+?)(?: · .*)?$/;
function isFailedBlock(m: Message): boolean {
  return m.author === "shell" && SHELL_FAILED.test(m.text.trimEnd());
}

/** The lead's own reply: the turn's answer, set as prose rather than a log row. */
function isAnswer(m: Message): boolean {
  return m.author === "orchestrator" || m.author === "solo";
}

/** Turns a flat message list into turns: each user message opens one block. */
export function groupTurns(messages: Message[]): Message[][] {
  const turns: Message[][] = [];
  for (const m of messages) {
    if (m.author === "user" || turns.length === 0) turns.push([m]);
    else turns[turns.length - 1].push(m);
  }
  return turns;
}

/** Per-message actions App supplies through a context, so a memoized message
 * row never re-renders because a handler's identity changed. `copyText` writes
 * to the clipboard and resolves whether the write landed; `forkFromMessage`
 * branches the thread off one message; `scrollToMessage` scrolls to the next
 * (1) or previous (-1) message; `editPrompt` puts a sent prompt back in the
 * composer; `openReview` opens the thread's diff. */
export type MessageActions = {
  copyText: (text: string) => Promise<boolean>;
  forkFromMessage: (messageId: string) => void;
  scrollToMessage: (messageId: string, dir: -1 | 1) => void;
  editPrompt: (text: string) => void;
  openReview: () => void;
};
export const MessageActionsContext = createContext<MessageActions | null>(null);

/** Connection details a transcript message needs to fetch its images over
 * HTTP: the daemon's base URL and token plus the thread the message sits in.
 * Provided through a context so memoized message rows never re-render from a
 * handler's identity; null renders no image section. */
export type MessageImageConn = { httpUrl: string; token: string; threadId: string };
export const MessageImageContext = createContext<MessageImageConn | null>(null);

function Icon({ children }: { children: React.ReactNode }) {
  return (
    <svg
      viewBox="0 0 24 24"
      width="12"
      height="12"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {children}
    </svg>
  );
}

/** The git-branch glyph the fork button carries. */
const BranchIcon = () => (
  <Icon>
    <line x1="6" y1="3" x2="6" y2="15" />
    <circle cx="18" cy="6" r="3" />
    <circle cx="6" cy="18" r="3" />
    <path d="M18 9a9 9 0 0 1-9 9" />
  </Icon>
);
const ChevronIcon = ({ up }: { up: boolean }) => (
  <Icon>{up ? <polyline points="6 15 12 9 18 15" /> : <polyline points="6 9 12 15 18 9" />}</Icon>
);
const CopyIcon = () => (
  <Icon>
    <rect x="9" y="9" width="11" height="11" rx="2" />
    <path d="M5 15V5a2 2 0 0 1 2-2h10" />
  </Icon>
);
const EditIcon = () => (
  <Icon>
    <path d="M4 20h4L19 9l-4-4L4 16z" />
  </Icon>
);

const ROLE_LABEL: Record<string, string> = {
  user: "You",
  system: "System",
  shell: "Shell",
  subthread: "Subthread",
  status: "Status",
  solo: "Solo",
};

/** Long non-answer blocks (shell output, pasted prompts, status dumps) start
 * clamped to this many lines; the answer itself is never clamped. */
const CLAMP_LINES = 24;
const CLAMP_CHARS = 2400;

function clockTime(at: string): string {
  const d = new Date(at);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

/** useCopy holds the copy button's click result for a moment, so a click
 * always has a visible outcome. */
export function useCopyState(copy: (text: string) => Promise<boolean>) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  const run = async (text: string) => {
    const ok = await copy(text);
    window.clearTimeout(timer.current);
    setState(ok ? "copied" : "failed");
    timer.current = window.setTimeout(() => setState("idle"), ok ? 1400 : 2500);
  };
  return [state, run] as const;
}

/** One chat message. Memoized on its message plus its nav edge flags:
 * refreshing a busy thread re-renders only the blocks whose own message (or
 * first/last position) changed, and the action handlers arrive via
 * MessageActionsContext so their identity is free. */
export const MessageBlock = memo(function MessageBlock({
  m,
  hasPrev,
  hasNext,
}: {
  m: Message;
  hasPrev: boolean;
  hasNext: boolean;
}) {
  const actions = useContext(MessageActionsContext);
  const imageConn = useContext(MessageImageContext);
  const [copyState, copy] = useCopyState((t) => actions?.copyText(t) ?? Promise.resolve(false));
  const answer = isAnswer(m);
  const lineCount = useMemo(() => (answer ? 0 : m.text.split("\n").length), [answer, m.text]);
  const long = !answer && (lineCount > CLAMP_LINES || m.text.length > CLAMP_CHARS);
  const [showAll, setShowAll] = useState(false);
  const clamped = long && !showAll;
  const kind =
    m.author === "user" || m.author === "steering" || m.author === "shell" || m.author === "status"
      ? m.author
      : answer
        ? "answer"
        : "";
  const role =
    m.author === "steering"
      ? m.consumed
        ? "Steering"
        : "Steering · queued"
      : (ROLE_LABEL[m.author] ?? "Orchestrator");
  return (
    <div
      data-mid={m.id}
      className={`msg ${kind}${m.author === "steering" && !m.consumed ? " pending" : ""}${isFailedBlock(m) ? " failed" : ""}`}
    >
      <div className="who">
        <span className="role">{role}</span>
        <time className="msg-time" dateTime={m.at}>
          {clockTime(m.at)}
        </time>
      </div>
      <div className="msg-actions" role="toolbar" aria-label="Message actions">
        <button
          type="button"
          className={`msg-icon-btn msg-copy-btn${copyState === "idle" ? "" : ` ${copyState}`}`}
          onClick={() => void copy(m.text)}
          title={copyState === "failed" ? "Could not copy" : "Copy"}
          aria-label="Copy this message"
        >
          <CopyIcon />
          <span className="msg-btn-label" aria-live="polite">
            {copyState === "copied" ? "Copied" : copyState === "failed" ? "Copy failed" : ""}
          </span>
        </button>
        {m.author === "user" && (
          <button
            type="button"
            className="msg-icon-btn"
            onClick={() => actions?.editPrompt(m.text)}
            title="Edit and resend — puts this prompt back in the composer"
            aria-label="Edit and resend this prompt"
          >
            <EditIcon />
          </button>
        )}
        <button
          type="button"
          className="msg-icon-btn"
          onClick={() => actions?.forkFromMessage(m.id)}
          title="Branch from here — new thread with the transcript up to this message"
          aria-label="Branch from here"
        >
          <BranchIcon />
        </button>
        {hasPrev && (
          <button
            type="button"
            className="msg-icon-btn"
            onClick={() => actions?.scrollToMessage(m.id, -1)}
            aria-label="Previous message"
            title="Previous message"
          >
            <ChevronIcon up />
          </button>
        )}
        {hasNext && (
          <button
            type="button"
            className="msg-icon-btn"
            onClick={() => actions?.scrollToMessage(m.id, 1)}
            aria-label="Next message"
            title="Next message"
          >
            <ChevronIcon up={false} />
          </button>
        )}
      </div>
      <div className={`body${clamped ? " clamped" : ""}`}>
        <Markdown text={m.text} />
        {!!m.images?.length && imageConn && (
          <div className="thread-images">
            {m.images.map((img) => (
              <ThreadImage
                key={img.id}
                httpUrl={imageConn.httpUrl}
                token={imageConn.token}
                threadId={imageConn.threadId}
                image={img}
              />
            ))}
          </div>
        )}
      </div>
      {long && (
        <button
          type="button"
          className="msg-more"
          onClick={() => setShowAll(!showAll)}
          aria-expanded={showAll}
        >
          {showAll ? "Show less" : `Show all ${lineCount} lines`}
        </button>
      )}
    </div>
  );
});

/** One turn: the user's prompt heads it, the team's response stacks under it,
 *  the lead's reply closes it. The response can be collapsed to a single
 *  summary row; collapsed state is per turn (keyed by the prompt message's
 *  instance), so streamed messages arriving in a collapsed turn never expand it. */
export const TurnBlock = memo(function TurnBlock({
  turn,
  index,
  total,
  streaming,
  onStreamGrow,
  changes,
  outcome,
}: {
  turn: Message[];
  index: Map<string, number>;
  total: number;
  /** The reply being written into this turn; only the last turn gets one. */
  streaming?: ReplySource | null;
  onStreamGrow: () => void;
  /** Show what this turn changed: the last turn, once it has finished. */
  changes?: boolean;
  /** How a turn that ended without a reply ended ("Stopped", "Failed — …"). */
  outcome?: string;
}) {
  const position = (m: Message) => {
    const i = index.get(m.id) ?? 0;
    return { hasPrev: i > 0, hasNext: i < total - 1 };
  };
  const [expanded, setExpanded] = useState(true);
  const summary = changes && turn[0].author === "user" ? <TurnChanges since={turn[0].at} /> : null;
  // An opening block with no prompt (transcript that starts mid-run) has
  // nothing to toggle: it renders flat.
  if (turn[0].author !== "user") {
    return (
      <section className="turn">
        {turn.map((m) => (
          <MessageBlock key={m.id} m={m} {...position(m)} />
        ))}
        {streaming && <StreamingReply source={streaming} onGrow={onStreamGrow} />}
      </section>
    );
  }
  const response = turn.slice(1);
  return (
    <section className="turn">
      <MessageBlock m={turn[0]} {...position(turn[0])} />
      {response.length > 0 &&
        (expanded ? (
          <button
            type="button"
            className="turn-toggle"
            onClick={() => setExpanded(false)}
            aria-expanded="true"
            title="Hide response"
          >
            ▾ Response
          </button>
        ) : (
          <button
            type="button"
            className="turn-hidden"
            onClick={() => setExpanded(true)}
            aria-expanded="false"
            title="Show response"
          >
            ▸ Response hidden — {response.length} {response.length === 1 ? "message" : "messages"}
          </button>
        ))}
      {expanded && response.map((m) => <MessageBlock key={m.id} m={m} {...position(m)} />)}
      {expanded && streaming && <StreamingReply source={streaming} onGrow={onStreamGrow} />}
      {outcome && (
        <div className="turn-outcome" role="status">
          {outcome}
        </div>
      )}
      {expanded && summary}
    </section>
  );
});

type FileChange = { path: string; add: number; del: number };

/** Files the turn's edit tool calls touched, with their +/- counts, from the
 * live log. Null when the log no longer reaches back to the turn's start, so a
 * trimmed log never shows a partial list as the whole turn. */
export function turnChanges(lines: LiveLine[], since: string): FileChange[] | null {
  const start = Date.parse(since);
  if (Number.isNaN(start)) return null;
  const first = lines[0];
  const firstAt = first?.at ? Date.parse(first.at) : first?.startedAt;
  if (lines.length >= LIVE_LIMIT && firstAt !== undefined && firstAt > start) return null;
  const byPath = new Map<string, FileChange>();
  for (const l of lines) {
    const t = l.tool;
    if (!t?.diff || t.status === "failed") continue;
    const at = l.at ? Date.parse(l.at) : l.startedAt;
    if (at === undefined || at < start) continue;
    const path = t.path || t.name || "file";
    const entry = byPath.get(path) ?? { path, add: 0, del: 0 };
    for (const d of parseToolDiff(t.diff)) {
      if (d.kind === "add") entry.add++;
      else if (d.kind === "delete") entry.del++;
    }
    byPath.set(path, entry);
  }
  return [...byPath.values()];
}

function shortPath(path: string): string {
  const parts = path.split("/");
  return parts.length > 2 ? `…/${parts.slice(-2).join("/")}` : path;
}

/** "Changes this turn": one compact row per touched file, linking to Review.
 * Rendered for a finished turn only, so streamed chunks never reach it. */
function TurnChanges({ since }: { since: string }) {
  const lines = useSyncExternalStore(liveLogStore.subscribe, () => liveLogStore.get().lines);
  const changes = useMemo(() => turnChanges(lines, since), [lines, since]);
  const actions = useContext(MessageActionsContext);
  if (!changes || changes.length === 0) return null;
  const add = changes.reduce((n, c) => n + c.add, 0);
  const del = changes.reduce((n, c) => n + c.del, 0);
  return (
    <div className="turn-changes">
      <div className="tc-head">
        <span className="tc-title">
          {changes.length} {changes.length === 1 ? "file" : "files"} changed
        </span>
        <span className="tc-add">+{add}</span>
        <span className="tc-del">−{del}</span>
        <button type="button" className="tc-review" onClick={() => actions?.openReview()}>
          Review changes →
        </button>
      </div>
      <ul className="tc-files">
        {changes.slice(0, 8).map((c) => (
          <li key={c.path} title={c.path}>
            <span className="tc-path">{shortPath(c.path)}</span>
            <span className="tc-add">+{c.add}</span>
            <span className="tc-del">−{c.del}</span>
          </li>
        ))}
        {changes.length > 8 && <li className="tc-more">and {changes.length - 8} more</li>}
      </ul>
    </div>
  );
}
