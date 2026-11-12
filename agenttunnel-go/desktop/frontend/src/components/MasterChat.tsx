import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import type { Rpc } from "../lib/rpc";
import {
  sendMaster,
  type MasterActionType,
  type MasterMessage,
} from "../lib/master";
import type { Project } from "../App";
import { Markdown } from "./Markdown";

/** One stored line; a reply that failed validation carries the reason inline. */
type Entry = MasterMessage & { error?: string };

const STORAGE_KEY = "at.masterChat.v1";
/** Oldest lines are dropped once the transcript grows past this. */
const MAX_ENTRIES = 40;
/** Past-tense label for each action that ran; start_thread opens its thread instead. */
const MASTER_DONE: Record<MasterActionType, string> = {
  start_thread: "started thread",
  send_message: "sent message",
  stop_thread: "stopped run",
  rename_thread: "renamed thread",
  set_peer_mailbox: "updated peer mailbox",
  delete_thread: "deleted thread",
  read_thread: "read thread",
};

function isEntry(value: unknown): value is Entry {
  if (!value || typeof value !== "object") return false;
  const entry = value as Record<string, unknown>;
  return (
    (entry.role === "user" || entry.role === "assistant") &&
    typeof entry.text === "string" &&
    (entry.error === undefined || typeof entry.error === "string")
  );
}

/** Restores the conversation; a corrupt or private-mode store starts empty. */
function loadTranscript(): Entry[] {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed.filter(isEntry).slice(-MAX_ENTRIES);
  } catch {
    return [];
  }
}

function saveTranscript(entries: Entry[]): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(entries));
  } catch {
    /* private mode: stay in memory */
  }
}

/**
 * MasterChat is the conversation with the master orchestrator, on its own page: the
 * user describes a task in natural language, and the orchestrator either asks
 * questions or starts a thread in a project and hands it straight to the app.
 */
export function MasterChat({
  rpc,
  projects,
  onOpenThread,
}: {
  rpc: Rpc | null;
  projects: Project[];
  onOpenThread: (projectId: string, threadId: string) => void;
}) {
  const [entries, setEntries] = useState<Entry[]>(loadTranscript);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);
  const entriesRef = useRef<Entry[]>(entries);
  const busyRef = useRef(false);
  const bodyRef = useRef<HTMLDivElement | null>(null);
  const taRef = useRef<HTMLTextAreaElement | null>(null);

  /** Writes through the ref so the persisted transcript is current even when a
   *  reply navigates away from this screen before React can flush an effect. */
  const update = (fn: (prev: Entry[]) => Entry[]) => {
    const next = fn(entriesRef.current).slice(-MAX_ENTRIES);
    entriesRef.current = next;
    setEntries(next);
    saveTranscript(next);
  };

  useEffect(() => {
    const el = bodyRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [entries, busy, sendError]);

  useLayoutEffect(() => {
    const el = taRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 160)}px`;
  }, [input]);

  const send = async () => {
    const text = input.trim();
    if (!rpc || !text || busyRef.current) return;
    // History is what was on screen before this message, oldest first.
    const history: MasterMessage[] = entriesRef.current
      .filter((e) => e.text !== "")
      .map((e) => ({ role: e.role, text: e.text }));
    busyRef.current = true;
    setBusy(true);
    setSendError(null);
    setInput("");
    update((prev) => [...prev, { role: "user", text }]);
    try {
      const result = await sendMaster(rpc, text, history);
      update((prev) => [
        ...prev,
        {
          role: "assistant",
          text: result.reply,
          ...(result.error ? { error: result.error } : {}),
        },
      ]);
      // Each action that ran without a thread to open gets one confirmation line.
      const done = (result.results ?? [])
        .filter((r) => r.ok && r.type !== "start_thread")
        .map((r) => MASTER_DONE[r.type]);
      if (done.length > 0) {
        update((prev) => [
          ...prev,
          { role: "assistant", text: `Done: ${done.join("; ")}.` },
        ]);
      }
      // The daemon created the thread and started its first turn in the
      // background; say which one and open it.
      if (result.threadId && result.projectId) {
        const projectName =
          projects.find((p) => p.id === result.projectId)?.name ?? result.projectId;
        const title =
          result.actions?.find((a) => a.type === "start_thread")?.title ?? "New thread";
        update((prev) => [
          ...prev,
          { role: "assistant", text: `Started “${title}” in ${projectName}.` },
        ]);
        onOpenThread(result.projectId, result.threadId);
      }
    } catch (e) {
      // Transport failures stay out of the transcript so the composer remains usable.
      setSendError(e instanceof Error ? e.message : String(e));
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  };

  const newChat = () => {
    if (busyRef.current) return;
    entriesRef.current = [];
    setEntries([]);
    setSendError(null);
    setInput("");
    try {
      window.localStorage.removeItem(STORAGE_KEY);
    } catch {
      /* private mode: nothing stored */
    }
  };

  const onKeyDown = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing || e.keyCode === 229) return;
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void send();
    }
  };

  const empty = entries.length === 0 && !sendError;

  return (
    <section className={`master-chat${empty ? " is-empty" : ""}`} aria-label="Orchestrator">
      <header className="master-head">
        <span className="master-title">Orchestrator</span>
        {busy && (
          <span className="master-busy">
            <span className="master-busy-dot" aria-hidden="true" />
            Thinking…
          </span>
        )}
        <button type="button" className="master-new" onClick={newChat} disabled={busy}>
          New chat
        </button>
      </header>
      <div className="master-body" ref={bodyRef} aria-live="polite">
        {empty && (
          <p className="master-empty">
            Describe a task in plain language and name the project. The orchestrator will ask
            questions, or start the thread with a model it picks.
          </p>
        )}
        {entries.map((entry, i) => (
          <div
            key={i}
            className={`master-msg ${entry.role === "user" ? "user" : "assistant"}`}
          >
            <div className="master-who">
              {entry.role === "user" ? "You" : "Orchestrator"}
            </div>
            {entry.text !== "" &&
              (entry.role === "user" ? (
                <div className="master-text">{entry.text}</div>
              ) : (
                <div className="master-text">
                  <Markdown text={entry.text} />
                </div>
              ))}
            {entry.error && <div className="master-error">{entry.error}</div>}
          </div>
        ))}
        {sendError && <p className="master-send-err">{sendError}</p>}
      </div>
      <div className="master-composer">
        <textarea
          ref={taRef}
          rows={1}
          value={input}
          disabled={!rpc || busy}
          placeholder="Describe what to build, and which project…"
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={onKeyDown}
        />
        <button
          type="button"
          className="primary"
          disabled={!rpc || busy || !input.trim()}
          onClick={() => void send()}
        >
          Send
        </button>
      </div>
    </section>
  );
}
