import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent,
} from "react";
import { Window } from "@wailsio/runtime";
import { discover, pickFolder, Rpc, type Conn } from "./lib/rpc";
import {
  addUsage,
  deriveThreadUsage,
  fmtCost,
  fmtTokens,
  totalTokens,
  type ThreadUsage,
  type TurnUsage,
  type UsageEntry,
  type UsageStep,
} from "./lib/usage";
import { ContextMenu, type MenuItem } from "./components/ContextMenu";
import { AgentGraph } from "./components/AgentGraph";
import { KanbanBoard } from "./components/KanbanBoard";
import { TaskList, type TaskItem } from "./components/TaskList";
import { DiffView } from "./components/DiffView";
import { Dropdown, type DropdownOption } from "./components/Dropdown";
import { FilesPanel, type FileContentLike, type FileEntryLike } from "./components/FilesPanel";
import { PRPanel, type PRSnapshot } from "./components/PRPanel";
import { Markdown } from "./components/Markdown";
import { AgentDetail } from "./components/AgentDetail";
import { ThreadInfoDialog } from "./components/ThreadInfoDialog";
import { UpdateChip, type UpdateStatus } from "./components/UpdateChip";
import type { MailboxMessage } from "./lib/graph";

type Role = {
  id: string;
  name: string;
  defaultHarness: string;
  defaultModel: string;
  builtin: boolean;
};
type Agent = {
  id: string;
  parentAgentId?: string;
  name?: string;
  role: string;
  harness: string;
  model: string;
  task: string;
  dependsOn?: string[];
  status: string;
  pipelineId?: string;
  startedAt?: string;
  endedAt?: string;
};
type LogEntry = { agentId: string; kind: string; text: string; at: string };
type Message = { id: string; author: string; text: string; at: string };
type ReviewComment = { id: string; file: string; line: number; body: string };
type ThreadState = {
  id: string;
  title: string;
  autoMode: boolean;
  peerMailbox: boolean;
  /** Orchestrator team size; zero or absent means one lead. */
  leads?: number;
  messages: Message[] | null;
  mailbox: MailboxMessage[] | null;
  agents: Record<string, Agent> | null;
  agentOrder: string[] | null;
  usage: Record<string, UsageEntry> | null;
  steps: Record<string, UsageStep[]> | null;
  turn: number;
  turns: TurnUsage[] | null;
  shared: Record<string, string> | null;
  reviewComments: ReviewComment[] | null;
  /** The agreed task list, in the order the leads added it. */
  tasks?: TaskItem[] | null;
};
type ThreadSummary = {
  id: string;
  title: string;
  projectId?: string;
  updatedAt?: string;
  messages: number;
  agents: number;
  autoMode: boolean;
  runningAgents?: number;
  staleAfterSeconds?: number;
  stale?: boolean;
};
type Project = { id: string; name: string; path: string; threads: number };

/** The environment's cross-project memory: guide, learnings log, rules. */
type InheritedView = {
  dir: string;
  guide: string;
  log: string;
  rules: { name: string; body: string }[] | null;
  rendered: string;
};

/** One editable seat in the council sheet before it is convened. */
type CouncilMemberRow = {
  name: string;
  role: string;
  harness: string;
  model: string;
  perspective: string;
};
type DiffLine = { old?: number; new?: number; kind: string; text: string };
type DiffFile = {
  path: string;
  oldPath?: string;
  additions: number;
  deletions: number;
  lines: DiffLine[] | null;
};
type Diff = { files: DiffFile[] | null; raw: string; empty: boolean };
type HarnessInfo = {
  all: string[];
  available: string[];
  enabled: string[] | null;
  models: Record<string, string[]> | null;
};
type RoleConfigView = {
  id: string;
  name: string;
  harness: string;
  model: string;
  variant: string;
  builtin: boolean;
};
type SettingsView = {
  workdir: string;
  autoMode: boolean;
  approvedModels: string[] | null;
  roles: RoleConfigView[];
  configPath: string;
  staleAfterMinutes?: number;
};

/** Aside panel tabs: agents + usage are pinned; the rest are addable.
 * (The team graph lives as a center tab beside Chat and Review. See `tab`.) */
type AsideTabId = "agents" | "usage" | "context" | "inherited" | "files" | "diff" | "pr";
const ASIDE_PINNED: AsideTabId[] = ["agents", "usage"];
const ASIDE_META: Record<AsideTabId, { label: string; key: string }> = {
  agents: { label: "Agents", key: "A" },
  usage: { label: "Token usage", key: "U" },
  context: { label: "Context", key: "C" },
  inherited: { label: "Inherited", key: "M" },
  files: { label: "Files", key: "F" },
  diff: { label: "Diff", key: "D" },
  pr: { label: "Pull request", key: "P" },
};
/** Projects opened as tabs, remembered across restarts. */
function loadOpenProjects(): string[] {
  try {
    const raw = window.localStorage.getItem("at.openProjects");
    if (raw) {
      const ids = JSON.parse(raw) as unknown;
      if (Array.isArray(ids)) return ids.filter((id): id is string => typeof id === "string");
    }
  } catch {
    /* private mode or corrupt: start with no tabs */
  }
  return [];
}

/** Composer drafts keyed by project and thread, remembered across restarts. */
function draftKeyOf(projectId: string | null, threadId: string | null): string {
  return `${projectId ?? ""}\u001f${threadId ?? ""}`;
}

function loadDrafts(): Record<string, string> {
  try {
    const raw = window.localStorage.getItem("at.drafts");
    if (raw) {
      const parsed = JSON.parse(raw) as unknown;
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        const out: Record<string, string> = {};
        for (const [k, v] of Object.entries(parsed)) if (typeof v === "string") out[k] = v;
        return out;
      }
    }
  } catch {
    /* private mode or corrupt: start with no drafts */
  }
  return {};
}

/** One line of the activity log box. */
type LiveLine = { agentId: string; kind: string; text: string };
const LIVE_LIMIT = 400;

/** Appends one output line. Harnesses stream text in chunks, so a text or
 *  reasoning chunk from the same agent grows the last block instead of stacking. */
function pushLiveLine(prev: LiveLine[], line: LiveLine): LiveLine[] {
  const last = prev[prev.length - 1];
  if (
    last &&
    last.agentId === line.agentId &&
    last.kind === line.kind &&
    (line.kind === "text" || line.kind === "reasoning")
  ) {
    return [...prev.slice(0, -1), { ...last, text: last.text + line.text }];
  }
  return [...prev.slice(-(LIVE_LIMIT - 1)), line];
}

/** Frames published while the snapshot loads may already be in the snapshot:
 *  the daemon appends a line before publishing it. Drop the longest run at the
 *  start of `buffered` that repeats the snapshot's tail, keeping only new lines. */
function dropSnapshotOverlap(snapshot: LiveLine[], buffered: LiveLine[]): LiveLine[] {
  const same = (a: LiveLine, b: LiveLine) =>
    a.agentId === b.agentId && a.kind === b.kind && a.text === b.text;
  for (let k = Math.min(snapshot.length, buffered.length); k > 0; k--) {
    const tail = snapshot.slice(snapshot.length - k);
    if (tail.every((line, i) => same(line, buffered[i]))) return buffered.slice(k);
  }
  return buffered;
}

function loadAsideTabs(): AsideTabId[] {
  try {
    const raw = window.localStorage.getItem("at.asideTabs");
    if (raw) {
      const ids = (JSON.parse(raw) as string[]).filter((id): id is AsideTabId => id in ASIDE_META);
      for (const p of ASIDE_PINNED) if (!ids.includes(p)) ids.unshift(p);
      if (ids.length > 0) return ids;
    }
  } catch {
    /* private mode or corrupt: fall through to defaults */
  }
  return [...ASIDE_PINNED];
}

type FileTreeState = {
  root: string;
  entries: FileEntryLike[];
  status: Record<string, string>;
};

/** Elapsed shows how long the running work has been going; it ticks on its own so
 * only this tiny node re-renders. */
function Elapsed({ since }: { since?: string }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const timer = window.setInterval(() => tick((n) => n + 1), 1000);
    return () => window.clearInterval(timer);
  }, []);

  const started = since ? Date.parse(since) : NaN;
  if (Number.isNaN(started)) return null;
  const secs = Math.max(0, Math.round((Date.now() - started) / 1000));
  const label =
    secs < 60 ? `${secs}s` : `${Math.floor(secs / 60)}m ${String(secs % 60).padStart(2, "0")}s`;
  return <span className="act-elapsed">{label}</span>;
}

/** fmtUpdated renders a thread's last activity as a short local date. */
function fmtUpdated(at?: string): string {
  if (!at) return "";
  const date = new Date(at);
  if (Number.isNaN(date.getTime()) || date.getFullYear() < 1970) return "";
  return date.toLocaleDateString([], { month: "short", day: "numeric", year: "numeric" });
}

/** Tab strips stay divs so they can hold a close control, which a button may
 * not. They get the keyboard behavior a button would otherwise provide. */
function tabProps(selected: boolean, onSelect: () => void) {
  return {
    role: "tab" as const,
    "aria-selected": selected,
    tabIndex: 0,
    onClick: onSelect,
    onKeyDown: (e: ReactKeyboardEvent<HTMLDivElement>) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        onSelect();
      }
    },
  };
}

/** Same treatment for the pill switches. */
function switchProps(on: boolean, onToggle: () => void) {
  return {
    role: "switch" as const,
    "aria-checked": on,
    tabIndex: 0,
    onClick: onToggle,
    onKeyDown: (e: ReactKeyboardEvent<HTMLDivElement>) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        onToggle();
      }
    },
  };
}

/** Clickable cards and rows are divs, so give them the button contract and
 * leave clicks on nested controls to those controls. */
function clickable(onActivate: () => void) {
  return {
    role: "button" as const,
    tabIndex: 0,
    onClick: onActivate,
    onKeyDown: (e: ReactKeyboardEvent<HTMLElement>) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        onActivate();
      }
    },
  };
}

/** Minimal typings for the Web Speech API (nonstandard, prefixed in WebKit). */
type SpeechRecognitionLike = {
  lang: string;
  continuous: boolean;
  interimResults: boolean;
  start(): void;
  stop(): void;
  onresult:
    | ((e: {
        resultIndex: number;
        results: ArrayLike<ArrayLike<{ transcript: string }> & { isFinal: boolean }>;
      }) => void)
    | null;
  onerror: ((e: { error?: string }) => void) | null;
  onend: (() => void) | null;
};

function getSpeechRecognition(): (new () => SpeechRecognitionLike) | null {
  const w = window as unknown as Record<string, unknown>;
  return (w.SpeechRecognition ?? w.webkitSpeechRecognition ?? null) as
    | (new () => SpeechRecognitionLike)
    | null;
}

/**
 * MicButton dictates into a text callback using the Web Speech API when the
 * webview provides it. When it does not (Wails' WebKit on macOS, for one), the
 * button degrades to a pointer at the OS dictation shortcut instead of a lying
 * control.
 */
