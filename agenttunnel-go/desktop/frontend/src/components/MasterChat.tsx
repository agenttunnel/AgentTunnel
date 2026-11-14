import {
  memo,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import type { Rpc } from "../lib/rpc";
import {
  deleteMasterThread,
  getMasterReporter,
  getMasterRuntime,
  listMasterThreads,
  newMasterThread,
  openMasterThread,
  sendMaster,
  setMasterReporter,
  setMasterRuntime,
  type MasterMessage,
  type MasterRuntime,
  type MasterThreadSummary,
} from "../lib/master";
import { REMOTE_DOT, remoteStateLabel, type RemoteStatus, type RemoteItem } from "../lib/remote";
import { switchProps } from "../lib/switchProps";
import { Dropdown, type DropdownOption } from "./Dropdown";
import type { Project } from "../App";
import { Markdown } from "./Markdown";

/** One rendered line, with a stable local key. A reply that failed validation
 * carries the reason inline; `warning` is a non-fatal follow-up problem. */
type Entry = MasterMessage & { error?: string; warning?: string; key: number };

/** Past-tense label for each action that ran; start_thread opens its thread instead. */
const MASTER_DONE: Record<string, string> = {
  start_thread: "started thread",
  send_message: "sent message",
  stop_thread: "stopped run",
  rename_thread: "renamed thread",
  set_peer_mailbox: "updated peer mailbox",
  delete_thread: "deleted thread",
  read_thread: "read thread",
};

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/** A short local "Nov 14, 09:41" stamp for the history rail. */
function fmtWhen(at: string): string {
  const t = Date.parse(at);
  if (Number.isNaN(t)) return "";
  return new Date(t).toLocaleString([], {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/**
 * The rail's title for a lazily-created conversation: the first line of the
 * message that started it, clipped. Master conversations run no title pass,
 * so the client names them the way threads' generateTitle would.
 */
function chatTitle(text: string): string {
  const line = text.split("\n", 1)[0].trim();
  if (!line) return "New chat";
  return line.length > 60 ? line.slice(0, 57) + "…" : line;
}

/**
 * One transcript row, memoized so typing in the composer re-renders only the
 * input, not every message. The markdown body is memoized separately.
 */
const EntryRow = memo(function EntryRow({ entry }: { entry: Entry }) {
  return (
    <div className={`master-msg ${entry.role === "user" ? "user" : "assistant"}`}>
      <div className="master-who">{entry.role === "user" ? "You" : "Orchestrator"}</div>
      {entry.text !== "" && (
        <div className="master-text">
          {entry.role === "user" ? entry.text : <Markdown text={entry.text} />}
        </div>
      )}
      {entry.error && <div className="master-error">{entry.error}</div>}
      {entry.warning && <div className="master-error master-warn">{entry.warning}</div>}
    </div>
  );
});

/**
 * One row of the history rail. The delete confirm is row-local, so it never
 * re-renders the rest of the list, and collapses when the button loses focus.
 */
const HistoryRow = memo(function HistoryRow({
  conv,
  active,
  disabled,
  onOpen,
  onDelete,
}: {
  conv: MasterThreadSummary;
  active: boolean;
  disabled: boolean;
  onOpen: (id: string) => void;
  onDelete: (id: string) => void;
}) {
  const [confirm, setConfirm] = useState(false);
  return (
    <div className={`master-hrow${active ? " active" : ""}`}>
      <button
        type="button"
        className="master-h-open"
        onClick={() => onOpen(conv.id)}
        disabled={disabled}
        title={conv.title}
      >
        <span className="master-h-title">{conv.title}</span>
        <span className="master-h-meta">
          {fmtWhen(conv.updatedAt)}
          {conv.messages > 0 ? ` · ${conv.messages} msg${conv.messages === 1 ? "" : "s"}` : ""}
        </span>
      </button>
      <button
        type="button"
        className={`master-h-del${confirm ? " confirm" : ""}`}
        onClick={() => {
          if (confirm) onDelete(conv.id);
          else setConfirm(true);
        }}
        onBlur={() => setConfirm(false)}
        disabled={disabled}
        title={confirm ? "Click again to delete this conversation" : "Delete conversation"}
        aria-label={`Delete conversation ${conv.title}`}
      >
        {confirm ? "Delete?" : "×"}
      </button>
    </div>
  );
});

/** Example openers for the empty state; one click fills the composer. */
function suggestions(projects: Project[]): string[] {
  const first = projects[0]?.name;
  return [
    first ? `Start a thread in ${first} to fix the failing tests` : "Start a thread and fix the failing tests",
    "What is the team working on right now?",
    "Summarize every project's latest runs",
  ];
}

/**
 * MasterChat is the conversation with the master orchestrator, on its own page:
 * the user describes a task in natural language, the orchestrator asks questions
 * or starts a thread and hands it to the app. Conversations are durable daemon
 * threads, so the history rail lists, reopens, and deletes them; a runtime
 * picker pins the harness and model the master itself runs on; reporter mode
 * pushes its status summary to WhatsApp whenever that bridge connects.
 */
export function MasterChat({
  rpc,
  projects,
  onOpenThread,
  harnessOptionsFor,
  modelOptions,
  refreshModels,
}: {
  rpc: Rpc | null;
  projects: Project[];
  onOpenThread: (projectId: string, threadId: string) => void;
  harnessOptionsFor: (current: string) => DropdownOption[];
  modelOptions: (harness: string, current: string) => DropdownOption[];
  refreshModels: (harness: string) => Promise<void>;
}) {
  const [entries, setEntries] = useState<Entry[]>([]);
  const [convs, setConvs] = useState<MasterThreadSummary[]>([]);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [historyOpen, setHistoryOpen] = useState(true);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);
  const [runtime, setRuntime] = useState<MasterRuntime | null>(null);
  const [runtimeError, setRuntimeError] = useState<string | null>(null);
  const [reporter, setReporter] = useState<boolean | null>(null);
  const [whatsapp, setWhatsapp] = useState<RemoteItem | null>(null);
  const busyRef = useRef(false);
  const bodyRef = useRef<HTMLDivElement | null>(null);
  const taRef = useRef<HTMLTextAreaElement | null>(null);
  // Stable keys for transcript rows: messages carry no server id, and index
  // keys break the moment a row is ever inserted or reordered. The counter is
  // global to the component, so keys stay unique across conversation switches.
  const keyRef = useRef(0);
  const nextKey = () => ++keyRef.current;

  const openConv = useCallback(
    async (id: string) => {
      if (!rpc || busyRef.current) return;
      try {
        const detail = await openMasterThread(rpc, id);
        setActiveId(id);
        setEntries(detail.messages.filter((m) => m.text !== "").map((m) => ({ ...m, key: nextKey() })));
        setSendError(null);
      } catch (e) {
        setSendError(errText(e));
      }
    },
    [rpc],
  );

  // HistoryRow takes a `(id) => void`, so the async openConv drops in directly
  // and keeps a stable identity: rail rows re-render only when their own
  // conversation row changes, not on every composer keystroke.
  const openHistoryRow = useCallback((id: string) => void openConv(id), [openConv]);

  // On connect, list past conversations and reopen the newest so a returning
  // user lands in context; a first visit starts on the empty state. The master
  // runtime and the reporter flag ride the same load.
  useEffect(() => {
    if (!rpc) return;
    let alive = true;
    getMasterRuntime(rpc)
      .then((rt) => {
        if (alive) setRuntime(rt);
      })
      .catch(() => undefined);
    getMasterReporter(rpc)
      .then((on) => {
        if (alive) setReporter(on);
      })
      .catch(() => undefined);
    (async () => {
      try {
        const list = await listMasterThreads(rpc);
        if (!alive || list.length === 0) return;
        setConvs(list);
        const detail = await openMasterThread(rpc, list[0].id);
        if (!alive) return;
        setActiveId(detail.id);
        setEntries(detail.messages.filter((m) => m.text !== "").map((m) => ({ ...m, key: nextKey() })));
      } catch {
        /* a failed history load leaves the composer usable statelessly */
      }
    })();
    return () => {
      alive = false;
    };
  }, [rpc]);

  // The WhatsApp row mirrors the Remote section: read once, then re-read on any
  // remote.* push rather than polling on a timer.
  useEffect(() => {
    if (!rpc) return;
    const read = () =>
      rpc
        .call<RemoteStatus>("remote.status")
        .then((st) => setWhatsapp(st.items?.find((i) => i.id === "whatsapp") ?? null))
        .catch(() => undefined);
    void read();
    return rpc.onNotification(({ method }) => {
      if (method.startsWith("remote.")) void read();
    });
  }, [rpc]);

  useEffect(() => {
    const el = bodyRef.current;
    if (!el) return;
    // Follow only when the reader is already at the end (the live-logs
    // pattern): scrolling up to reread a reply must not be yanked back down
    // by the next line.
    if (el.scrollHeight - el.scrollTop - el.clientHeight <= 48) el.scrollTop = el.scrollHeight;
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
    // The stateless fallback sends what was on screen before this message.
    const history: MasterMessage[] = entries
      .filter((e) => e.text !== "")
      .map((e) => ({ role: e.role, text: e.text }));
    busyRef.current = true;
    setBusy(true);
    setSendError(null);
    setInput("");
    setEntries((prev) => [...prev, { role: "user", text, key: nextKey() }]);
    try {
      // The first message of a new chat makes the conversation durable before
      // the model runs, so the exchange survives even if the send fails after.
      // Its title comes from this message: master conversations run no title
      // pass, and untitled rows are indistinguishable in the rail.
      let threadId = activeId;
      if (!threadId) {
        try {
          const conv = await newMasterThread(rpc, chatTitle(text));
          threadId = conv.id;
          setActiveId(conv.id);
          setConvs((prev) => [
            { id: conv.id, title: conv.title, updatedAt: conv.updatedAt, messages: 0 },
            ...prev,
          ]);
        } catch {
          /* no durable history: the exchange still runs statelessly */
        }
      }
      const result = await sendMaster(rpc, text, threadId, history);
      const entry: Entry = {
        role: "assistant",
        text: result.reply,
        key: nextKey(),
        ...(result.error ? { error: result.error } : {}),
        ...(result.warning ? { warning: result.warning } : {}),
      };
      setEntries((prev) => {
        let next = [...prev, entry];
        if (!threadId) {
          // Stateless sends keep the local confirmation line the durable
          // transcript would otherwise hold.
          const done = (result.results ?? [])
            .filter((r) => r.ok && r.type !== "start_thread")
            .map((r) => MASTER_DONE[r.type]);
          if (done.length > 0)
            next = [...next, { role: "assistant", text: `Done: ${done.join("; ")}.`, key: nextKey() }];
        }
        return next;
      });
      if (threadId) {
        // The conversation saw activity: move it to the top of the rail.
        const when = new Date().toISOString();
        setConvs((prev) => {
          const row = prev.find((c) => c.id === threadId);
          const bumped = {
            id: threadId,
            title: row?.title ?? "",
            updatedAt: when,
            messages: (row?.messages ?? 0) + 2,
          };
          return [bumped, ...prev.filter((c) => c.id !== threadId)];
        });
      }
      // The daemon created the thread and started its first turn in the
      // background; say which one and open it.
      if (result.threadId && result.projectId) {
        const projectName =
          projects.find((p) => p.id === result.projectId)?.name ?? result.projectId;
        const title =
          result.actions?.find((a) => a.type === "start_thread")?.title ?? "New thread";
        setEntries((prev) => [
          ...prev,
          { role: "assistant", text: `Started “${title}” in ${projectName}.`, key: nextKey() },
        ]);
        onOpenThread(result.projectId, result.threadId);
      }
    } catch (e) {
      // Transport failures stay out of the transcript so the composer remains usable.
      setSendError(errText(e));
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  };

  const newChat = useCallback(() => {
    if (busyRef.current) return;
    setActiveId(null);
    setEntries([]);
    setSendError(null);
    setInput("");
    taRef.current?.focus();
  }, []);

  const deleteConv = useCallback(
    (id: string) => {
      if (!rpc) return;
      void (async () => {
        try {
          await deleteMasterThread(rpc, id);
          const rest = convs.filter((c) => c.id !== id);
          setConvs(rest);
          if (activeId === id) {
            if (rest.length > 0) await openConv(rest[0].id);
            else newChat();
          }
        } catch (e) {
          setSendError(errText(e));
        }
      })();
    },
    [rpc, convs, activeId, openConv, newChat],
  );

  const applyRuntime = async (patch: { harness: string; model: string; variant: string }) => {
    if (!rpc) return;
    setRuntimeError(null);
    try {
      setRuntime(await setMasterRuntime(rpc, patch));
    } catch (e) {
      setRuntimeError(errText(e));
      // Show what the daemon actually holds after a refused patch.
      getMasterRuntime(rpc)
        .then(setRuntime)
        .catch(() => undefined);
    }
  };

  const pickHarness = (v: string) => {
    if (!runtime) return;
    // Overriding the harness drops the inherited model, the same rule a
    // thread's lead override follows.
    void applyRuntime({ harness: v, model: "", variant: "" });
    if (v) refreshModels(v).catch(() => undefined);
  };

  const pickModel = (m: string) => {
    if (!runtime) return;
    void applyRuntime({ harness: runtime.harness, model: m, variant: runtime.variant });
  };

  const toggleReporter = () => {
    if (!rpc || reporter === null) return;
    setMasterReporter(rpc, !reporter)
      .then(setReporter)
      .catch((e) => setRuntimeError(errText(e)));
  };

  const onKeyDown = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing || e.keyCode === 229) return;
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void send();
    }
  };

  const empty = entries.length === 0 && !sendError;
  const effectiveHarness = runtime?.effective.harness ?? "";
  const harnessOptions: DropdownOption[] = runtime
    ? [
        { value: "", label: `Orchestrator role (${effectiveHarness || "default"})` },
        ...harnessOptionsFor(runtime.harness),
      ]
    : [];
  const shows = useMemo(() => suggestions(projects), [projects]);

  return (
    <section className="master-shell" aria-label="Orchestrator">
      {historyOpen && convs.length > 0 && (
        <aside className="master-rail" aria-label="Past orchestrator conversations">
          <div className="master-rail-head">
            <span>History</span>
            <span className="master-rail-count">{convs.length}</span>
          </div>
          <div className="master-rail-list">
            {convs.map((c) => (
              <HistoryRow
                key={c.id}
                conv={c}
                active={c.id === activeId}
                disabled={busy}
                onOpen={openHistoryRow}
                onDelete={deleteConv}
              />
            ))}
          </div>
        </aside>
      )}
      <section className={`master-chat${empty ? " is-empty" : ""}`} aria-label="Orchestrator chat">
        <header className="master-head">
          {convs.length > 0 && (
            <button
              type="button"
              className="master-rail-toggle"
              onClick={() => setHistoryOpen((v) => !v)}
              aria-label={historyOpen ? "Hide past conversations" : "Show past conversations"}
              title={historyOpen ? "Hide history" : "Show history"}
            >
              {historyOpen ? "‹" : "›"}
            </button>
          )}
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
        <div className="master-toolbar">
          <span className="master-tb-label">Runs on</span>
          <div className="master-tb-dd">
            <Dropdown
              value={runtime?.harness ?? ""}
              options={harnessOptions}
              onChange={pickHarness}
              disabled={!rpc || !runtime}
            />
          </div>
          <div className="master-tb-dd model">
            <Dropdown
              value={runtime?.model ?? ""}
              options={modelOptions(effectiveHarness, runtime?.model ?? "")}
              searchable
              onChange={pickModel}
              disabled={!rpc || !runtime}
              placeholder="model (default)"
              footNote="Discovered from the harness CLI"
              onRefresh={() => effectiveHarness && refreshModels(effectiveHarness).catch(() => undefined)}
            />
          </div>
          {runtimeError && (
            <span className="master-tb-err" title={runtimeError} role="alert">
              !
            </span>
          )}
          <div className="master-reporter">
            <div
              className={`toggle${reporter ? " on" : ""}`}
              {...switchProps(!!reporter, toggleReporter)}
              aria-disabled={reporter === null}
              title="Reporter: whenever WhatsApp connects, the master sends its status summary there."
            >
              <span className="sw" /> Reporter
            </div>
            <span
              className="master-wa"
              title={whatsapp?.detail || "WhatsApp bridge connection state"}
            >
              <span className={`dot ${REMOTE_DOT[whatsapp?.state ?? "off"]}`} aria-hidden="true" />
              {whatsapp?.unavailable ? "Not configured" : `WhatsApp ${remoteStateLabel(whatsapp?.state ?? "off")}`}
            </span>
          </div>
        </div>
        <div className="master-body" ref={bodyRef} aria-live="polite">
          {empty ? (
            <div className="master-empty">
              <p className="master-empty-title">What should the team do?</p>
              <p className="master-empty-sub">
                Describe a task in plain language and name the project. The orchestrator will ask
                questions, or start the thread with a model it picks.
              </p>
              <div className="master-suggest">
                {shows.map((s) => (
                  <button
                    type="button"
                    key={s}
                    onClick={() => {
                      setInput(s);
                      taRef.current?.focus();
                    }}
                  >
                    {s}
                  </button>
                ))}
              </div>
            </div>
          ) : (
            entries.map((entry) => <EntryRow key={entry.key} entry={entry} />)
          )}
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
    </section>
  );
}