function MicButton({
  onText,
  disabled,
  onUnavailable,
}: {
  onText: (text: string) => void;
  disabled?: boolean;
  onUnavailable?: (msg: string) => void;
}) {
  const [listening, setListening] = useState(false);
  const recRef = useRef<SpeechRecognitionLike | null>(null);

  const stop = () => {
    recRef.current?.stop();
    recRef.current = null;
    setListening(false);
  };

  const toggle = () => {
    if (listening) {
      stop();
      return;
    }
    const Ctor = getSpeechRecognition();
    if (!Ctor) {
      onUnavailable?.(
        "Dictation is not available in this webview. Use your OS dictation instead (macOS: Edit ▸ Start Dictation, or press Fn Fn).",
      );
      return;
    }
    const rec = new Ctor();
    rec.lang = navigator.language || "en-US";
    rec.continuous = true;
    rec.interimResults = false;
    rec.onresult = (e) => {
      for (let i = e.resultIndex; i < e.results.length; i++) {
        const res = e.results[i];
        if (res.isFinal) onText(res[0].transcript);
      }
    };
    rec.onerror = () => stop();
    rec.onend = () => {
      recRef.current = null;
      setListening(false);
    };
    recRef.current = rec;
    rec.start();
    setListening(true);
  };

  useEffect(() => () => recRef.current?.stop(), []);

  return (
    <button
      className={`mic-btn${listening ? " on" : ""}`}
      onClick={toggle}
      disabled={disabled}
      title={
        listening
          ? "Stop dictation"
          : "Dictate your message (falls back to OS dictation when unavailable)"
      }
    >
      <svg
        viewBox="0 0 24 24"
        width="14"
        height="14"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <rect x="9" y="3" width="6" height="11" rx="3" />
        <path d="M5 11a7 7 0 0 0 14 0M12 18v3" />
      </svg>
      {listening ? "Stop" : "Dictate"}
    </button>
  );
}

/** fmtIdleDuration renders an age as "45s" / "12m" / "3h 05m" / "4d". */
function fmtIdleDuration(ms: number): string {
  const mins = Math.floor(ms / 60_000);
  if (mins < 1) return `${Math.max(0, Math.round(ms / 1000))}s`;
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ${String(mins % 60).padStart(2, "0")}m`;
  return `${Math.floor(hours / 24)}d`;
}

/**
 * ThreadClock shows a thread's activity state as a live pill: "Working" while
 * agents run, "Stale" once the thread has been idle past the configured
 * threshold, or a countdown to staleness otherwise. It owns a coarse interval
 * (30s) and re-renders only itself, so a full board of timers costs almost
 * nothing.
 */
function ThreadClock({ t }: { t: ThreadSummary }) {
  const [, bump] = useState(0);
  useEffect(() => {
    const id = setInterval(() => bump((n) => n + 1), 30_000);
    return () => clearInterval(id);
  }, []);

  if ((t.runningAgents ?? 0) > 0) {
    return <span className="pill run">● Working</span>;
  }
  const at = t.updatedAt ? Date.parse(t.updatedAt) : NaN;
  if (Number.isNaN(at)) return null;
  const idleMs = Date.now() - at;
  const afterMs = (t.staleAfterSeconds ?? 0) * 1000;
  if (afterMs <= 0) return null;
  if (idleMs >= afterMs) {
    return <span className="pill stale">◌ Stale · {fmtIdleDuration(idleMs)}</span>;
  }
  return <span className="pill live">○ Stale in {fmtIdleDuration(afterMs - idleMs)}</span>;
}

export default function App() {
  const [status, setStatus] = useState<"connecting" | "online" | "offline">("connecting");
  const [error, setError] = useState<string | null>(null);

  const [roles, setRoles] = useState<Role[]>([]);
  const [harnesses, setHarnesses] = useState<HarnessInfo>({
    all: [],
    available: [],
    enabled: null,
    models: null,
  });
  const [threads, setThreads] = useState<ThreadSummary[]>([]);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [thread, setThread] = useState<ThreadState | null>(null);
  const [globalUsage, setGlobalUsage] = useState<ThreadUsage | null>(null);
  const [update, setUpdate] = useState<UpdateStatus | null>(null);
  const [updateBusy, setUpdateBusy] = useState(false);
  const [updateError, setUpdateError] = useState<string | null>(null);

  const [busy, setBusy] = useState(false);
  /** The thread whose "! cmd" is running, so its Stop bar only shows there. */
  const [shellThread, setShellThread] = useState<string | null>(null);
  const [detailId, setDetailId] = useState<string | null>(null);
  // Aside panel tabs: agents + usage are pinned; context, files, and diff can
  // be added via the "+" menu (or single-letter shortcuts) and closed again.
  const [asideTabs, setAsideTabs] = useState<AsideTabId[]>(loadAsideTabs);
  const [asideActive, setAsideActive] = useState<AsideTabId>("agents");
  const [panelMenu, setPanelMenu] = useState<{ x: number; y: number } | null>(null);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [live, setLive] = useState<{ agentId: string; kind: string; text: string }[]>([]);

  const [tab, setTab] = useState<"chat" | "review" | "graph" | "board">("chat");
  const [diff, setDiff] = useState<Diff | null>(null);
  const [diffError, setDiffError] = useState<string | null>(null);
  const [draft, setDraft] = useState<{ file: string; line: number; body: string } | null>(null);

  // File browser state for the aside Files panel.
  const [fileTree, setFileTree] = useState<FileTreeState | null>(null);
  const [fileTreeLoading, setFileTreeLoading] = useState(false);

  // Inherited context panel state: the environment's shared memory.
  const [inherited, setInherited] = useState<InheritedView | null>(null);
  const [inheritedGuide, setInheritedGuide] = useState("");
  const [inheritedLog, setInheritedLog] = useState("");
  const [inheritedDirty, setInheritedDirty] = useState(false);
  const [inheritedRules, setInheritedRules] = useState<{ name: string; body: string }[]>([]);
  const [newRuleName, setNewRuleName] = useState("");
  const [fileTreeError, setFileTreeError] = useState<string | null>(null);
  // PR/MR status for the active thread; tagged with its thread so a late reply never shows elsewhere.
  const [prState, setPRState] = useState<PRSnapshot | null>(null);
  const [prLoading, setPRLoading] = useState(false);
  const [prError, setPRError] = useState<string | null>(null);
  const [selectedFile, setSelectedFile] = useState<string | null>(null);
  const [fileContent, setFileContent] = useState<FileContentLike | null>(null);
  const [fileContentLoading, setFileContentLoading] = useState(false);
  const [fileContentError, setFileContentError] = useState<string | null>(null);

  const [settingsOpen, setSettingsOpen] = useState(false);
  const [modelConfigOpen, setModelConfigOpen] = useState(false);
  const [settings, setSettings] = useState<SettingsView | null>(null);

  // Council of agents: convene several perspectives on one question and let
  // them debate to a synthesized outcome.
  const [councilOpen, setCouncilOpen] = useState(false);
  const [councilTopic, setCouncilTopic] = useState("");
  const [councilRounds, setCouncilRounds] = useState(2);
  const [councilMembers, setCouncilMembers] = useState<CouncilMemberRow[]>([
    {
      name: "planner",
      role: "planner",
      harness: "",
      model: "",
      perspective: "weigh architecture and long-term maintainability",
    },
    {
      name: "executor",
      role: "executor",
      harness: "",
      model: "",
      perspective: "prize the simplest thing that actually ships",
    },
    {
      name: "reviewer",
      role: "reviewer",
      harness: "",
      model: "",
      perspective: "hunt risk, edge cases, and hidden cost",
    },
  ]);
  const [workdirInput, setWorkdirInput] = useState("");
  const [approvedInput, setApprovedInput] = useState("");
  const [staleAfterInput, setStaleAfterInput] = useState("30");
  const [saved, setSaved] = useState(false);
  const [modelsNotice, setModelsNotice] = useState<string | null>(null);

  const [projects, setProjects] = useState<Project[]>([]);
  const [projectId, setProjectId] = useState<string | null>(null);
  // Each (project, thread) keeps its own composer text, so switching threads
  // swaps the draft instead of carrying it over.
  const [drafts, setDrafts] = useState<Record<string, string>>(loadDrafts);
  const draftKey = draftKeyOf(projectId, activeId);
  const input = drafts[draftKey] ?? "";
  // Mirrors send(): a leading "!" (after trimming) runs a shell command.
  const commandMode = input.trimStart().startsWith("!");
  const setInput = useCallback(
    (text: string) => {
      if (!activeId) return;
      setDrafts((prev) => ({ ...prev, [draftKey]: text }));
    },
    [activeId, draftKey],
  );
  // Projects stay open as tabs; each one remembers the thread it was showing.
  const [openProjectIds, setOpenProjectIds] = useState<string[]>(loadOpenProjects);
  const lastThreadByProject = useRef<Record<string, string | null>>({});
  const [showNewProject, setShowNewProject] = useState(false);
  const [newProjectName, setNewProjectName] = useState("");
  const [newProjectPath, setNewProjectPath] = useState("");

  // Thread context-menu flow: the menu itself, plus the three dialogs it can
  // open (rename, delete confirm, usage/subagent drill-in).
  const [threadMenu, setThreadMenu] = useState<{
    thread: ThreadSummary;
    x: number;
    y: number;
  } | null>(null);
  const [renameTarget, setRenameTarget] = useState<ThreadSummary | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<ThreadSummary | null>(null);
  const [infoTarget, setInfoTarget] = useState<{ id: string; tab: "usage" | "agents" } | null>(
    null,
  );
  const [infoState, setInfoState] = useState<{ loading: boolean; thread: ThreadState | null }>({
    loading: false,
    thread: null,
  });

  const rpcRef = useRef<Rpc | null>(null);
  const debounce = useRef<number | undefined>(undefined);
  // Chat stream auto-scroll: pinned to bottom while the user is already there.
  const streamRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const el = streamRef.current;
    if (!el) return;
    // Follow the stream only when the user is near the bottom; reading history
    // wins over jumping.
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 120;
    if (nearBottom) el.scrollTop = el.scrollHeight;
  }, [thread?.messages?.length, thread?.messages?.[thread.messages.length - 1]?.text, live.length]);

  const refresh = useCallback(async (id: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    const [state, list, usage] = await Promise.all([
      rpc.call<ThreadState>("thread.get", { threadId: id }),
      rpc.call<ThreadSummary[]>("thread.list"),
      rpc.call<ThreadUsage>("usage.get"),
    ]);
    setThread(state);
    setThreads(list);
    setGlobalUsage(usage);
  }, []);

  const installUpdate = useCallback(async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    setUpdateBusy(true);
    setUpdateError(null);
    try {
      setUpdate(await rpc.call<UpdateStatus>("update.apply"));
    } catch (e) {
      setUpdateError(e instanceof Error ? e.message : String(e));
    } finally {
      setUpdateBusy(false);
    }
  }, []);

  const loadDiff = useCallback(async (id: string | null) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      // Thread-scoped: the daemon diffs the thread's project directory, not
      // the global fallback workdir.
      const d = await rpc.call<Diff>("review.diff", { threadId: id });
      setDiff(d);
      setDiffError(null);
    } catch (e) {
      setDiffError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  const loadPR = useCallback(async (id: string | null) => {
    const rpc = rpcRef.current;
    if (!rpc || !id) return;
    setPRLoading(true);
    setPRError(null);
    try {
      setPRState({ ...(await rpc.call<PRSnapshot>("pr.status", { threadId: id })), threadId: id });
    } catch (e) {
      setPRError(e instanceof Error ? e.message : String(e));
    } finally {
      setPRLoading(false);
    }
  }, []);

  const loadFileTree = useCallback(async (id: string | null) => {
    const rpc = rpcRef.current;
    if (!rpc || !id) return;
    setFileTreeLoading(true);
    setFileTreeError(null);
    try {
      setFileTree(await rpc.call<FileTreeState>("files.tree", { threadId: id }));
      setSelectedFile(null);
      setFileContent(null);
      setFileContentError(null);
    } catch (e) {
      setFileTreeError(e instanceof Error ? e.message : String(e));
    } finally {
      setFileTreeLoading(false);
    }
  }, []);

  const loadFileContent = useCallback(async (id: string | null, path: string) => {
    const rpc = rpcRef.current;
    if (!rpc || !id) return;
    setSelectedFile(path);
    setFileContent(null);
    setFileContentError(null);
    setFileContentLoading(true);
    try {
      setFileContent(await rpc.call<FileContentLike>("files.read", { threadId: id, path }));
    } catch (e) {
      setFileContentError(e instanceof Error ? e.message : String(e));
    } finally {
      setFileContentLoading(false);
    }
  }, []);

  const openPanel = useCallback((id: AsideTabId) => {
    setAsideTabs((prev) => (prev.includes(id) ? prev : [...prev, id]));
    setAsideActive(id);
  }, []);

  const closePanel = useCallback((id: AsideTabId) => {
    if (ASIDE_PINNED.includes(id)) return;
    setAsideTabs((prev) => prev.filter((t) => t !== id));
    setAsideActive((cur) => (cur === id ? "agents" : cur));
  }, []);

  const loadHarnesses = useCallback(async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    setHarnesses(await rpc.call<HarnessInfo>("harness.list"));
  }, []);

  const loadProjects = useCallback(async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    setProjects(await rpc.call<Project[]>("project.list"));
  }, []);

  const createProject = async () => {
    const rpc = rpcRef.current;
    if (!rpc || !newProjectName.trim()) return;
    const res = await rpc.call<{ project: Project; projects: Project[] }>("project.create", {
      name: newProjectName.trim(),
      path: newProjectPath.trim(),
    });
    setProjects(res.projects);
    setNewProjectName("");
    setNewProjectPath("");
    setShowNewProject(false);
    openProject(res.project.id);
  };

  const deleteProject = async (id: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    setProjects(await rpc.call<Project[]>("project.delete", { id }));
    closeProjectTab(id);
  };

  /** Thread selection also records the project's place, so its tab reopens
   * where it was left. */
  const selectThread = (id: string | null) => {
    setActiveId(id);
    if (projectId) lastThreadByProject.current[projectId] = id;
  };

  /** The thread a project tab should reopen, unless it has since been deleted. */
  const rememberedThread = (id: string | null): string | null => {
    if (!id) return null;
    const threadId = lastThreadByProject.current[id] ?? null;
    if (!threadId) return null;
    return threads.some((t) => t.id === threadId) ? threadId : null;
  };

  /** Opening a project reuses its tab and restores the thread it was showing;
   * the project grid and the tab strip both go through here. */
  const openProject = (id: string) => {
    setOpenProjectIds((prev) => (prev.includes(id) ? prev : [...prev, id]));
    setProjectId(id);
    setActiveId(rememberedThread(id));
  };

  const showProjects = () => {
    setProjectId(null);
    setActiveId(null);
  };

  const closeProjectTab = (id: string) => {
    const index = openProjectIds.indexOf(id);
    const next = openProjectIds.filter((x) => x !== id);
    delete lastThreadByProject.current[id];
    setOpenProjectIds(next);
    if (projectId !== id) return;
    const fallback = next[index] ?? next[index - 1] ?? null;
    setProjectId(fallback);
    setActiveId(rememberedThread(fallback));
  };

  // --- thread context-menu actions ---------------------------------------

  const openThreadMenu = (t: ThreadSummary, e: MouseEvent) => {
    e.preventDefault();
    setThreadMenu({ thread: t, x: e.clientX, y: e.clientY });
  };

  const reloadThreads = async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    setThreads(await rpc.call<ThreadSummary[]>("thread.list"));
  };

  const beginRename = (t: ThreadSummary) => {
    setRenameTarget(t);
    setRenameValue(t.title);
  };

  const confirmRename = async () => {
    const rpc = rpcRef.current;
    const title = renameValue.trim();
    if (!rpc || !renameTarget || !title) return;
    try {
      await rpc.call("thread.rename", { threadId: renameTarget.id, title });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return;
    }
    const renamed = renameTarget;
    setRenameTarget(null);
    await reloadThreads();
    if (activeId === renamed.id) refresh(activeId).catch(() => undefined);
  };

  const confirmDeleteThread = async () => {
    const rpc = rpcRef.current;
    if (!rpc || !deleteTarget) return;
    try {
      await rpc.call("thread.delete", { threadId: deleteTarget.id });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return;
    }
    const deleted = deleteTarget;
    setDeleteTarget(null);
    await reloadThreads();
    // Deleting the open thread closes back to the thread list.
    if (activeId === deleted.id) {
      selectThread(null);
      setThread(null);
    }
  };

  /** Fetches a thread's state for the info dialog; works for threads that are
   * not currently open. */
  useEffect(() => {
    if (!infoTarget) return;
    const rpc = rpcRef.current;
    if (!rpc) return;
    let cancelled = false;
    setInfoState({ loading: true, thread: null });
    rpc
      .call<ThreadState>("thread.get", { threadId: infoTarget.id })
      .then((st) => {
        if (!cancelled) setInfoState({ loading: false, thread: st });
      })
      .catch(() => {
        if (!cancelled) setInfoState({ loading: false, thread: null });
      });
    return () => {
      cancelled = true;
    };
  }, [infoTarget]);

  // Adding a project starts with the native folder picker; the chosen path seeds
  // the name, which stays editable. Cancelling still shows the form so a path can
  // be typed (for example in the browser, where there is no shell dialog).
  const openPicker = async () => {
    const path = await pickFolder();
    setShowNewProject(true);
    if (!path) return;
    setNewProjectPath(path);
    setNewProjectName((name) => name.trim() || (path.split("/").filter(Boolean).pop() ?? ""));
  };

  const refreshModels = useCallback(async (harness: string) => {
    const rpc = rpcRef.current;
    if (!rpc || !harness) return;
    try {
      const res = await rpc.call<{ harness: string; models: string[] | null; error?: string }>(
        "models.refresh",
        { harness },
      );
      setHarnesses((prev) => ({
        ...prev,
        models: { ...(prev.models ?? {}), [res.harness]: res.models ?? [] },
      }));
      setModelsNotice(res.error ? `Could not discover ${res.harness} models: ${res.error}` : null);
    } catch (e) {
      setModelsNotice(e instanceof Error ? e.message : String(e));
    }
  }, []);

  /** Loads the inherited-context panel: guide, log, and rules. */
  const loadInherited = useCallback(async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      const view = await rpc.call<InheritedView>("context.inherited.get");
      setInherited(view);
      setInheritedGuide(view.guide);
      setInheritedLog(view.log);
      setInheritedRules(view.rules ?? []);
      setInheritedDirty(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  /** Saves the guide and log drafts. */
  const saveInherited = async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      await rpc.call("context.inherited.save", {
        guide: inheritedGuide,
        log: inheritedLog,
      });
      setInheritedDirty(false);
      await loadInherited();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const saveInheritedRule = async (name: string, body: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      await rpc.call("context.inherited.rule.save", { name, body });
      await loadInherited();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const deleteInheritedRule = async (name: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      await rpc.call("context.inherited.rule.delete", { name });
      await loadInherited();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const addModel = useCallback(async (harness: string, model: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    const res = await rpc.call<{ harness: string; models: string[] | null }>("models.add", {
      harness,
      model,
    });
    setHarnesses((prev) => ({
      ...prev,
      models: { ...(prev.models ?? {}), [res.harness]: res.models ?? [] },
    }));
  }, []);

  const loadSettings = useCallback(async (): Promise<SettingsView | null> => {
    const rpc = rpcRef.current;
    if (!rpc) return null;
    const s = await rpc.call<SettingsView>("settings.get");
    setSettings(s);
    setWorkdirInput(s.workdir ?? "");
    setApprovedInput((s.approvedModels ?? []).join(", "));
    setStaleAfterInput(String(s.staleAfterMinutes ?? 30));
    return s;
  }, []);

  // Connect to the embedded daemon once the shell hands us its address.
  useEffect(() => {
    let cancelled = false;
    discover().then(async (conn: Conn) => {
      if (cancelled) return;
      const rpc = new Rpc(conn);
      try {
        await rpc.connect();
      } catch (e) {
        setStatus("offline");
        setError(e instanceof Error ? e.message : String(e));
        return;
      }
      if (cancelled) return;
      rpcRef.current = rpc;
      setStatus("online");
      // Not awaited: a slow GitHub lookup must not hold up startup.
      rpc.call<UpdateStatus>("update.check").then(setUpdate).catch(() => {});
      try {
        const [r, h, list, projs] = await Promise.all([
          rpc.call<Role[]>("roles.list"),
          rpc.call<HarnessInfo>("harness.list"),
          rpc.call<ThreadSummary[]>("thread.list"),
          rpc.call<Project[]>("project.list"),
        ]);
        setRoles(r);
        setHarnesses(h);
        setThreads(list);
        setProjects(projs);
        // Start at the projects screen; a thread is opened explicitly —
        // unless the app was launched as `agenttunnel <dir>`, which opens a
        // thread in that project right away.
        if (conn.launchDir) {
          const opened = await rpc.call<{
            threadId: string;
            projectId: string;
            projects: Project[];
          }>("thread.open", { dir: conn.launchDir });
          setProjects(opened.projects);
          setOpenProjectIds((prev) =>
            prev.includes(opened.projectId) ? prev : [...prev, opened.projectId],
          );
          lastThreadByProject.current[opened.projectId] = opened.threadId;
          setProjectId(opened.projectId);
          setActiveId(opened.threadId);
          const updated = await rpc.call<ThreadSummary[]>("thread.list");
          setThreads(updated);
        }
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      }
      // Another `agenttunnel <dir>` invocation while this window runs: the
      // launcher forwarded the thread here.
      rpc.onAppOpen(({ threadId, projectId: openedProjectId }) => {
        if (openedProjectId) {
          setOpenProjectIds((prev) =>
            prev.includes(openedProjectId) ? prev : [...prev, openedProjectId],
          );
          setProjectId(openedProjectId);
          if (threadId) lastThreadByProject.current[openedProjectId] = threadId;
        }
        if (threadId) {
          setActiveId(threadId);
          reloadThreads();
        }
        Window.Focus().catch(() => undefined);
      });
    });
    return () => {
      cancelled = true;
    };
  }, []);

  const scheduleRefresh = useCallback(
    (id: string) => {
      window.clearTimeout(debounce.current);
      debounce.current = window.setTimeout(() => {
        refresh(id).catch(() => undefined);
      }, 120);
    },
    [refresh],
  );

  // Subscribe to the active thread and keep the local view fresh. The activity
  // log box restores this thread's persisted output on switch, then appends
  // "agent.output" frames for this thread as they stream.
  useEffect(() => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId) return;
    let stale = false;
    // Frames that arrive before the snapshot lands wait here, then merge after it.
    let buffered: LiveLine[] | null = [];
    setLive([]);
    rpc.call("thread.subscribe", { threadId: activeId }).catch(() => undefined);
    // Snapshot and buffered frames are both raw chunks, so overlap is compared
    // before the chunks are merged into display lines.
    const applyBuffered = (snapshot: LiveLine[]) => {
      const fresh = dropSnapshotOverlap(snapshot, buffered ?? []);
      buffered = null;
      setLive([...snapshot, ...fresh].reduce<LiveLine[]>(pushLiveLine, []));
    };
    rpc
      .call<LogEntry[]>("thread.logs", { threadId: activeId })
      .then((entries) => {
        if (stale) return;
        applyBuffered(entries);
      })
      .catch(() => {
        if (stale) return;
        applyBuffered([]);
      });
    const off = rpc.onThreadEvent((raw) => {
      const ev = raw as {
        type?: string;
        threadId?: string;
        payload?: { agentId?: string; kind?: string; text?: string };
      };
      if (ev?.type === "agent.output" && ev.payload) {
        // Output from another thread belongs to that thread's log; it is
        // restored from the daemon when the user switches back.
        if (stale || ev.threadId !== activeId) return;
        const line = {
          agentId: String(ev.payload.agentId ?? ""),
          kind: String(ev.payload.kind ?? "text"),
          text: String(ev.payload.text ?? ""),
        };
        if (buffered) buffered.push(line);
        else setLive((prev) => pushLiveLine(prev, line));
        return;
      }
      scheduleRefresh(activeId);
    });
    refresh(activeId).catch(() => undefined);
    return () => {
      stale = true;
      off();
    };
  }, [activeId, refresh, scheduleRefresh]);

  // The diff backs both the center Review tab and the aside Diff panel.
  useEffect(() => {
    if (tab === "review" || asideActive === "diff") loadDiff(activeId);
  }, [tab, asideActive, activeId, loadDiff]);

  // The PR panel loads when shown and refreshes while visible; a minute keeps
  // CI status fresh without hammering the forge CLI.
  useEffect(() => {
    if (asideActive !== "pr") return;
    loadPR(activeId).catch(() => undefined);
    const timer = setInterval(() => loadPR(activeId).catch(() => undefined), 60_000);
    return () => clearInterval(timer);
  }, [asideActive, activeId, loadPR]);

  // The file tree loads when its panel is shown.
  useEffect(() => {
    if (asideActive === "files") loadFileTree(activeId);
  }, [asideActive, activeId, loadFileTree]);

  // Inherited context loads when its panel is opened, and stays fresh on
  // thread changes since agents can record learnings mid-turn.
  useEffect(() => {
    if (asideActive === "inherited") loadInherited();
  }, [asideActive, activeId, loadInherited]);

  // Remember the open panels across restarts.
  useEffect(() => {
    try {
      window.localStorage.setItem("at.asideTabs", JSON.stringify(asideTabs));
    } catch {
      /* private mode: stay in memory */
    }
  }, [asideTabs]);

  // Remember composer drafts across restarts; empty drafts are dropped.
  useEffect(() => {
    try {
      const kept = Object.fromEntries(Object.entries(drafts).filter(([, v]) => v !== ""));
      window.localStorage.setItem("at.drafts", JSON.stringify(kept));
    } catch {
      /* private mode: stay in memory */
    }
  }, [drafts]);

  // Remember which projects are open as tabs.
  useEffect(() => {
    try {
      window.localStorage.setItem("at.openProjects", JSON.stringify(openProjectIds));
    } catch {
      /* private mode: stay in memory */
    }
  }, [openProjectIds]);

  // Drop tabs whose project was removed elsewhere (settings, another window).
  useEffect(() => {
    if (projects.length === 0) return;
    setOpenProjectIds((prev) => {
      const next = prev.filter((id) => projects.some((p) => p.id === id));
      return next.length === prev.length ? prev : next;
    });
  }, [projects]);

  // Other projects keep working while their tab is in the background. Poll the
  // recency list so their tabs stay honest about running agents and activity.
  useEffect(() => {
    if (status !== "online") return;
    const timer = window.setInterval(() => {
      rpcRef.current
        ?.call<ThreadSummary[]>("thread.list")
        .then(setThreads)
        .catch(() => undefined);
    }, 4000);
    return () => window.clearInterval(timer);
  }, [status]);

  // Single-letter panel shortcuts (shown in the "+" menu), inactive while typing.
  useEffect(() => {
    if (!activeId) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || e.isComposing) return;
      const t = e.target;
      if (
        t instanceof HTMLInputElement ||
        t instanceof HTMLTextAreaElement ||
        t instanceof HTMLSelectElement
      ) {
        return;
      }
      if (t instanceof HTMLElement && t.isContentEditable) return;
      const id = (Object.keys(ASIDE_META) as AsideTabId[]).find(
        (k) => ASIDE_META[k].key.toLowerCase() === e.key.toLowerCase(),
      );
      if (id) openPanel(id);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [activeId, openPanel]);

  // While an agent is open in the detail panel, poll its log.
  useEffect(() => {
    const rpc = rpcRef.current;
    if (!rpc || !detailId || !activeId) return;
    const load = async () => {
      const l = await rpc
        .call<LogEntry[]>("agents.logs", { threadId: activeId, agentId: detailId })
        .catch(() => null);
      if (l) setLogs(l);
    };
    load();
    const timer = window.setInterval(load, 800);
    return () => window.clearInterval(timer);
  }, [detailId, activeId]);

  const rolesById = useMemo(() => {
    const map: Record<string, string> = {};
    for (const r of roles) map[r.id] = r.name;
    return map;
  }, [roles]);

  const agents = useMemo<Agent[]>(() => {
    const out: Agent[] = [];
    if (thread?.agentOrder) {
      for (const id of thread.agentOrder) {
        const a = thread.agents?.[id];
        if (a) out.push(a);
      }
    }
    return out;
  }, [thread]);

  const threadUsage = useMemo(() => deriveThreadUsage(thread), [thread]);
  const sharedEntries = useMemo(
    () => Object.entries(thread?.shared ?? {}).sort(([a], [b]) => a.localeCompare(b)),
    [thread],
  );
  const turns = thread?.turns ?? [];
  const totals = useMemo(() => {
    const all = addUsage(threadUsage.orchestrator, threadUsage.subagents);
    return {
      total: totalTokens(all),
      orchestrator: totalTokens(threadUsage.orchestrator),
      subagents: totalTokens(threadUsage.subagents),
      input: all.inputTokens,
      output: all.outputTokens,
      cached: all.cachedTokens,
      reasoning: all.reasoningTokens ?? 0,
      processed: all.inputTokens + all.outputTokens + all.cachedTokens,
      cost: all.costUsd,
    };
  }, [threadUsage]);
  // Agents grouped into their pipelines, the way the orchestrator dispatched them.
  const lanes = useMemo(() => {
    const order: string[] = [];
    const grouped: Record<string, Agent[]> = {};
    for (const a of agents) {
      const lane = a.pipelineId && a.pipelineId.length > 0 ? a.pipelineId : "work";
      if (!grouped[lane]) {
        grouped[lane] = [];
        order.push(lane);
      }
      grouped[lane].push(a);
    }
    return order.map((name) => {
      const members = grouped[name];
      return {
        name,
        agents: members,
        done: members.filter((x) => x.status !== "running" && x.status !== "pending").length,
      };
    });
  }, [agents]);
  const settled = agents.filter((a) => a.status !== "running" && a.status !== "pending").length;
  const detailAgent = agents.find((a) => a.id === detailId) ?? null;
  const comments = thread?.reviewComments ?? [];
  const tasks = thread?.tasks ?? [];
  const running = useMemo(() => agents.filter((a) => a.status === "running"), [agents]);
  const selectedProject = projects.find((p) => p.id === projectId) ?? null;
  const [threadFilter, setThreadFilter] = useState("");
  const needle = threadFilter.trim().toLowerCase();
  const projectThreads = useMemo(
    () =>
      threads.filter(
        (t) =>
          t.projectId === projectId && (needle === "" || t.title.toLowerCase().includes(needle)),
      ),
    [threads, projectId, needle],
  );
  const openProjects = useMemo(
    () =>
      openProjectIds
        .map((id) => projects.find((p) => p.id === id))
        .filter((p): p is Project => !!p),
    [openProjectIds, projects],
  );
  const runningByProject = useMemo(() => {
    const map: Record<string, number> = {};
    for (const t of threads) {
      if (t.projectId && t.runningAgents) map[t.projectId] = (map[t.projectId] ?? 0) + t.runningAgents;
    }
    return map;
  }, [threads]);
  const enabledHarnesses = harnesses.enabled ?? harnesses.all;
  const isHarnessEnabled = (h: string) => enabledHarnesses.includes(h);

  // Roles only offer enabled harnesses. A role already pointing at a disabled
  // harness keeps it as an option so its current value stays visible.
  const harnessOptionsFor = (current: string): DropdownOption[] =>
    harnesses.all
      .filter((h) => isHarnessEnabled(h) || h === current)
      .map((h) => ({
        value: h,
        label: isHarnessEnabled(h) ? h : `${h} (disabled)`,
      }));

  const modelOptions = (harness: string, current: string): DropdownOption[] => {
    const opts: DropdownOption[] = [{ value: "", label: "model (default)", group: "Default" }];
    const list = harnesses.models?.[harness] ?? [];
    const merged = current && !list.includes(current) ? [current, ...list] : list;
    for (const m of merged) {
      const slash = m.indexOf("/");
      opts.push({ value: m, label: m, group: slash > 0 ? m.slice(0, slash) : "Other" });
    }
    return opts;
  };

  const newThread = async () => {
    const rpc = rpcRef.current;
    if (!rpc || !projectId) return;
    // No title: the server names it, and the orchestrator model renames it after
    // the first exchange.
    const created = await rpc.call<{ threadId: string }>("thread.create", { projectId });
    const list = await rpc.call<ThreadSummary[]>("thread.list");
    setThreads(list);
    selectThread(created.threadId);
  };

  const send = async () => {
    const rpc = rpcRef.current;
    const text = input.trim();
    if (!rpc || !activeId || !text) return;
    // "! cmd" runs a shell command in the thread's project. A lone "!" is just text.
    const command = text.startsWith("!") ? text.slice(1).trim() : "";
    // While a turn is running, a message steers the orchestrator's next pass
    // rather than starting a competing turn.
    const steer = running.length > 0;
    if (command && steer) {
      setError("Stop the running turn before running a shell command.");
      return;
    }
    setInput("");
    setBusy(true);
    if (command) setShellThread(activeId);
    try {
      if (command) {
        await rpc.call("thread.runCommand", { threadId: activeId, command });
      } else {
        await rpc.call(steer ? "thread.steer" : "thread.turn.start", { threadId: activeId, text });
      }
    } catch (e) {
      // Put the typed text back so a failed send does not lose it.
      setInput(input);
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
      setShellThread(null);
      refresh(activeId).catch(() => undefined);
    }
  };

  const toggle = async (field: "autoMode" | "peerMailbox", value: boolean) => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId) return;
    const method = field === "autoMode" ? "thread.setAutoMode" : "thread.setPeerMailbox";
    await rpc.call(method, { threadId: activeId, enabled: value });
    refresh(activeId).catch(() => undefined);
  };

  /** Sets how many orchestrators lead the active thread's turns from the next turn on. */
  const setLeads = async (count: number) => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId) return;
    try {
      await rpc.call("thread.setLeads", { threadId: activeId, count });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
    refresh(activeId).catch(() => undefined);
  };

  /** Stops the turn running on the active thread, if any. */
  const stopTurn = async () => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId) return;
    try {
      await rpc.call("thread.turn.stop", { threadId: activeId });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
    refresh(activeId).catch(() => undefined);
  };

  /** Stops a single agent; its turn-mates keep running. */
  const stopAgent = async (agentId: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      await rpc.call("agents.stop", { agentId });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
    if (activeId) refresh(activeId).catch(() => undefined);
  };

  /** Opens the council sheet, seeding the topic from the composer draft. */
  const openCouncil = () => {
    if (input.trim()) setCouncilTopic(input.trim());
    setCouncilOpen(true);
  };

  const updateCouncilMember = (idx: number, patch: Partial<CouncilMemberRow>) => {
    setCouncilMembers((prev) => prev.map((m, i) => (i === idx ? { ...m, ...patch } : m)));
  };

  /** Convenes the council: members debate in parallel rounds, then a
   *  facilitator synthesizes the outcome into the thread. */
  const startCouncil = async () => {
    const rpc = rpcRef.current;
    const topic = councilTopic.trim();
    if (!rpc || !activeId || !topic) return;
    setCouncilOpen(false);
    setInput("");
    setBusy(true);
    try {
      await rpc.call("thread.council.start", {
        threadId: activeId,
        topic,
        rounds: councilRounds,
        members: councilMembers.map((m) => ({
          name: (m.name || m.role).trim() || m.role,
          role: m.role,
          harness: m.harness,
          model: m.model,
          perspective: m.perspective,
        })),
      });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
      refresh(activeId).catch(() => undefined);
    }
  };

  const addComment = async () => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId || !draft || !draft.body.trim()) return;
    await rpc.call("review.comment", {
      threadId: activeId,
      file: draft.file,
      line: draft.line,
      body: draft.body,
    });
    setDraft(null);
    refresh(activeId).catch(() => undefined);
  };

  const toggleModelConfig = () => {
    // Role config lives in settings, which is only fetched on demand — load it
    // the first time the composer panel opens, same trio the settings sheet uses.
    if (!modelConfigOpen && !settings) {
      (async () => {
        try {
          const [s] = await Promise.all([loadSettings(), loadHarnesses()]);
          const used = Array.from(new Set((s?.roles ?? []).map((r) => r.harness)));
          await Promise.all(used.map((h) => refreshModels(h)));
        } catch (e) {
          setError(e instanceof Error ? e.message : String(e));
        }
      })();
    }
    setModelConfigOpen((v) => !v);
  };

  const openSettings = () => {
    setSettingsOpen(true);
    (async () => {
      try {
        const [s] = await Promise.all([loadSettings(), loadHarnesses(), loadProjects()]);
        const used = Array.from(new Set((s?.roles ?? []).map((r) => r.harness)));
        await Promise.all(used.map((h) => refreshModels(h)));
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      }
    })();
  };

  const flashSaved = () => {
    setSaved(true);
    window.setTimeout(() => setSaved(false), 1500);
  };

  const saveEnv = async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    const s = await rpc.call<SettingsView>("settings.set", {
      workdir: workdirInput,
      autoMode: settings?.autoMode ?? false,
      approvedModels: approvedInput
        .split(",")
        .map((x) => x.trim())
        .filter(Boolean),
      staleAfterMinutes: staleAfterInput,
    });
    setSettings(s);
    flashSaved();
  };

  const toggleHarness = async (id: string, enabled: boolean) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    await rpc.call("settings.set", { harness: { id, enabled } });
    await loadHarnesses();
    flashSaved();
  };

  const updateRole = (id: string, patch: Partial<RoleConfigView>) => {
    setSettings((prev) =>
      prev
        ? { ...prev, roles: prev.roles.map((r) => (r.id === id ? { ...r, ...patch } : r)) }
        : prev,
    );
  };

  const persistRole = async (row: RoleConfigView) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    const s = await rpc.call<SettingsView>("settings.set", {
      role: { id: row.id, harness: row.harness, model: row.model, variant: row.variant },
    });
    setSettings(s);
    flashSaved();
  };

  if (status !== "online") {
    return (
      <div className="splash">
        <img src="/brand-mark.svg" alt="" />
        <div className="mark">AgentTunnel</div>
        <div className="splash-sub">
          {status === "connecting" ? "Starting the daemon…" : "Daemon unavailable"}
        </div>
        {error && <div className="err-text">{error}</div>}
      </div>
    );
  }

  const titlebar = (
    <div className="titlebar">
      <div className="brand">
        <img src="/brand-mark.svg" alt="" />
        <span>
          Agent<b>Tunnel</b>
        </span>
      </div>
      <div className={`conn ${status}`}>
        <span className="led" />
        <span>
          {enabledHarnesses.length} harness
          {enabledHarnesses.length === 1 ? "" : "es"} · {roles.length} roles
        </span>
      </div>
      <UpdateChip
        status={update}
        busy={updateBusy}
        error={updateError}
        onInstall={installUpdate}
      />
    </div>
  );

  // Open projects live in a persistent tab strip, so switching projects is one
  // click and never loses what the other project was doing.
  const projectTabs =
    openProjects.length > 0 ? (
      <div className="project-tabs">
        <div
          className={`ptab pt-home${projectId ? "" : " on"}`}
          {...tabProps(projectId === null, showProjects)}
          title="All projects"
        >
          <svg
            viewBox="0 0 24 24"
            width="13"
            height="13"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.8"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
          >
            <rect x="3" y="3" width="7" height="7" rx="1.5" />
            <rect x="14" y="3" width="7" height="7" rx="1.5" />
            <rect x="3" y="14" width="7" height="7" rx="1.5" />
            <rect x="14" y="14" width="7" height="7" rx="1.5" />
          </svg>
          Projects
        </div>
        <div className="pt-scroll" role="tablist" aria-label="Open projects">
          {openProjects.map((p) => {
            const busy = runningByProject[p.id] ?? 0;
            return (
              <div
                key={p.id}
                className={`ptab${p.id === projectId ? " on" : ""}`}
                {...tabProps(p.id === projectId, () => openProject(p.id))}
                title={p.path || p.name}
              >
                <span className={`pt-led${busy > 0 ? " run" : ""}`} />
                <span className="pt-name">{p.name}</span>
                {busy > 0 && (
                  <span className="pt-count" title={`${busy} running agent${busy === 1 ? "" : "s"}`}>
                    {busy}
                  </span>
                )}
                <span
                  className="pt-x"
                  role="button"
                  tabIndex={0}
                  title={`Close ${p.name}`}
                  aria-label={`Close ${p.name}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    closeProjectTab(p.id);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      e.stopPropagation();
                      closeProjectTab(p.id);
                    }
                  }}
                >
                  ×
                </span>
              </div>
            );
          })}
        </div>
        <button className="pt-add" onClick={openPicker} title="Add project" aria-label="Add project">
          +
        </button>
      </div>
    ) : null;

  const menuItems: MenuItem[] = threadMenu
    ? [
        { label: "Rename…", onSelect: () => beginRename(threadMenu.thread) },
        {
          label: "Usage…",
          onSelect: () => setInfoTarget({ id: threadMenu.thread.id, tab: "usage" }),
        },
        {
          label: "Subagents…",
          onSelect: () => setInfoTarget({ id: threadMenu.thread.id, tab: "agents" }),
        },
        { label: "Delete…", danger: true, onSelect: () => setDeleteTarget(threadMenu.thread) },
      ]
    : [];

  // Rendered in every screen that shows threads (the thread list and the chat
  // sidebar), since App returns early per screen.
  const threadOverlays = (
    <>
      {threadMenu && (
        <ContextMenu
          x={threadMenu.x}
          y={threadMenu.y}
          items={menuItems}
          onClose={() => setThreadMenu(null)}
        />
      )}
      {renameTarget && (
        <div className="overlay" onClick={() => setRenameTarget(null)}>
          <div className="sheet" onClick={(e) => e.stopPropagation()}>
            <div className="sheet-head">
              <strong>Rename thread</strong>
              <button onClick={() => setRenameTarget(null)}>Cancel</button>
            </div>
            <div className="sheet-body">
              <input
                autoFocus
                value={renameValue}
                placeholder="Thread title"
                onChange={(e) => setRenameValue(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") confirmRename().catch(() => undefined);
                }}
              />
              <div className="sheet-actions">
                <button className="primary" onClick={() => confirmRename().catch(() => undefined)}>
                  Rename
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
      {deleteTarget && (
        <div className="overlay" onClick={() => setDeleteTarget(null)}>
          <div className="sheet" onClick={(e) => e.stopPropagation()}>
            <div className="sheet-head">
              <strong>Delete thread</strong>
              <button onClick={() => setDeleteTarget(null)}>Cancel</button>
            </div>
            <div className="sheet-body">
              <p className="hint">
                Delete “{deleteTarget.title}”? Its history is kept on disk, but the thread
                disappears from the app and cannot be reopened.
              </p>
              <div className="sheet-actions">
                <button
                  className="danger"
                  onClick={() => confirmDeleteThread().catch(() => undefined)}
                >
                  Delete thread
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
      {infoTarget && (
        <ThreadInfoDialog
          thread={infoState.thread}
          loading={infoState.loading}
          tab={infoTarget.tab}
          onTab={(tab) => setInfoTarget({ ...infoTarget, tab })}
          onClose={() => setInfoTarget(null)}
        />
      )}
    </>
  );

  // Review comments belong with the diff, not pinned under every aside tab.
  // The same block renders in the center Review tab and the aside Diff panel.
  const reviewComments = (
    <div className="section">
      <h3>
        Review comments <span>{comments.length}</span>
      </h3>
      {comments.length === 0 && (
        <p className="hint">None yet. Comment on a line in Review or the Diff panel.</p>
      )}
      {comments.map((c) => (
        <div className="comment-item" key={c.id}>
          <div className="comment-file">
            {c.file}:{c.line}
          </div>
          <div className="comment-body">{c.body}</div>
        </div>
      ))}
    </div>
  );

  // Screen 1 — the projects configured in this environment.
  if (!projectId) {
    return (
      <div className="screen">
        {titlebar}
        {projectTabs}
        <div className="screen-body">
          <div className="screen-head">
            <div className="screen-title">
              <h2>Projects</h2>
              <p className="screen-sub">
                {projects.length === 0
                  ? "No projects yet"
                  : `${projects.length} project${projects.length === 1 ? "" : "s"}`}
              </p>
            </div>
            <button className="primary" onClick={openPicker}>
              New project
            </button>
          </div>
          {showNewProject && (
            <div className="new-project">
              <input
                placeholder="Name"
                value={newProjectName}
                onChange={(e) => setNewProjectName(e.target.value)}
              />
              <input
                placeholder="/path/to/repo"
                value={newProjectPath}
                onChange={(e) => setNewProjectPath(e.target.value)}
              />
              <button onClick={openPicker}>Browse…</button>
              <button className="primary" onClick={createProject}>
                Add project
              </button>
            </div>
          )}
          {projects.length === 0 && !showNewProject && (
            <div className="empty">No projects yet. Add one to get started.</div>
          )}
          <div className="project-grid">
            {projects.map((p) => (
              <div
                className={`project-card${openProjectIds.includes(p.id) ? " is-open" : ""}`}
                key={p.id}
                {...clickable(() => openProject(p.id))}
              >
                <div className="pc-name" title={p.name}>
                  {p.name}
                </div>
                <div className="pc-path" title={p.path || "No path set"}>
                  {p.path || "No path set"}
                </div>
                <div className="pc-meta">
                  <span>
                    {p.threads} thread{p.threads === 1 ? "" : "s"}
                  </span>
                  <span className="pc-arrow" aria-hidden="true">
                    →
                  </span>
                </div>
                <button
                  className="pc-del"
                  title="Remove project"
                  aria-label={`Remove ${p.name}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    deleteProject(p.id).catch(() => undefined);
                  }}
                >
                  ×
                </button>
              </div>
            ))}
          </div>
        </div>
      </div>
    );
  }

  // Screen 2 — the threads of the selected project.
  if (!activeId) {
    return (
      <div className="screen">
        {titlebar}
        {projectTabs}
        <div className="screen-body">
          <div className="screen-head">
            <button className="back" onClick={showProjects}>
              ← Projects
            </button>
            <div className="screen-title">
              <h2>{selectedProject?.name ?? "Threads"}</h2>
              {selectedProject?.path && <p className="screen-sub">{selectedProject.path}</p>}
            </div>
            <div className="screen-actions">
              <input
                className="thread-filter"
                placeholder="Filter threads…"
                value={threadFilter}
                onChange={(e) => setThreadFilter(e.target.value)}
              />
              <button className="primary" onClick={newThread}>
                New thread
              </button>
            </div>
          </div>
          {projectThreads.length === 0 && (
            <div className="empty">
              {needle
                ? "No threads match that filter."
                : "No threads yet. Start one for this project."}
            </div>
          )}
          <div className="thread-cards">
            {projectThreads.map((t) => (
              <div
                className={`thread-card${t.stale ? " is-stale" : ""}`}
                key={t.id}
                {...clickable(() => selectThread(t.id))}
                onContextMenu={(e) => openThreadMenu(t, e)}
              >
                <div className="tc-head">
                  <div className="tc-title" title={t.title}>
                    {t.title}
                  </div>
                </div>
                <div className="tc-meta">
                  {t.messages} messages · {t.agents} agents{t.autoMode ? " · auto" : ""}
                </div>
                <div className="tc-foot">
                  <ThreadClock t={t} />
                  {fmtUpdated(t.updatedAt) && (
                    <span className="tc-updated">Updated {fmtUpdated(t.updatedAt)}</span>
                  )}
                </div>
              </div>
            ))}
          </div>
          {threadOverlays}
        </div>
      </div>
    );
  }

  return (
    <div className="app">
      {titlebar}
      {projectTabs}

      <div className="col sidebar">
        <div className="col-head">
          <button className="back-mini" onClick={() => selectThread(null)}>
            ← {selectedProject?.name ?? "Threads"}
          </button>
          <button onClick={newThread}>New</button>
        </div>
        <div className="col-body">
          {projectThreads.length === 0 && <div className="empty">No threads yet</div>}
          {projectThreads.map((t) => (
            <div
              key={t.id}
              className={`thread-row ${t.id === activeId ? "active" : ""} ${t.stale ? "stale" : ""}`}
              {...clickable(() => selectThread(t.id))}
              onContextMenu={(e) => openThreadMenu(t, e)}
            >
              <div className="t" title={t.title}>
                {t.title}
              </div>
              <div className="m">
                {t.messages} msgs · {t.agents} agents{t.autoMode ? " · auto" : ""}
              </div>
              <div className="thread-row-foot">
                <ThreadClock t={t} />
                {fmtUpdated(t.updatedAt) && <span className="u">{fmtUpdated(t.updatedAt)}</span>}
              </div>
            </div>
          ))}
        </div>
        <div className="sidebar-foot">
          <button className="foot-btn" onClick={openSettings}>
            <svg
              viewBox="0 0 24 24"
              width="15"
              height="15"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.8"
              strokeLinecap="round"
              strokeLinejoin="round"
              aria-hidden="true"
            >
              <circle cx="12" cy="12" r="3" />
              <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" />
            </svg>
            Settings
          </button>
        </div>
      </div>

      <div className="col">
        <div className="chat-head">
          <div className="tabs" role="tablist" aria-label="Thread view">
            <div
              className={`tab ${tab === "chat" ? "on" : ""}`}
              {...tabProps(tab === "chat", () => setTab("chat"))}
            >
              Chat
            </div>
            <div
              className={`tab ${tab === "review" ? "on" : ""}`}
              {...tabProps(tab === "review", () => setTab("review"))}
            >
              Review
              {comments.length > 0 && <span className="pill">{comments.length}</span>}
            </div>
            <div
              className={`tab ${tab === "graph" ? "on" : ""}`}
              {...tabProps(tab === "graph", () => setTab("graph"))}
            >
              Graph
              {(thread?.mailbox?.length ?? 0) > 0 && (
                <span className="pill">{thread?.mailbox?.length}</span>
              )}
            </div>
            <div
              className={`tab ${tab === "board" ? "on" : ""}`}
              {...tabProps(tab === "board", () => setTab("board"))}
            >
              Board
              {agents.some((a) => a.role !== "orchestrator") && (
                <span className="pill">{agents.filter((a) => a.role !== "orchestrator").length}</span>
              )}
            </div>
          </div>
          <div className="toggles">
            <select
              className="team-size"
              aria-label="Orchestrator team size"
              title="Leads that plan and dispatch together on the next turn"
              value={thread?.leads && thread.leads > 0 ? thread.leads : 1}
              disabled={!thread}
              onChange={(e) => setLeads(Number(e.target.value))}
            >
              <option value={1}>1 lead</option>
              <option value={2}>2 leads</option>
              <option value={3}>3 leads</option>
            </select>
            <div
              className={`toggle ${thread?.peerMailbox ? "on" : ""}`}
              {...switchProps(!!thread?.peerMailbox, () =>
                thread && toggle("peerMailbox", !thread.peerMailbox),
              )}
              aria-label="Let subagents message each other"
            >
              <span className="sw" /> Peer chat
            </div>
          </div>
        </div>

        {tab === "chat" ? (
          <>
            <div className="stream" ref={streamRef}>
              {(!thread || (thread.messages?.length ?? 0) === 0) && running.length === 0 && (
                <div className="stream-empty">
                  <img src="/brand-mark.svg" alt="" />
                  <div className="stream-empty-title">Start a thread</div>
                  <div className="stream-empty-sub">
                    Describe the task. The orchestrator will plan it and dispatch your team.
                  </div>
                </div>
              )}
              {(thread?.messages ?? []).map((m) => (
                <div
                  key={m.id}
                  className={`msg ${m.author === "user" ? "user" : m.author === "steering" ? "steering" : m.author === "shell" ? "shell" : ""}`}
                >
                  <div className="who">
                    <span className="role">
                      {m.author === "user"
                        ? "You"
                        : m.author === "steering"
                          ? "Steering"
                          : m.author === "system"
                            ? "System"
                            : m.author === "shell"
                              ? "Shell"
                              : "Orchestrator"}
                    </span>
                  </div>
                  <div className="body">
                    <Markdown text={m.text} />
                  </div>
                </div>
              ))}
              {running.length > 0 && (
                <div className="activity">
                  <div className="act-head">
                    <span className="act-led" />
                    <span className="act-label">
                      Working —{" "}
                      {running.map((a) => a.name || rolesById[a.role] || a.role).join(", ")}
                    </span>
                    <Elapsed since={running[0]?.startedAt} />
                    <button
                      className="stop-btn"
                      onClick={stopTurn}
                      title="Stop this turn — running agents are cancelled"
                    >
                      Stop
                    </button>
                  </div>
                  <div className="act-body">
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
                  </div>
                </div>
              )}
              {shellThread === activeId && shellThread !== null && (
                <div className="activity">
                  <div className="act-head">
                    <span className="act-led" />
                    <span className="act-label">Running shell command</span>
                    <button className="stop-btn" onClick={stopTurn} title="Stop this command">
                      Stop
                    </button>
                  </div>
                </div>
              )}
              {error && <div className="empty err-text">{error}</div>}
            </div>

            <div className={commandMode ? "composer command-mode" : "composer"}>
              <div className="box">
                <textarea
                  rows={1}
                  placeholder={
                    running.length > 0
                      ? "Steer the orchestrator — it lands on the next pass…"
                      : "Ask the team to build something, or ! to run a shell command…"
                  }
                  value={input}
                  onChange={(e) => setInput(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && !e.shiftKey) {
                      e.preventDefault();
                      send();
                    }
                  }}
                />
                <MicButton
                  onText={(t) => setInput(t)}
                  disabled={!activeId}
                  onUnavailable={(msg) => setError(msg)}
                />
                <button className="primary" onClick={send} disabled={busy || !activeId}>
                  {running.length > 0 ? "Steer" : "Send"}
                </button>
              </div>
              <div className="composer-foot">
                <button
                  className={`model-btn${modelConfigOpen ? " open" : ""}`}
                  onClick={toggleModelConfig}
                  aria-expanded={modelConfigOpen}
                  title="Pick the harness and model each role runs on"
                >
                  <svg
                    viewBox="0 0 24 24"
                    width="14"
                    height="14"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="1.8"
                    strokeLinecap="round"
                    aria-hidden="true"
                  >
                    <line x1="4" y1="7" x2="20" y2="7" />
                    <circle cx="9" cy="7" r="2.2" fill="var(--panel)" />
                    <line x1="4" y1="17" x2="20" y2="17" />
                    <circle cx="15" cy="17" r="2.2" fill="var(--panel)" />
                  </svg>
                  Model config
                </button>
                <button
                  className="model-btn"
                  onClick={openCouncil}
                  title="Convene a council: several agents debate one question in parallel rounds and reach a synthesized outcome"
                >
                  <svg
                    viewBox="0 0 24 24"
                    width="14"
                    height="14"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="1.8"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    aria-hidden="true"
                  >
                    <circle cx="12" cy="7" r="2.4" />
                    <circle cx="5" cy="17" r="2.4" />
                    <circle cx="19" cy="17" r="2.4" />
                    <path d="M10.2 8.9 6.7 14.8M13.8 8.9l3.5 5.9M7.4 17h9.2" />
                  </svg>
                  Council
                </button>
                {settings && (
                  <div className="model-summary">
                    {thread?.autoMode && <span className="ms-auto">auto</span>}
                    {settings.roles.map((r) => (
                      <span key={r.id}>
                        {r.name} <b>{r.model || "default"}</b> {r.harness}
                      </span>
                    ))}
                  </div>
                )}
              </div>
              {modelConfigOpen && (
                <div className="model-panel">
                  {!settings && <p className="hint">Loading roles…</p>}
                  {settings && (
                    <>
                      <div
                        className={`toggle${thread?.autoMode ? " on" : ""}`}
                        {...switchProps(!!thread?.autoMode, () =>
                          thread && toggle("autoMode", !thread.autoMode),
                        )}
                        title="Auto mode: the orchestrator picks each task's model and harness"
                      >
                        <span className="sw" /> Auto mode
                      </div>
                      <p className="hint">
                        {thread?.autoMode
                          ? "Auto mode is on — the orchestrator picks each task's model and harness. Other roles are parked."
                          : "Each role runs on its own harness and model. Changes apply to the next turn."}
                      </p>
                      {settings.roles.map((r) => {
                        const parked = !!thread?.autoMode && r.id !== "orchestrator";
                        return (
                          <div
                            className={`role-edit${parked ? " parked" : ""}`}
                            key={r.id}
                            title={
                              parked
                                ? "Parked while auto mode is on — the orchestrator decides this role's runtime"
                                : undefined
                            }
                          >
                            <span className="rn">{r.name}</span>
                            <Dropdown
                              value={r.harness}
                              options={harnessOptionsFor(r.harness)}
                              disabled={parked}
                              onChange={(v) => {
                                const row = { ...r, harness: v };
                                updateRole(r.id, { harness: v });
                                persistRole(row).catch(() => undefined);
                                refreshModels(v).catch(() => undefined);
                              }}
                            />
                            <Dropdown
                              value={r.model}
                              options={modelOptions(r.harness, r.model)}
                              searchable
                              placeholder="model (default)"
                              disabled={parked}
                              onRefresh={() => refreshModels(r.harness).catch(() => undefined)}
                              addLabel="Add model"
                              onAdd={(m) => {
                                addModel(r.harness, m)
                                  .then(() => {
                                    const row = { ...r, model: m };
                                    updateRole(r.id, { model: m });
                                    persistRole(row).catch(() => undefined);
                                  })
                                  .catch(() => undefined);
                              }}
                              onChange={(v) => {
                                const row = { ...r, model: v };
                                updateRole(r.id, { model: v });
                                persistRole(row).catch(() => undefined);
                              }}
                            />
                          </div>
                        );
                      })}
                    </>
                  )}
                </div>
              )}
            </div>
          </>
        ) : tab === "review" ? (
          <div className="stream">
            {diffError && <div className="empty err-text">{diffError}</div>}
            <DiffView
              diff={diff}
              idPrefix="review-"
              title={thread?.title ?? "Working tree"}
              comments={comments}
              draft={draft}
              emptyText="No changes in the working tree."
              onLineClick={(file, line) => setDraft({ file, line, body: "" })}
              onDraftBody={(body) => setDraft((d) => (d ? { ...d, body } : d))}
              onAddComment={() => addComment().catch(() => undefined)}
              onCancelDraft={() => setDraft(null)}
              onRefresh={() => loadDiff(activeId)}
            />
            {reviewComments}
          </div>
        ) : tab === "board" ? (
          <div className="board-view">
            {tasks.length > 0 && <TaskList tasks={tasks} />}
            <KanbanBoard agents={agents} roleNames={rolesById} onOpenAgent={setDetailId} />
          </div>
        ) : (
          <AgentGraph
            key={activeId ?? ""}
            rpc={rpcRef.current}
            threadId={activeId ?? ""}
            agents={agents}
            roleNames={rolesById}
            usage={thread?.usage ?? null}
            mailbox={thread?.mailbox ?? null}
            peerMailbox={thread?.peerMailbox ?? false}
            onOpenAgent={setDetailId}
          />
        )}
      </div>

      <div className="col aside">
        <div className="aside-tabs" role="tablist" aria-label="Thread details">
          {asideTabs.map((id) => (
            <div
              key={id}
              className={`tab ${asideActive === id ? "on" : ""}`}
              {...tabProps(asideActive === id, () => setAsideActive(id))}
            >
              {id === "agents" ? (
                <>
                  Agents <span className="pill">{agents.length}</span>
                </>
              ) : (
                ASIDE_META[id].label
              )}
              {!ASIDE_PINNED.includes(id) && (
                <span
                  className="tab-x"
                  role="button"
                  tabIndex={0}
                  title={`Close ${ASIDE_META[id].label}`}
                  aria-label={`Close ${ASIDE_META[id].label}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    closePanel(id);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      e.stopPropagation();
                      closePanel(id);
                    }
                  }}
                >
                  ×
                </span>
              )}
            </div>
          ))}
          <div
            className="tab tab-add"
            role="button"
            tabIndex={0}
            aria-label="Add panel"
            title="Add panel"
            onClick={(e) => {
              const r = e.currentTarget.getBoundingClientRect();
              setPanelMenu({ x: r.left, y: r.bottom + 4 });
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                const r = e.currentTarget.getBoundingClientRect();
                setPanelMenu({ x: r.left, y: r.bottom + 4 });
              }
            }}
          >
            +
          </div>
        </div>
        {panelMenu && (
          <ContextMenu
            x={panelMenu.x}
            y={panelMenu.y}
            items={(Object.keys(ASIDE_META) as AsideTabId[]).map((id) => ({
              label: ASIDE_META[id].label,
              hint: ASIDE_META[id].key,
              onSelect: () => openPanel(id),
            }))}
            onClose={() => setPanelMenu(null)}
          />
        )}

        <div className="aside-body">
          {asideActive === "agents" ? (
            <>
              <div className="section">
                <h3>
                  Workflow{" "}
                  <span>
                    {settled}/{agents.length} settled
                  </span>
                </h3>
                {lanes.length === 0 && <p className="hint">No agents yet.</p>}
                {lanes.map((lane) => (
                  <div className="lane" key={lane.name}>
                    <div className="lane-head">
                      <span className="lane-name">{lane.name}</span>
                      <span className="lane-count">
                        {lane.done}/{lane.agents.length}
                      </span>
                    </div>
                    {lane.agents.map((a) => {
                      const entry = thread?.usage?.[a.id];
                      return (
                        <div className="wf-row" key={a.id} {...clickable(() => setDetailId(a.id))}>
                          <span className={`dot ${a.status}`} />
                          <div className="wf-main">
                            <div className="wf-name">{a.name || rolesById[a.role] || a.role}</div>
                            <div className="wf-task">{a.task}</div>
                          </div>
                          <div className="wf-side">
                            <div className="wf-role">{a.role}</div>
                            <div className="wf-meta">
                              {entry ? `${fmtTokens(totalTokens(entry.usage))} tok` : a.status}
                            </div>
                            {a.status === "running" && (
                              <button
                                className="stop-btn mini"
                                title="Stop this agent — the rest of the turn keeps running"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  stopAgent(a.id);
                                }}
                              >
                                Stop
                              </button>
                            )}
                          </div>
                        </div>
                      );
                    })}
                  </div>
                ))}
                <div className="wf-foot">
                  <span>{settled} settled</span>
                  <span>Σ {fmtTokens(totals.total)} tok</span>
                </div>
              </div>
              <div className="section">
                <h3>Roles</h3>
                <ul className="roles">
                  {roles.map((r) => (
                    <li key={r.id}>
                      <span>{r.name}</span>
                      <span className="harness">{r.defaultHarness}</span>
                    </li>
                  ))}
                </ul>
              </div>
            </>
          ) : asideActive === "usage" ? (
          <>
            <div className="section">
              <h3>Token usage</h3>
              <div className="usage-grid">
                <div className="stat">
                  <div className="k">Total tokens</div>
                  <div className="v">{fmtTokens(totals.total)}</div>
                </div>
                <div className="stat">
                  <div className="k">Orchestrator tokens</div>
                  <div className="v">{fmtTokens(totals.orchestrator)}</div>
                </div>
                <div className="stat">
                  <div className="k">Subagent tokens</div>
                  <div className="v">{fmtTokens(totals.subagents)}</div>
                </div>
                <div className="stat">
                  <div className="k">Input tokens</div>
                  <div className="v">{fmtTokens(totals.input)}</div>
                </div>
                <div className="stat">
                  <div className="k">Output tokens</div>
                  <div className="v">{fmtTokens(totals.output)}</div>
                </div>
                <div className="stat">
                  <div className="k">Cached input</div>
                  <div className="v">{fmtTokens(totals.cached)}</div>
                </div>
                <div className="stat">
                  <div className="k">Reasoning output</div>
                  <div className="v">{fmtTokens(totals.reasoning)}</div>
                </div>
                <div className="stat">
                  <div className="k">Processed</div>
                  <div className="v">{fmtTokens(totals.processed)}</div>
                </div>
                <div className="stat">
                  <div className="k">Cost</div>
                  <div className="v">{fmtCost(totals.cost)}</div>
                </div>
                <div className="stat">
                  <div className="k">Agents</div>
                  <div className="v">{agents.length}</div>
                </div>
              </div>
            </div>

            <div className="section">
              <h3>By turn</h3>
              {turns.length === 0 ? (
                <p className="hint">No turns yet.</p>
              ) : (
                <div className="md-table-wrap">
                  <table className="grid">
                    <thead>
                      <tr>
                        <th>Turn</th>
                        <th>When</th>
                        <th>In</th>
                        <th>Out</th>
                        <th>Sub</th>
                        <th>Total</th>
                      </tr>
                    </thead>
                    <tbody>
                      {turns.map((t) => {
                        const usage = t.agents ?? {};
                        let inTok = 0;
                        let outTok = 0;
                        let subTok = 0;
                        for (const [id, u] of Object.entries(usage)) {
                          inTok += u.inputTokens;
                          outTok += u.outputTokens;
                          const owner = thread?.agents?.[id];
                          if (!owner || owner.parentAgentId) {
                            subTok += u.inputTokens + u.outputTokens;
                          }
                        }
                        return (
                          <tr key={t.turn}>
                            <td>#{t.turn}</td>
                            <td>
                              {t.at
                                ? new Date(t.at).toLocaleTimeString([], {
                                    hour: "2-digit",
                                    minute: "2-digit",
                                  })
                                : ""}
                            </td>
                            <td>{fmtTokens(inTok)}</td>
                            <td>{fmtTokens(outTok)}</td>
                            <td>{fmtTokens(subTok)}</td>
                            <td>{fmtTokens(inTok + outTok)}</td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              )}
            </div>

            <div className="section">
              <h3>Agents</h3>
              <div className="md-table-wrap">
                <table className="grid">
                  <thead>
                    <tr>
                      <th>Agent</th>
                      <th>Model</th>
                      <th>In</th>
                      <th>Out</th>
                      <th>Tools</th>
                      <th>Cost</th>
                      <th>Total</th>
                    </tr>
                  </thead>
                  <tbody>
                    {agents.map((a) => {
                      const u = thread?.usage?.[a.id]?.usage;
                      if (!u) return null;
                      return (
                        <tr key={a.id} className="rowlink" onClick={() => setDetailId(a.id)}>
                          <td>
                            <span className={`dot ${a.status}`} />
                            {a.name || rolesById[a.role] || a.role}
                          </td>
                          <td>{a.model || a.harness}</td>
                          <td>{fmtTokens(u.inputTokens)}</td>
                          <td>{fmtTokens(u.outputTokens)}</td>
                          <td>{u.toolUses}</td>
                          <td>{fmtCost(u.costUsd)}</td>
                          <td>{fmtTokens(totalTokens(u))}</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>

            {globalUsage && (
              <div className="section">
                <h3>Environment</h3>
                <div className="usage-grid">
                  <div className="stat">
                    <div className="k">Orchestrator tokens</div>
                    <div className="v">{fmtTokens(totalTokens(globalUsage.orchestrator))}</div>
                  </div>
                  <div className="stat">
                    <div className="k">Subagent tokens</div>
                    <div className="v">{fmtTokens(totalTokens(globalUsage.subagents))}</div>
                  </div>
                </div>
              </div>
            )}
          </>
        ) : asideActive === "context" ? (
          <div className="section">
            <h3>Context</h3>
            {sharedEntries.length === 0 ? (
              <p className="hint">
                No shared context yet. Agents record findings here as they work.
              </p>
            ) : (
              sharedEntries.map(([key, value]) => (
                <div className="ctx-entry" key={key}>
                  <div className="ctx-key">{key}</div>
                  <div className="ctx-val">{value}</div>
                </div>
              ))
            )}
          </div>
        ) : asideActive === "inherited" ? (
          <div className="section inherited-section">
            <h3>Inherited context</h3>
            <p className="hint">
              Cross-project memory injected into every agent prompt. Agents append learnings with{" "}
              <code>tool learn</code>; everything here is yours to edit.
            </p>
            {!inherited ? (
              <p className="hint">Loading…</p>
            ) : (
              <>
                <div className="inh-block">
                  <span className="field-label">GUIDE.md — standing guidance</span>
                  <textarea
                    className="inh-edit"
                    rows={6}
                    value={inheritedGuide}
                    onChange={(e) => {
                      setInheritedGuide(e.target.value);
                      setInheritedDirty(true);
                    }}
                  />
                </div>
                <div className="inh-block">
                  <span className="field-label">LOG.md — learnings log</span>
                  <textarea
                    className="inh-edit"
                    rows={6}
                    value={inheritedLog}
                    onChange={(e) => {
                      setInheritedLog(e.target.value);
                      setInheritedDirty(true);
                    }}
                  />
                </div>
                <button className="primary" disabled={!inheritedDirty} onClick={saveInherited}>
                  Save guide &amp; log
                </button>
                <div className="inh-block" style={{ marginTop: 14 }}>
                  <span className="field-label">Rules (rules/*.md)</span>
                  {inheritedRules.map((rule, idx) => (
                    <div className="inh-rule" key={rule.name}>
                      <div className="cm-row">
                        <span className="inh-rule-name">{rule.name}</span>
                        <button
                          className="cm-remove"
                          title="Delete this rule"
                          onClick={() => deleteInheritedRule(rule.name)}
                        >
                          ×
                        </button>
                      </div>
                      <textarea
                        className="inh-edit"
                        rows={4}
                        defaultValue={rule.body}
                        onBlur={(e) => {
                          if (e.target.value !== rule.body) {
                            saveInheritedRule(rule.name, e.target.value);
                          }
                        }}
                      />
                    </div>
                  ))}
                  <div className="cm-row">
                    <input
                      className="cm-name"
                      placeholder="new rule name"
                      value={newRuleName}
                      onChange={(e) => setNewRuleName(e.target.value)}
                    />
                    <button
                      className="cm-add"
                      disabled={!newRuleName.trim()}
                      onClick={() => {
                        if (!newRuleName.trim()) return;
                        saveInheritedRule(newRuleName.trim(), "").then(() => setNewRuleName(""));
                      }}
                    >
                      + Add rule
                    </button>
                  </div>
                </div>
              </>
            )}
          </div>
        ) : asideActive === "pr" ? (
          <div className="section pr-section">
            <PRPanel
              snapshot={prState?.threadId === activeId ? prState : null}
              loading={prLoading}
              error={prError}
              onRefresh={() => loadPR(activeId).catch(() => undefined)}
            />
          </div>
        ) : asideActive === "files" ? (
          <div className="section files-section">
            <FilesPanel
              key={activeId ?? "none"}
              root={fileTree?.root ?? null}
              entries={fileTree?.entries ?? []}
              status={fileTree?.status ?? {}}
              loading={fileTreeLoading}
              error={fileTreeError}
              selectedPath={selectedFile}
              content={fileContent}
              contentLoading={fileContentLoading}
              contentError={fileContentError}
              onSelectFile={(p) => loadFileContent(activeId, p)}
              onRefresh={() => loadFileTree(activeId)}
            />
          </div>
        ) : (
          <>
            <div className="section diff-section">
              {diffError && <div className="empty err-text">{diffError}</div>}
              <DiffView
                diff={diff}
                idPrefix="aside-"
                title={thread?.title ?? "Working tree"}
                comments={comments}
                draft={draft}
                emptyText="No changes in the working tree."
                onLineClick={(file, line) => setDraft({ file, line, body: "" })}
                onDraftBody={(body) => setDraft((d) => (d ? { ...d, body } : d))}
                onAddComment={() => addComment().catch(() => undefined)}
                onCancelDraft={() => setDraft(null)}
                onRefresh={() => loadDiff(activeId)}
              />
            </div>
            {reviewComments}
          </>
        )}
        </div>
      </div>

      {detailAgent && (
        <AgentDetail
          agent={detailAgent}
          steps={thread?.steps?.[detailAgent.id] ?? []}
          logs={logs}
          roleName={rolesById[detailAgent.role] || detailAgent.role}
          onClose={() => setDetailId(null)}
        />
      )}

      {councilOpen && activeId && (
        <div className="overlay" onClick={() => setCouncilOpen(false)}>
          <div className="sheet council-sheet" onClick={(e) => e.stopPropagation()}>
            <div className="sheet-head">
              <strong>Convene a council</strong>
              <div className="sheet-head-right">
                <button onClick={() => setCouncilOpen(false)}>Close</button>
              </div>
            </div>
            <div className="sheet-body">
              <label className="field">
                <span>The question to debate</span>
                <textarea
                  rows={2}
                  value={councilTopic}
                  onChange={(e) => setCouncilTopic(e.target.value)}
                  placeholder="e.g. Which approach should we take for the parser rewrite?"
                />
              </label>
              <label className="field">
                <span>
                  Debate rounds — every member answers, then sees and challenges the table
                </span>
                <input
                  type="number"
                  min={1}
                  max={4}
                  value={councilRounds}
                  onChange={(e) =>
                    setCouncilRounds(Math.max(1, Math.min(4, Number(e.target.value) || 2)))
                  }
                  style={{ maxWidth: 100 }}
                />
              </label>
              <div className="council-members">
                <span className="field-label">Members</span>
                {councilMembers.map((m, idx) => (
                  <div className="council-member" key={idx}>
                    <div className="cm-row">
                      <input
                        className="cm-name"
                        value={m.name}
                        onChange={(e) => updateCouncilMember(idx, { name: e.target.value })}
                        placeholder="name"
                        title="Seat name — how this member shows up in the debate"
                      />
                      <Dropdown
                        value={m.role}
                        options={roles.map((r) => ({ value: r.id, label: r.name }))}
                        onChange={(v) => updateCouncilMember(idx, { role: v })}
                      />
                      <Dropdown
                        value={m.harness}
                        options={harnessOptionsFor(m.harness)}
                        onChange={(v) => {
                          updateCouncilMember(idx, { harness: v, model: "" });
                          if (v) refreshModels(v).catch(() => undefined);
                        }}
                      />
                      <Dropdown
                        value={m.model}
                        options={modelOptions(m.harness, m.model)}
                        searchable
                        placeholder="model (role default)"
                        onRefresh={() => refreshModels(m.harness).catch(() => undefined)}
                        onChange={(v) => updateCouncilMember(idx, { model: v })}
                      />
                      {councilMembers.length > 2 && (
                        <button
                          className="cm-remove"
                          title="Remove this seat"
                          onClick={() =>
                            setCouncilMembers((prev) => prev.filter((_, i) => i !== idx))
                          }
                        >
                          ×
                        </button>
                      )}
                    </div>
                    <input
                      className="cm-perspective"
                      value={m.perspective}
                      onChange={(e) => updateCouncilMember(idx, { perspective: e.target.value })}
                      placeholder="Perspective this member argues from (optional)"
                    />
                  </div>
                ))}
                {councilMembers.length < 6 && (
                  <button
                    className="cm-add"
                    onClick={() =>
                      setCouncilMembers((prev) => [
                        ...prev,
                        {
                          name: roles[prev.length % Math.max(1, roles.length)]?.id ?? "member",
                          role: roles[prev.length % Math.max(1, roles.length)]?.id ?? "executor",
                          harness: "",
                          model: "",
                          perspective: "",
                        },
                      ])
                    }
                  >
                    + Add seat
                  </button>
                )}
              </div>
              <p className="hint">
                Members run in parallel each round. A facilitator — who was not part of the debate —
                writes the final consensus into this thread, with dissent recorded.
              </p>
              <div className="sheet-actions">
                <button
                  className="primary"
                  disabled={!councilTopic.trim() || councilMembers.length < 2 || busy}
                  onClick={startCouncil}
                >
                  Convene council
                </button>
              </div>
            </div>
          </div>
        </div>
      )}

      {settingsOpen && settings && (
        <div className="overlay" onClick={() => setSettingsOpen(false)}>
          <div className="sheet" onClick={(e) => e.stopPropagation()}>
            <div className="sheet-head">
              <strong>Settings</strong>
              <div className="sheet-head-right">
                {saved && <span className="saved">Saved</span>}
                <button onClick={() => setSettingsOpen(false)}>Close</button>
              </div>
            </div>
            <div className="sheet-body">
              <section className="set-section">
                <h4>Environment</h4>
                <label className="field">
                  <span>Fallback working directory (used when a thread has no project)</span>
                  <input
                    value={workdirInput}
                    onChange={(e) => setWorkdirInput(e.target.value)}
                    placeholder="/path/to/your/repo"
                  />
                </label>
                <div
                  className={`toggle ${settings.autoMode ? "on" : ""}`}
                  {...switchProps(settings.autoMode, () =>
                    setSettings({ ...settings, autoMode: !settings.autoMode }),
                  )}
                  style={{ marginTop: 10 }}
                >
                  <span className="sw" /> Auto mode — orchestrator picks each task's model
                </div>
                <label className="field" style={{ marginTop: 12 }}>
                  <span>Approved models (comma separated)</span>
                  <input
                    value={approvedInput}
                    onChange={(e) => setApprovedInput(e.target.value)}
                    placeholder="gpt-5, sonnet, …"
                  />
                </label>
                <label className="field" style={{ marginTop: 12 }}>
                  <span>Mark threads stale after this many minutes of inactivity</span>
                  <input
                    value={staleAfterInput}
                    onChange={(e) => setStaleAfterInput(e.target.value)}
                    inputMode="numeric"
                    placeholder="30"
                    style={{ maxWidth: 120 }}
                  />
                </label>
                {settings.configPath && (
                  <p className="hint">
                    Config file: <code>{settings.configPath}</code>
                  </p>
                )}
                <div className="sheet-actions">
                  <button className="primary" onClick={saveEnv}>
                    Save environment
                  </button>
                </div>
              </section>

              <section className="set-section">
                <h4>Harnesses</h4>
                <p className="hint">Disable a harness to remove it from every role.</p>
                {harnesses.all.map((h) => (
                  <div className="harness-row" key={h}>
                    <span className="hn">{h}</span>
                    <span className={`avail ${harnesses.available.includes(h) ? "detected" : ""}`}>
                      {harnesses.available.includes(h) ? "detected" : "not on PATH"}
                    </span>
                    <div
                      className={`toggle ${isHarnessEnabled(h) ? "on" : ""}`}
                      {...switchProps(isHarnessEnabled(h), () =>
                        toggleHarness(h, !isHarnessEnabled(h)),
                      )}
                      aria-label={`${isHarnessEnabled(h) ? "Disable" : "Enable"} ${h}`}
                    >
                      <span className="sw" />
                    </div>
                  </div>
                ))}
              </section>

              <section className="set-section">
                <h4>Roles</h4>
                <p className="hint">
                  Each role runs on its own harness and model. Changes apply to the next turn.
                </p>
                {modelsNotice && <p className="hint warn">{modelsNotice}</p>}
                {settings.roles.map((r) => (
                  <div className="role-edit" key={r.id}>
                    <span className="rn">{r.name}</span>
                    <Dropdown
                      value={r.harness}
                      options={harnessOptionsFor(r.harness)}
                      onChange={(v) => {
                        const row = { ...r, harness: v };
                        updateRole(r.id, { harness: v });
                        persistRole(row).catch(() => undefined);
                        refreshModels(v).catch(() => undefined);
                      }}
                    />
                    <Dropdown
                      value={r.model}
                      options={modelOptions(r.harness, r.model)}
                      searchable
                      placeholder="model (default)"
                      footNote="Discovered from the harness CLI"
                      onRefresh={() => refreshModels(r.harness).catch(() => undefined)}
                      addLabel="Add model"
                      onAdd={(m) => {
                        addModel(r.harness, m)
                          .then(() => {
                            const row = { ...r, model: m };
                            updateRole(r.id, { model: m });
                            persistRole(row).catch(() => undefined);
                          })
                          .catch(() => undefined);
                      }}
                      onChange={(v) => {
                        const row = { ...r, model: v };
                        updateRole(r.id, { model: v });
                        persistRole(row).catch(() => undefined);
                      }}
                    />
                  </div>
                ))}
              </section>
            </div>
          </div>
        </div>
      )}

      {threadOverlays}
    </div>
  );
}
