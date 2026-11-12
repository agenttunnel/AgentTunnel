import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ClipboardEvent as ReactClipboardEvent,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent,
} from "react";
import { Window } from "@wailsio/runtime";
import { discover, pickFolder, Rpc, setKeepRunning as emitKeepRunning, type Conn } from "./lib/rpc";
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
import { ThreadSetup } from "./components/ThreadSetup";
import { ApprovedModelsPicker } from "./components/ApprovedModelsPicker";
import { switchProps } from "./lib/switchProps";
import {
  PANEL_BOUNDS,
  PanelToggle,
  SplitHandle,
  clampWidth,
} from "./components/PanelSplit";
import { AgentGraph } from "./components/AgentGraph";
import { HOLD_MOD_LABEL, useHoldToTalk } from "./lib/dictation";
import { VoiceRecorder } from "./components/VoiceRecorder";
import { useRotatingPlaceholder } from "./lib/placeholder";
import { KanbanBoard } from "./components/KanbanBoard";
import { TaskList, type TaskItem } from "./components/TaskList";
import { DiffView, lineRange, type DraftLike } from "./components/DiffView";
import { reviewAnchorKey, type ReviewThreadLike } from "./components/ReviewThreads";
import { Dropdown, type DropdownOption } from "./components/Dropdown";
import { FileEditor } from "./components/FileEditor";
import { MentionMenu } from "./components/MentionMenu";
import { dropMention, mentionAt, MAX_ATTACHMENTS, type Attachment, type FileMatch } from "./lib/mentions";
import { PRPanel, type PRListing } from "./components/PRPanel";
import { ThreadBoard, type ThreadState as BoardThreadState } from "./components/ThreadBoard";
import { HarnessStatsView } from "./components/HarnessStatsPanel";
import { StatsPage } from "./components/StatsPage";
import { WorktreesPanel, type WorktreeInfo } from "./components/WorktreesPanel";
import { RemoteSection } from "./components/RemoteSection";
import { Markdown } from "./components/Markdown";
import { QuestionCard } from "./components/QuestionCard";
import type { AgentQuestion } from "./lib/questions";
import { AgentDetail } from "./components/AgentDetail";
import { ThreadInfoDialog } from "./components/ThreadInfoDialog";
import { UpdateChip, type UpdateStatus } from "./components/UpdateChip";
import { HarnessUpdateChip } from "./components/HarnessUpdateChip";
import type {
  HarnessUpdateReport,
  HarnessUpdateResult,
} from "./lib/harnessUpdates";
import { LimitsMeter } from "./components/LimitsMeter";
import type { MailboxMessage } from "./lib/graph";
import { arrangeThreads, visibleThreadRows } from "./lib/threads";
import { fuzzyScore } from "./lib/fuzzy";
import {
  buildCommands,
  matchCommands,
  parseSlash,
  slashQuery,
  type SlashCommand,
} from "./lib/commands";
import { SlashMenu } from "./components/SlashMenu";
import { BtwPanel, type BtwEntry } from "./components/BtwPanel";
import { MasterChat } from "./components/MasterChat";

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
type Message = { id: string; author: string; text: string; at: string; consumed?: boolean };
/** A shell block ends with its status line: `exit N`, `stopped`, or `timed out after …`. */
const SHELL_FAILED = /\n(?:exit [1-9]\d*|stopped|timed out after .+?)(?: · .*)?$/;
function isFailedBlock(m: Message): boolean {
  return m.author === "shell" && SHELL_FAILED.test(m.text.trimEnd());
}
/** submitted comments were already handed to the team in a review turn. */
type ReviewComment = {
  id: string;
  file: string;
  line: number;
  endLine: number;
  side?: "old" | "new";
  body: string;
  submitted: boolean;
};
/** A reviewer's inline thread on a diff line; replies run oldest first. */
type ReviewThread = {
  id: string;
  file: string;
  line: number;
  endLine: number;
  side?: "old" | "new";
  body: string;
  authorId: string;
  status: "open" | "resolved";
  replies?: { authorId: string; body: string; at: string }[] | null;
  openedAt: string;
  updatedAt: string;
};
/** One lead's runtime override. Empty fields inherit the Orchestrator role's runtime. */
export type LeadRuntimeView = { harness?: string; model?: string; variant?: string };

export type ThreadState = {
  id: string;
  title: string;
  autoMode: boolean;
  peerMailbox: boolean;
  /** Seconds between workflow status reports while a turn runs; 0 is off. */
  statusPeriodSeconds?: number;
  /** Orchestrator team size; zero or absent means one lead. */
  leads?: number;
  /** Per-lead runtime overrides by lead index; an empty harness inherits the Orchestrator role. */
  leadRuntimes?: LeadRuntimeView[];
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
  /** Reviewer threads on diff lines. Absent on daemons that predate them. */
  reviewThreads?: ReviewThread[] | null;
  /** Agent questions for the human, oldest first. Pending ones await an answer. */
  questions?: AgentQuestion[] | null;
  /** The agreed task list, in the order the leads added it. */
  tasks?: TaskItem[] | null;
};
type ThreadSummary = {
  id: string;
  title: string;
  projectId?: string;
  /** The first human message, truncated by the daemon for list search. */
  firstPrompt?: string;
  /** Set on a subthread: the thread that started it. */
  parentThreadId?: string;
  updatedAt?: string;
  messages: number;
  agents: number;
  autoMode: boolean;
  /** Filed by the user; shown in the board's Archived column. */
  archived?: boolean;
  runningAgents?: number;
  staleAfterSeconds?: number;
  stale?: boolean;
  /** The thread's own worktree; its branch is what a PR is opened from. */
  worktree?: { path: string; branch: string; base: string } | null;
};

// A prompt hit must be stronger than a title hit to win, so a thread whose
// title matches leads a thread that only mentions the query in its prompt.
const PROMPT_WEIGHT = 0.7;

function threadSearchScore(t: ThreadSummary, query: string): number | null {
  const title = fuzzyScore(query, t.title);
  const prompt = t.firstPrompt ? fuzzyScore(query, t.firstPrompt) : null;
  if (title === null && prompt === null) return null;
  return Math.max(title ?? -Infinity, (prompt ?? -Infinity) * PROMPT_WEIGHT);
}
export type Project = { id: string; name: string; path: string; threads: number };

/** The top-level pages shown when no project is open. */
type HomePage = "projects" | "orchestrator" | "stats";

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
export type RoleConfigView = {
  id: string;
  name: string;
  harness: string;
  model: string;
  variant: string;
  builtin: boolean;
  enabled: boolean;
  required: boolean;
  experimental: boolean;
};
export type SettingsView = {
  workdir: string;
  autoMode: boolean;
  approvedModels: string[] | null;
  experimentalRoles: boolean;
  roles: RoleConfigView[];
  /** Master orchestrator override; empty fields inherit the Orchestrator role. */
  lead?: LeadRuntimeView;
  configPath: string;
  staleAfterMinutes?: number;
};
export type DependencyStatus = "ok" | "outdated" | "missing";
export type DependencyView = {
  name: string;
  installed: boolean;
  version?: string;
  minVersion: string;
  status: DependencyStatus;
};

/** Aside panel tabs: agents + usage are pinned; the rest are addable.
 * (The team graph lives as a center tab beside Chat and Review. See `tab`.) */
type AsideTabId =
  | "agents"
  | "usage"
  | "context"
  | "inherited"
  | "files"
  | "diff"
  | "pr"
  | "stats"
  | "models"
  | "council";
const ASIDE_PINNED: AsideTabId[] = ["agents", "usage"];
const ASIDE_META: Record<AsideTabId, { label: string; key: string }> = {
  agents: { label: "Agents", key: "A" },
  usage: { label: "Token usage", key: "U" },
  context: { label: "Context", key: "C" },
  inherited: { label: "Inherited", key: "M" },
  files: { label: "Files", key: "F" },
  diff: { label: "Diff", key: "D" },
  pr: { label: "Pull request", key: "P" },
  stats: { label: "Harness stats", key: "H" },
  models: { label: "Models", key: "L" },
  council: { label: "Council", key: "O" },
};
/** How the project's threads are laid out: the card list or the status board. */
type ThreadView = "list" | "board";
function loadThreadView(): ThreadView {
  try {
    if (window.localStorage.getItem("at.threadView") === "board") return "board";
  } catch {
    /* private mode: default to the list */
  }
  return "list";
}

/** How often the board re-reads PR state while it is open. */
const THREAD_PR_POLL_MS = 20_000;
/** How often a connected app asks for a newer release. At this rate a client
 * makes about 12 unauthenticated GitHub API calls an hour (the cap is 60). */
const UPDATE_CHECK_MS = 5 * 60_000;
/** After a detached send, how long to wait for the turn to appear before
 * treating a busy flag with no visible turn as stale (the start failed before
 * any agent existed). */
const DETACHED_START_GRACE_MS = 60_000;
/** Playful verbs for the setup view's Start button. */
const START_VERBS = [
  "Brewing",
  "Noodling",
  "Wrangling",
  "Tinkering",
  "Conjuring",
  "Herding",
  "Scheming",
  "Juggling",
  "Puzzling",
  "Dreaming",
];

/** The activity log box is shown unless the viewer collapsed it; the choice persists. */
function loadLogsOpen(): boolean {
  try {
    return window.localStorage.getItem("at.logsOpen") !== "false";
  } catch {
    /* private mode: default to shown */
  }
  return true;
}

function sameThreadState(a: BoardThreadState, b: BoardThreadState): boolean {
  if (a.dirty !== b.dirty) return false;
  const p = a.pr;
  const q = b.pr;
  if (!p || !q) return p === q;
  return p.number === q.number && p.url === q.url && p.state === q.state && p.draft === q.draft && p.reviewDecision === q.reviewDecision;
}

/** Keeps the entries that did not change, so memoized board cards keep their props. */
function mergeThreadStates(
  prev: Record<string, BoardThreadState>,
  next: Record<string, BoardThreadState>,
): Record<string, BoardThreadState> {
  const ids = Object.keys(next);
  let changed = ids.length !== Object.keys(prev).length;
  const out: Record<string, BoardThreadState> = {};
  for (const id of ids) {
    const old = prev[id];
    const cur = next[id];
    if (old && sameThreadState(old, cur)) {
      out[id] = old;
    } else {
      out[id] = cur;
      changed = true;
    }
  }
  return changed ? out : prev;
}

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

/** Long pastes collapse to a `[Pasted text #id +N lines]` token; the full text is
 *  kept per draft and swapped back in on send, persistence, or click. */
const PASTE_TOKEN = /\[Pasted text #(\d+) \+\d+ lines\]/g;
function pasteTokens(text: string): { start: number; end: number; id: number }[] {
  return [...text.matchAll(PASTE_TOKEN)].map((m) => {
    const start = m.index ?? 0;
    return { start, end: start + m[0].length, id: Number(m[1]) };
  });
}
function expandPastes(text: string, store?: Record<number, string>): string {
  if (!store) return text;
  return text.replace(PASTE_TOKEN, (tok, id: string) => store[Number(id)] ?? tok);
}
/** Turns a flat message list into turns: each user message opens one block. */
function groupTurns(messages: Message[]): Message[][] {
  const turns: Message[][] = [];
  for (const m of messages) {
    if (m.author === "user" || turns.length === 0) turns.push([m]);
    else turns[turns.length - 1].push(m);
  }
  return turns;
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

type PanelLayout = {
  sidebarW: number;
  asideW: number;
  sidebarOpen: boolean;
  asideOpen: boolean;
};

/** Panel sizes and collapsed state persist per viewer; a corrupt entry falls back. */
function loadLayout(): PanelLayout {
  const { sidebar, aside } = PANEL_BOUNDS;
  const fallback: PanelLayout = {
    sidebarW: sidebar.def,
    asideW: aside.def,
    sidebarOpen: true,
    asideOpen: true,
  };
  try {
    const raw = window.localStorage.getItem("at.layout");
    if (!raw) return fallback;
    const v = JSON.parse(raw) as Partial<PanelLayout>;
    const width = (n: unknown, b: { min: number; max: number; def: number }) =>
      typeof n === "number" && Number.isFinite(n) ? clampWidth(n, b.min, b.max) : b.def;
    return {
      sidebarW: width(v.sidebarW, sidebar),
      asideW: width(v.asideW, aside),
      sidebarOpen: v.sidebarOpen !== false,
      asideOpen: v.asideOpen !== false,
    };
  } catch {
    return fallback;
  }
}

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

/** UI state that belongs to one thread. A thread with no value of its own reads
 * the global default; writing sets only that thread's value. Writing while no
 * thread is selected changes the default. `storageKey` keeps the default across
 * launches; a thread's own values live for the session. */
function useThreadScoped<T>(threadId: string | null, initial: T | (() => T), storageKey?: string) {
  const [fallback, setFallback] = useState<T>(() => {
    if (storageKey) {
      try {
        const raw = window.localStorage.getItem(storageKey);
        if (raw !== null) return JSON.parse(raw) as T;
      } catch {
        /* corrupt or private mode: use the built-in default */
      }
    }
    return typeof initial === "function" ? (initial as () => T)() : initial;
  });
  const [byThread, setByThread] = useState<Record<string, T>>({});
  const threadRef = useRef(threadId);
  threadRef.current = threadId;
  const fallbackRef = useRef(fallback);
  fallbackRef.current = fallback;
  useEffect(() => {
    if (!storageKey) return;
    try {
      window.localStorage.setItem(storageKey, JSON.stringify(fallback));
    } catch {
      /* private mode: the default resets next launch */
    }
  }, [fallback, storageKey]);
  const value = threadId !== null && threadId in byThread ? byThread[threadId] : fallback;
  const set = useCallback((next: T | ((prev: T) => T)) => {
    const id = threadRef.current;
    const apply = (prev: T) => (typeof next === "function" ? (next as (p: T) => T)(prev) : next);
    if (id === null) setFallback((prev) => apply(prev));
    else setByThread((prev) => ({ ...prev, [id]: apply(id in prev ? prev[id] : fallbackRef.current) }));
  }, []);
  return [value, set, setFallback] as const;
}

const defaultCouncilSetup = (): { rounds: number; members: CouncilMemberRow[] } => ({
  rounds: 2,
  members: [
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
  ],
});

export default function App() {
  const [status, setStatus] = useState<"connecting" | "online" | "offline">("connecting");
  // Errors are kept per thread (key = thread id; "" = environment-wide), so a
  // failure in one thread never shows in another.
  const [errors, setErrors] = useState<Record<string, string>>({});

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
  // Last loaded state per thread, so switching back paints the thread at once
  // instead of an empty screen until thread.get returns. Keyed by id, so a
  // thread never shows another thread's messages.
  const threadCacheRef = useRef<Record<string, ThreadState>>({});
  const [globalUsage, setGlobalUsage] = useState<ThreadUsage | null>(null);
  const [update, setUpdate] = useState<UpdateStatus | null>(null);
  const [updateBusy, setUpdateBusy] = useState(false);
  const [updateError, setUpdateError] = useState<string | null>(null);
  const [harnessUpdates, setHarnessUpdates] = useState<HarnessUpdateReport | null>(null);
  const [harnessUpdateBusy, setHarnessUpdateBusy] = useState(false);
  const [harnessUpdateError, setHarnessUpdateError] = useState<string | null>(null);
  // A check keeps the "restart to run" state after an install; the daemon's
  // plain check result does not know about it.
  const checkForUpdate = useCallback(() => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    rpc
      .call<UpdateStatus>("update.check")
      .then((s) => setUpdate((prev) => (prev?.installed ? prev : s)))
      .catch(() => {});
  }, []);
  useEffect(() => {
    if (status !== "online") return;
    const id = window.setInterval(checkForUpdate, UPDATE_CHECK_MS);
    return () => window.clearInterval(id);
  }, [status, checkForUpdate]);

  // Threads with a turn, council, or review in flight. Keyed by thread so a turn
  // running in one thread never gates or steers another.
  const [turnThreads, setTurnThreads] = useState<Record<string, true>>({});
  const setTurnBusy = useCallback((threadId: string, on: boolean) => {
    setTurnThreads((prev) => {
      const next = { ...prev };
      if (on) next[threadId] = true;
      else delete next[threadId];
      return next;
    });
  }, []);
  // Threads with a send in its RPC window; stops a double Enter from submitting twice.
  const sendingRef = useRef(new Set<string>());
  // Detached turns outlive the RPC that starts them: thread.turn.start with
  // detach replies before any agent exists, so the busy flag cannot be cleared
  // when the call returns. The thread list shows a running turn as
  // pending/running agents (and a new user message); clear the flag once the
  // turn has appeared and gone quiet again, or after a start grace if it never
  // appeared at all.
  const detachedTurns = useRef<Record<string, { seen: boolean; messages: number; at: number }>>({});
  const noteDetachedTurn = useCallback(
    (threadId: string) => {
      const summary = threads.find((t) => t.id === threadId);
      // Prefer the live thread state for the thread on screen: the summary can
      // lag one refresh behind, and a stale baseline would look like the turn
      // already started.
      const live = activeIdRef.current === threadId ? (thread?.messages?.length ?? 0) : 0;
      detachedTurns.current[threadId] = {
        seen: false,
        messages: Math.max(summary?.messages ?? 0, live),
        at: Date.now(),
      };
    },
    [threads, thread],
  );
  const settleDetachedTurns = useCallback(
    (list: ThreadSummary[]) => {
      const now = Date.now();
      for (const [id, entry] of Object.entries(detachedTurns.current)) {
        const summary = list.find((t) => t.id === id);
        const inFlight = (summary?.runningAgents ?? 0) > 0;
        if (inFlight) entry.seen = true;
        // The user message is recorded when the turn starts; a fast turn that
        // began and ended between two refreshes still counts as started.
        const started = entry.seen || (summary?.messages ?? 0) > entry.messages;
        if ((started && !inFlight) || (!started && now - entry.at > DETACHED_START_GRACE_MS)) {
          delete detachedTurns.current[id];
          setTurnBusy(id, false);
        }
      }
    },
    [setTurnBusy],
  );
  const activeIdRef = useRef<string | null>(null);
  activeIdRef.current = activeId;
  const busy = !!activeId && !!turnThreads[activeId];
  // Defaults to the thread on screen; pass a thread to file the error elsewhere.
  const setError = useCallback((message: string | null, thread: string | null = activeIdRef.current) => {
    const key = thread ?? "";
    setErrors((prev) => {
      const next = { ...prev };
      if (message === null) delete next[key];
      else next[key] = message;
      return next;
    });
  }, []);
  const error = (activeId && errors[activeId]) || errors[""] || null;
  /** The thread whose "! cmd" is running, so its Stop bar only shows there. */
  const [shellThread, setShellThread] = useState<string | null>(null);
  const [detailId, setDetailId] = useThreadScoped<string | null>(activeId, null);
  // Aside panel tabs: agents + usage are pinned; context, files, and diff can
  // be added via the "+" menu (or single-letter shortcuts) and closed again.
  const [asideTabs, setAsideTabs, setAsideTabsDefault] = useThreadScoped<AsideTabId[]>(
    activeId,
    loadAsideTabs,
    "at.asideTabs",
  );
  const [asideActive, setAsideActive, setAsideActiveDefault] = useThreadScoped<AsideTabId>(
    activeId,
    "agents",
    "at.asideActive",
  );
  // Side panels: width (dragged via the split handles) and collapsed state.
  const [layout, setLayout, setLayoutDefault] = useThreadScoped<PanelLayout>(
    activeId,
    loadLayout,
    "at.layout",
  );
  const shellRef = useRef<HTMLDivElement>(null);
  const [panelMenu, setPanelMenu] = useState<{ x: number; y: number } | null>(null);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [live, setLive] = useState<{ agentId: string; kind: string; text: string }[]>([]);
  // The live log box can be collapsed to a header; the choice persists per viewer.
  const [logsOpen, setLogsOpen] = useState(loadLogsOpen);
  const toggleLogs = () => {
    const next = !logsOpen;
    setLogsOpen(next);
    try {
      window.localStorage.setItem("at.logsOpen", String(next));
    } catch {
      /* private mode: the choice lasts this session */
    }
  };

  const [tab, setTab, setTabDefault] = useThreadScoped<"chat" | "review" | "graph" | "board">(
    activeId,
    "chat",
    "at.tab",
  );
  const [diff, setDiff] = useState<Diff | null>(null);
  const [diffError, setDiffError] = useState<string | null>(null);
  const [draft, setDraft] = useThreadScoped<DraftLike | null>(activeId, null);

  // Inherited context panel state: the environment's shared memory.
  const [inherited, setInherited] = useState<InheritedView | null>(null);
  const [inheritedGuide, setInheritedGuide] = useState("");
  const [inheritedLog, setInheritedLog] = useState("");
  const [inheritedDirty, setInheritedDirty] = useState(false);
  const [inheritedRules, setInheritedRules] = useState<{ name: string; body: string }[]>([]);
  const [newRuleName, setNewRuleName] = useState("");
  // Pull requests the active thread opened; tagged with its thread so a late reply never shows elsewhere.
  const [prState, setPRState] = useState<PRListing | null>(null);
  const [prLoading, setPRLoading] = useState(false);
  const [prError, setPRError] = useState<string | null>(null);
  // Worktrees: per open project, live git facts from worktree.list. Every thread
  // in a git project gets one, so the UI offers no choice about it. Shown in the
  // threads view (no thread open), not the aside.
  const [worktrees, setWorktrees] = useState<Record<string, WorktreeInfo[] | undefined>>({});
  const [worktreesLoading, setWorktreesLoading] = useState(false);
  const [worktreesError, setWorktreesError] = useState<string | null>(null);
  const [showWorktrees, setShowWorktrees] = useState(false);

  const [settingsOpen, setSettingsOpen] = useState(false);
  const modelsOpen = asideActive === "models" && asideTabs.includes("models");
  // Pasted text per draft: draftKey -> token id -> full text.
  const [pastes, setPastes] = useState<Record<string, Record<number, string>>>({});
  // Files and folders attached per draft through @; sent with the turn as context.
  const [attachments, setAttachments] = useState<Record<string, Attachment[]>>({});
  // Caret in the composer: an @-token is the word ending at the caret.
  const [caret, setCaret] = useState(0);
  const [mentionActive, setMentionActive] = useState(0);
  // Start of an @-token the user dismissed with Escape.
  const [mentionDismissed, setMentionDismissed] = useState<number | null>(null);
  // Matches tagged with the query they answer, so an old list never shows for a new query.
  const [mentionResults, setMentionResults] = useState<{ query: string; matches: FileMatch[] } | null>(
    null,
  );
  const [settings, setSettings] = useState<SettingsView | null>(null);
  // Shell capability state: where the daemon lives and whether it should
  // outlive the window. Connection data only — thread data stays on the
  // daemon WebSocket.
  const [conn, setConn] = useState<Conn | null>(null);
  const [keepRunning, setKeepRunningState] = useState(false);
  // Send turns detached (daemon-scoped) so closing the window cannot cancel
  // them. Defaults on when the daemon already outlives this window.
  const [runInBackground, setRunInBackground] = useState(false);

  // Council of agents: convene several perspectives on one question and let
  // them debate to a synthesized outcome.
  // The topic is per thread; rounds and seats default globally and can be saved
  // as the default for new threads.
  const [councilTopic, setCouncilTopic] = useThreadScoped<string>(activeId, "");
  const [councilSetup, setCouncilSetup, setCouncilSetupDefault] = useThreadScoped(
    activeId,
    defaultCouncilSetup,
    "at.councilSetup",
  );
  const councilRounds = councilSetup.rounds;
  const councilMembers = councilSetup.members;
  const setCouncilRounds = (rounds: number) => setCouncilSetup((s) => ({ ...s, rounds }));
  const setCouncilMembers = (update: (prev: CouncilMemberRow[]) => CouncilMemberRow[]) =>
    setCouncilSetup((s) => ({ ...s, members: update(s.members) }));
  const [workdirInput, setWorkdirInput] = useState("");
  const [approvedModels, setApprovedModels] = useState<string[]>([]);
  const [staleAfterInput, setStaleAfterInput] = useState("30");
  const [saved, setSaved] = useState(false);
  const [modelsNotice, setModelsNotice] = useState<string | null>(null);
  const [appVersion, setAppVersion] = useState<string | null>(null);
  const [deps, setDeps] = useState<DependencyView[] | null>(null);
  const [depsBusy, setDepsBusy] = useState(false);
  const [depsError, setDepsError] = useState<string | null>(null);

  const [projects, setProjects] = useState<Project[]>([]);
  const [projectId, setProjectId] = useState<string | null>(null);
  // Each (project, thread) keeps its own composer text, so switching threads
  // swaps the draft instead of carrying it over.
  const [drafts, setDrafts] = useState<Record<string, string>>(loadDrafts);
  const draftKey = draftKeyOf(projectId, activeId);
  const input = drafts[draftKey] ?? "";
  // Mirrors send(): a leading "!" (after trimming) runs a shell command.
  const commandMode = input.trimStart().startsWith("!");
  // Read by openPanel (a stable callback) to seed the council topic from the draft.
  const draftRef = useRef("");
  useEffect(() => {
    draftRef.current = expandPastes(input, pastes[draftKey]).trim();
  }, [input, pastes, draftKey]);
  const setInput = useCallback(
    (text: string) => {
      if (!activeId) return;
      setDrafts((prev) => ({ ...prev, [draftKey]: text }));
    },
    [activeId, draftKey],
  );
  // Projects stay open as tabs; each one remembers the thread it was showing.
  const [openProjectIds, setOpenProjectIds] = useState<string[]>(loadOpenProjects);
  // Which top-level page shows when no project is open.
  const [homePage, setHomePage] = useState<HomePage>("projects");
  const lastThreadByProject = useRef<Record<string, string | null>>({});

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
  // Only the edge is stored, so scrolling re-renders when the user crosses it.
  const [atBottom, setAtBottom] = useState(true);
  const nearBottom = (el: HTMLElement) => el.scrollHeight - el.scrollTop - el.clientHeight < 120;

  useEffect(() => {
    const el = streamRef.current;
    if (!el) return;
    // Follow the stream only when the user is near the bottom; reading history
    // wins over jumping.
    const near = nearBottom(el);
    if (near) el.scrollTop = el.scrollHeight;
    setAtBottom(near);
  }, [
    thread?.messages?.length,
    thread?.messages?.[thread.messages.length - 1]?.text,
    thread?.questions?.length,
    live.length,
  ]);

  const scrollToBottom = () => {
    const el = streamRef.current;
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
  };

  // Live log follows the newest output, unless the user scrolled up to read.
  // Reopening a collapsed box jumps back to the newest output.
  const actBodyRef = useRef<HTMLDivElement | null>(null);
  const logsWasOpen = useRef(logsOpen);
  useEffect(() => {
    const opening = !logsWasOpen.current && logsOpen;
    logsWasOpen.current = logsOpen;
    const el = actBodyRef.current;
    if (!el) return;
    if (opening || el.scrollHeight - el.scrollTop - el.clientHeight < 48) {
      el.scrollTop = el.scrollHeight;
    }
  }, [live, logsOpen]);

  const refresh = useCallback(async (id: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    const [state, list, usage] = await Promise.all([
      rpc.call<ThreadState>("thread.get", { threadId: id }),
      rpc.call<ThreadSummary[]>("thread.list"),
      rpc.call<ThreadUsage>("usage.get"),
    ]);
    threadCacheRef.current[id] = state;
    // A reply for a thread the user has since left must not replace the one on screen.
    if (activeIdRef.current === id) setThread(state);
    setThreads(list);
    settleDetachedTurns(list);
    setGlobalUsage(usage);
  }, [settleDetachedTurns]);

  // The daemon closes a question on the answer, so refresh right away: the card
  // flips to its answered state without waiting for the event debounce. Errors
  // reject to the card, which shows them beside its buttons.
  const answerQuestion = useCallback(
    async (questionId: string, selected: string[], text: string) => {
      const rpc = rpcRef.current;
      if (!rpc || !activeId) return;
      await rpc.call("question.answer", { threadId: activeId, questionId, selected, text });
      await refresh(activeId);
    },
    [activeId, refresh],
  );
  const dismissQuestion = useCallback(
    async (questionId: string) => {
      const rpc = rpcRef.current;
      if (!rpc || !activeId) return;
      await rpc.call("question.dismiss", { threadId: activeId, questionId });
      await refresh(activeId);
    },
    [activeId, refresh],
  );

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

  const updateHarnesses = useCallback(async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    setHarnessUpdateBusy(true);
    setHarnessUpdateError(null);
    try {
      const res = await rpc.call<HarnessUpdateResult>("harness.update", { harnesses: [] });
      setHarnessUpdates(res.report);
      if (res.failed && res.failed.length > 0) {
        setHarnessUpdateError(res.failed.map((f) => `${f.harness}: ${f.error}`).join("; "));
      }
    } catch (e) {
      setHarnessUpdateError(e instanceof Error ? e.message : String(e));
    } finally {
      setHarnessUpdateBusy(false);
    }
  }, []);

  const loadDiff = useCallback(async (id: string | null) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      // Thread-scoped: the daemon diffs the thread's project directory, not
      // the global fallback workdir.
      const d = await rpc.call<Diff>("review.diff", { threadId: id });
      if (activeIdRef.current !== id) return;
      setDiff(d);
      setDiffError(null);
    } catch (e) {
      if (activeIdRef.current !== id) return;
      setDiffError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  const loadPR = useCallback(async (id: string | null) => {
    const rpc = rpcRef.current;
    if (!rpc || !id) return;
    setPRLoading(true);
    setPRError(null);
    try {
      const listing = await rpc.call<PRListing>("pr.list", { threadId: id });
      if (activeIdRef.current === id) setPRState({ ...listing, threadId: id });
    } catch (e) {
      if (activeIdRef.current === id) setPRError(e instanceof Error ? e.message : String(e));
    } finally {
      if (activeIdRef.current === id) setPRLoading(false);
    }
  }, []);


  const openPanel = useCallback((id: AsideTabId) => {
    if (id === "council" && draftRef.current) setCouncilTopic(draftRef.current);
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

  const loadDeps = useCallback(async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    setDepsBusy(true);
    try {
      setDeps(await rpc.call<DependencyView[]>("deps.check"));
      setDepsError(null);
    } catch (e) {
      setDepsError(e instanceof Error ? e.message : String(e));
    } finally {
      setDepsBusy(false);
    }
  }, []);

  const loadProjects = useCallback(async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    setProjects(await rpc.call<Project[]>("project.list"));
  }, []);

  /** Adding a project is the native folder picker; the folder's name is the project name. */
  const addProject = async () => {
    const rpc = rpcRef.current;
    const path = await pickFolder();
    if (!rpc || !path) return;
    const name = path.split("/").filter(Boolean).pop() ?? path;
    const res = await rpc.call<{ project: Project; projects: Project[] }>("project.create", {
      name,
      path,
    });
    setProjects(res.projects);
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

  const showHome = (page: HomePage) => {
    setProjectId(null);
    setActiveId(null);
    setHomePage(page);
  };
  const showProjects = () => showHome("projects");

  /** From a thread, leave it for its project's board. */
  const showProjectBoard = () => {
    setThreadView("board");
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

  // --- worktree panel actions --------------------------------------------

  /** Loads the worktrees of the given projects. A failing project does not hide
   * the others; the first failure is shown in the panel. */
  const loadWorktrees = useCallback(async (ids: string[]) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    setWorktreesLoading(true);
    setWorktreesError(null);
    const results = await Promise.allSettled(
      ids.map((id) => rpc.call<WorktreeInfo[]>("worktree.list", { projectId: id })),
    );
    setWorktrees((prev) => {
      const next = { ...prev };
      results.forEach((r, i) => {
        if (r.status === "fulfilled") next[ids[i]] = r.value;
      });
      return next;
    });
    const failed = results.find((r): r is PromiseRejectedResult => r.status === "rejected");
    if (failed) setWorktreesError(failed.reason instanceof Error ? failed.reason.message : String(failed.reason));
    setWorktreesLoading(false);
  }, []);

  /** Runs one worktree mutation, then reloads that project's list. A refused
   * remove (dirty tree) shows the daemon's reason instead of failing silently. */
  const worktreeAction = async (projectId: string, method: string, params: Record<string, unknown>) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    let failure: string | null = null;
    try {
      await rpc.call(method, { projectId, ...params });
    } catch (e) {
      failure = e instanceof Error ? e.message : String(e);
    }
    await loadWorktrees([projectId]);
    if (failure) setWorktreesError(failure);
  };

  const openWorktreeThread = (projectId: string, threadId: string) => {
    openProject(projectId);
    lastThreadByProject.current[projectId] = threadId;
    setActiveId(threadId);
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

  /** Archives or restores several threads in one go. A failure stops the batch
   * and reports the first error; threads already changed stay changed. */
  const setThreadsArchived = async (ids: string[], archived: boolean) => {
    const rpc = rpcRef.current;
    if (!rpc || ids.length === 0) return;
    try {
      for (const threadId of ids) {
        await rpc.call("thread.setArchived", { threadId, enabled: archived });
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
    await reloadThreads();
  };

  /** Deletes several threads. Deleting the open thread closes back to the list. */
  const deleteThreadsById = async (ids: string[]) => {
    const rpc = rpcRef.current;
    if (!rpc || ids.length === 0) return;
    const deleted: string[] = [];
    try {
      for (const threadId of ids) {
        await rpc.call("thread.delete", { threadId });
        deleted.push(threadId);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
    await reloadThreads();
    if (activeId && deleted.includes(activeId)) {
      selectThread(null);
      setThread(null);
    }
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
    setApprovedModels(s.approvedModels ?? []);
    setStaleAfterInput(String(s.staleAfterMinutes ?? 30));
    return s;
  }, []);

  const loadAppInfo = useCallback(async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      const info = await rpc.call<{ version?: string }>("app.info");
      setAppVersion(info?.version || "dev");
    } catch {
      setAppVersion("dev");
    }
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
      setConn(conn);
      setKeepRunningState(!!conn.keepRunning);
      // A daemon that outlives this window (external, or kept running) makes
      // detached turns the sane default.
      if (conn.attached || conn.keepRunning) setRunInBackground(true);
      // Not awaited: a slow GitHub lookup must not hold up startup.
      checkForUpdate();
      rpc.call<HarnessUpdateReport>("harness.updates").then(setHarnessUpdates).catch(() => {});
      try {
        const [r, h, list, projs, s] = await Promise.all([
          rpc.call<Role[]>("roles.list"),
          rpc.call<HarnessInfo>("harness.list"),
          rpc.call<ThreadSummary[]>("thread.list"),
          rpc.call<Project[]>("project.list"),
          rpc.call<SettingsView>("settings.get"),
        ]);
        setRoles(r);
        setSettings(s);
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
  // Switching threads drops the other thread's data. The new thread paints from
  // its cached state (or nothing if never loaded) while refresh reloads it, and
  // late replies for the old thread are ignored by the loaders.
  useEffect(() => {
    setThread(activeId ? (threadCacheRef.current[activeId] ?? null) : null);
    setLogs([]);
    setDiff(null);
    setDiffError(null);
    setPRState(null);
    setPRError(null);
    setPRLoading(false);
  }, [activeId]);
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


  // Worktree facts come from live git runs, so they load when the section is shown
  // and after each action, not on a timer.
  useEffect(() => {
    if (projectId && !activeId && showWorktrees) loadWorktrees([projectId]).catch(() => undefined);
  }, [projectId, activeId, showWorktrees, loadWorktrees]);

  // A stored aside tab that no longer exists (e.g. the removed Worktrees tab)
  // would otherwise render the diff fallback with no tab highlighted.
  useEffect(() => {
    if (!(asideActive in ASIDE_META)) setAsideActive("agents");
  }, [asideActive, setAsideActive]);


  // Inherited context loads when its panel is opened, and stays fresh on
  // thread changes since agents can record learnings mid-turn.
  useEffect(() => {
    if (asideActive === "inherited") loadInherited();
  }, [asideActive, activeId, loadInherited]);

  // Remember composer drafts across restarts; empty drafts are dropped.
  useEffect(() => {
    try {
      const kept = Object.fromEntries(
        Object.entries(drafts)
          .map(([k, v]) => [k, expandPastes(v, pastes[k])] as const)
          .filter(([, v]) => v !== ""),
      );
      window.localStorage.setItem("at.drafts", JSON.stringify(kept));
    } catch {
      /* private mode: stay in memory */
    }
  }, [drafts, pastes]);

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
      if (l && activeIdRef.current === activeId) setLogs(l);
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

  // The master orchestrator (lead) runs on the Orchestrator role's runtime
  // unless it is pinned, in which case the pin is the persisted lead setting.
  const leadSettings = settings?.lead ?? {};
  const orchestratorRole = settings?.roles.find((r) => r.id === "orchestrator");
  const leadHarness = leadSettings.harness || orchestratorRole?.harness || "";

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
  /** The name a question card shows for the agent that asked it. */
  const agentLabel = (agentId: string) => {
    const a = thread?.agents?.[agentId];
    return a ? a.name || rolesById[a.role] || a.role : "Agent";
  };

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
  // Each role's effective runtime from settings, with what it is doing in this thread.
  const roleRows = useMemo(
    () =>
      (settings?.roles ?? []).map((r) => {
        const mine = agents.filter((a) => a.role === r.id);
        const running = mine.filter((a) => a.status === "running");
        const latest = mine[mine.length - 1];
        const tokens = mine.reduce((sum, a) => {
          const usage = thread?.usage?.[a.id]?.usage;
          return usage ? sum + totalTokens(usage) : sum;
        }, 0);
        return {
          ...r,
          mine,
          running,
          done: mine.filter((a) => a.status !== "running" && a.status !== "pending").length,
          tokens,
          latestId: latest?.id ?? null,
          latestStatus: latest?.status ?? "pending",
          current: running[running.length - 1] ?? null,
        };
      }),
    [settings, agents, thread],
  );
  const detailAgent = agents.find((a) => a.id === detailId) ?? null;
  const comments = thread?.reviewComments ?? [];
  /** Review threads by file|side|line so each diff line finds its thread without a scan. */
  const reviewAnchors = useMemo(() => {
    const map = new Map<string, ReviewThreadLike[]>();
    for (const t of thread?.reviewThreads ?? []) {
      const key = reviewAnchorKey(t.file, t.side ?? "new", t.line);
      const list = map.get(key);
      if (list) list.push(t);
      else map.set(key, [t]);
    }
    return map;
  }, [thread?.reviewThreads]);
  const reviewPeople = useMemo(
    () => ({ agents: thread?.agents ?? null, roleNames: rolesById }),
    [thread?.agents, rolesById],
  );
  const pendingComments = useMemo(() => comments.filter((c) => !c.submitted), [comments]);
  const tasks = thread?.tasks ?? [];
  const running = useMemo(() => agents.filter((a) => a.status === "running"), [agents]);
  const selectedProject = projects.find((p) => p.id === projectId) ?? null;
  const [threadFilter, setThreadFilter] = useState("");
  // Threads whose subthreads are hidden in the sidebar. Clicking a thread that
  // has subthreads toggles this, so a long tree can be folded away.
  const [collapsedThreads, setCollapsedThreads] = useState<ReadonlySet<string>>(() => new Set());
  const toggleSubthreads = (id: string) =>
    setCollapsedThreads((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const query = threadFilter.trim();
  const projectThreads = useMemo(() => {
    const scoped = threads.filter((t) => t.projectId === projectId);
    if (query === "") return arrangeThreads(scoped);
    const ranked: { thread: ThreadSummary; score: number }[] = [];
    for (const thread of scoped) {
      const score = threadSearchScore(thread, query);
      if (score !== null) ranked.push({ thread, score });
    }
    // Array.sort is stable, so equal scores keep the daemon's recency order.
    ranked.sort((a, b) => b.score - a.score);
    return arrangeThreads(ranked.map((r) => r.thread));
  }, [threads, projectId, query]);

  // The board is an alternative to the card list, not a replacement: the choice
  // is remembered, and both views read the same thread list.
  const [threadView, setThreadViewState] = useState<ThreadView>(loadThreadView);
  const setThreadView = (view: ThreadView) => {
    setThreadViewState(view);
    try {
      window.localStorage.setItem("at.threadView", view);
    } catch {
      /* not persisted: the choice lasts this session */
    }
  };
  const [threadStates, setThreadStates] = useState<Record<string, BoardThreadState>>({});
  const openBoardThread = useCallback(
    (id: string) => {
      setActiveId(id);
      if (projectId) lastThreadByProject.current[projectId] = id;
    },
    [projectId],
  );
  // Only threads with a worktree can have a branch PR. The joined id string keeps
  // the poll from restarting when unrelated thread fields change.
  const boardPRIds = useMemo(
    () => projectThreads.filter((t) => t.worktree).map((t) => t.id).join(","),
    [projectThreads],
  );
  useEffect(() => {
    if (threadView !== "board" || boardPRIds === "") return;
    let live = true;
    const load = () => {
      rpcRef.current
        ?.call<Record<string, BoardThreadState>>("pr.states", { threadIds: boardPRIds.split(",") })
        .then((next) => {
          if (live) setThreadStates((prev) => mergeThreadStates(prev, next));
        })
        .catch(() => undefined);
    };
    load();
    const timer = setInterval(load, THREAD_PR_POLL_MS);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [threadView, boardPRIds]);
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
    const threadId = activeId;
    const text = expandPastes(input, pastes[draftKey]).trim();
    if (!rpc || !threadId || !text) return;
    // A registered "/name" runs its command. Commands never start or steer a
    // turn; an unknown "/word" is sent as ordinary text.
    const slash = parseSlash(text);
    const slashCmd = slash && slashCommands().find((c) => c.name === slash.name);
    if (slash && slashCmd) {
      setInput("");
      setPastes((prev) => {
        const rest = { ...prev };
        delete rest[draftKey];
        return rest;
      });
      slashCmd.run(slash.arg);
      return;
    }
    // A turn already running on this thread takes the message as a steer, so
    // Enter never drops a message while the team is working.
    const steer = running.length > 0 || busy;
    // The ref closes the window before React re-renders, so a second Enter cannot
    // submit the same draft again. Steers get their own key: a turn's RPC stays in
    // flight for the whole run, so a turn key would swallow every mid-run steer.
    const sendKey = steer ? `steer:${threadId}` : threadId;
    if (sendingRef.current.has(sendKey)) return;
    // "! cmd" runs a shell command in the thread's project. A lone "!" is just text.
    const command = text.startsWith("!") ? text.slice(1).trim() : "";
    if (command && steer) {
      setError("Stop the running turn before running a shell command.");
      return;
    }
    const sentPastes = pastes[draftKey];
    // A shell command takes no context; its attachments stay in the draft.
    const sentFiles = command ? [] : (attachments[draftKey] ?? []);
    setInput("");
    setPastes((prev) => {
      const rest = { ...prev };
      delete rest[draftKey];
      return rest;
    });
    if (sentFiles.length > 0) {
      setAttachments((prev) => {
        const rest = { ...prev };
        delete rest[draftKey];
        return rest;
      });
    }
    const detached = !command && !steer && (runInBackground || !!conn?.attached);
    sendingRef.current.add(sendKey);
    if (!steer) setTurnBusy(threadId, true);
    if (detached) noteDetachedTurn(threadId);
    if (command) setShellThread(threadId);
    try {
      if (command) {
        await rpc.call("thread.runCommand", { threadId, command });
      } else if (steer) {
        await rpc.call("thread.steer", {
          threadId,
          text,
          attachments: sentFiles.map((a) => a.path),
        });
      } else {
        const params: Record<string, unknown> = {
          threadId,
          text,
          attachments: sentFiles.map((a) => a.path),
        };
        // Detached turns run on the daemon's lifetime context: closing the
        // window cannot cancel them. An external daemon always detaches.
        if (runInBackground || conn?.attached) params.detach = true;
        await rpc.call("thread.turn.start", params);
      }
    } catch (e) {
      // Put the typed text back into its own thread's draft, even if the user has moved on.
      const key = draftKeyOf(projectId, threadId);
      setDrafts((prev) => ({ ...prev, [key]: input }));
      setPastes((prev) => ({ ...prev, [key]: sentPastes ?? {} }));
      if (sentFiles.length > 0) setAttachments((prev) => ({ ...prev, [key]: sentFiles }));
      setError(e instanceof Error ? e.message : String(e), threadId);
      // A failed start left no turn behind: retract the busy flag now instead of
      // waiting for it to end.
      if (!steer) {
        delete detachedTurns.current[threadId];
        setTurnBusy(threadId, false);
      }
    } finally {
      sendingRef.current.delete(sendKey);
      // A finished steer must not clear the busy flag of the turn it joined, and
      // a detached turn replies before any agent exists: keep it busy until
      // settleDetachedTurns sees the turn end.
      if (!steer && !detached) setTurnBusy(threadId, false);
      setShellThread((cur) => (cur === threadId ? null : cur));
      refresh(threadId).catch(() => undefined);
    }
  };

  // A graph group's prompt goes to its thread the way a chat message does: a new
  // turn, or a steer while one runs. The chat tab shows the result.
  const sendGroup = async (threadId: string, text: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    const steer = running.length > 0 || busy;
    const detached = !steer && (runInBackground || !!conn?.attached);
    if (!steer) setTurnBusy(threadId, true);
    if (detached) noteDetachedTurn(threadId);
    setTab("chat");
    try {
      const params: Record<string, unknown> = { threadId, text };
      if (detached) params.detach = true;
      await rpc.call(steer ? "thread.steer" : "thread.turn.start", params);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e), threadId);
      if (!steer) {
        delete detachedTurns.current[threadId];
        setTurnBusy(threadId, false);
      }
    } finally {
      if (!steer && !detached) setTurnBusy(threadId, false);
      refresh(threadId).catch(() => undefined);
    }
  };

  const toggle = async (field: "autoMode" | "peerMailbox", value: boolean) => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId) return;
    const method = field === "autoMode" ? "thread.setAutoMode" : "thread.setPeerMailbox";
    await rpc.call(method, { threadId: activeId, enabled: value });
    refresh(activeId).catch(() => undefined);
  };

  /** Sets how often a running turn reports workflow status; 0 turns it off. */
  const setStatusPeriod = async (seconds: number) => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId) return;
    try {
      await rpc.call("thread.setStatusPeriod", { threadId: activeId, seconds });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
    refresh(activeId).catch(() => undefined);
  };

  const [addingLead, setAddingLead] = useState(false);

  /** Grows the lead team by one from the next turn on. */
  const addLead = async (count: number) => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId || addingLead) return;
    setAddingLead(true);
    try {
      await rpc.call("thread.setLeads", { threadId: activeId, count });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setAddingLead(false);
    }
    refresh(activeId).catch(() => undefined);
  };

  /** Sets one lead's runtime override from the next turn on. */
  const setLeadRuntime = async (index: number, rt: LeadRuntimeView) => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId) return;
    try {
      await rpc.call("thread.setLeadRuntime", {
        threadId: activeId,
        index,
        harness: rt.harness ?? "",
        model: rt.model ?? "",
        variant: rt.variant ?? "",
      });
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

  const updateCouncilMember = (idx: number, patch: Partial<CouncilMemberRow>) => {
    setCouncilMembers((prev) => prev.map((m, i) => (i === idx ? { ...m, ...patch } : m)));
  };

  /** Convenes the council: members debate in parallel rounds, then a
   *  facilitator synthesizes the outcome into the thread. */
  const startCouncil = async () => {
    const rpc = rpcRef.current;
    const topic = councilTopic.trim();
    const threadId = activeId;
    if (!rpc || !threadId || !topic || turnThreads[threadId]) return;
    setInput("");
    setTurnBusy(threadId, true);
    try {
      await rpc.call("thread.council.start", {
        threadId,
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
      setError(e instanceof Error ? e.message : String(e), threadId);
    } finally {
      setTurnBusy(threadId, false);
      refresh(threadId).catch(() => undefined);
    }
  };

  const addComment = async () => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId || !draft || !draft.body.trim()) return;
    await rpc.call("review.comment", {
      threadId: activeId,
      file: draft.file,
      line: draft.line,
      endLine: draft.endLine,
      side: draft.side,
      body: draft.body,
    });
    setDraft(null);
    refresh(activeId).catch(() => undefined);
  };

  /** Hands every pending review comment to the team as one review turn. */
  const submitReview = async () => {
    const rpc = rpcRef.current;
    const threadId = activeId;
    if (!rpc || !threadId || pendingComments.length === 0 || turnThreads[threadId]) return;
    setTurnBusy(threadId, true);
    try {
      await rpc.call("review.submit", { threadId });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e), threadId);
    } finally {
      setTurnBusy(threadId, false);
      refresh(threadId).catch(() => undefined);
    }
  };

  const toggleModels = () => {
    // A collapsed pane opens to the Models tab; an open one keeps the toggle.
    if (!layout.asideOpen) {
      setLayout((l) => ({ ...l, asideOpen: true }));
      openPanel("models");
      return;
    }
    if (modelsOpen) closePanel("models");
    else openPanel("models");
  };
  useEffect(() => {
    // Role config lives in settings, which is only fetched on demand — load it
    // the first time the Models panel opens, same trio the settings sheet uses.
    if (!modelsOpen || settings) return;
    (async () => {
      try {
        const [s] = await Promise.all([loadSettings(), loadHarnesses()]);
        const used = Array.from(new Set((s?.roles ?? []).map((r) => r.harness)));
        await Promise.all(used.map((h) => refreshModels(h)));
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      }
    })();
  }, [modelsOpen, settings]);

  // /btw side answers, one per thread (ephemeral: never a thread event or message).
  const [btw, setBtw] = useState<Record<string, BtwEntry>>({});
  const btwSeq = useRef(0);
  // Slash popup: highlighted row, and the input it was dismissed on with Esc.
  const [slashIdx, setSlashIdx] = useState(0);
  const [slashClosed, setSlashClosed] = useState<string | null>(null);

  /** Asks the daemon a side question. It runs beside any running turn: no turn
   * slot, no steer, no message. Only the newest ask on a thread may settle. */
  const askBtw = (question: string) => {
    const rpc = rpcRef.current;
    const threadId = activeIdRef.current;
    const q = question.trim();
    if (!rpc || !threadId) return;
    if (!q) {
      setError("Usage: /btw <question>");
      return;
    }
    const id = ++btwSeq.current;
    const settle = (patch: Partial<BtwEntry>) =>
      setBtw((prev) => {
        const cur = prev[threadId];
        if (!cur || cur.id !== id) return prev;
        return { ...prev, [threadId]: { ...cur, ...patch } };
      });
    setBtw((prev) => ({ ...prev, [threadId]: { id, question: q, answer: null, error: null } }));
    rpc
      .call<{ answer: string }>("thread.btw", { threadId, question: q })
      .then((r) => settle({ answer: r.answer }))
      .catch((e: unknown) => settle({ error: e instanceof Error ? e.message : String(e) }));
  };

  const closeBtw = useCallback(() => {
    const threadId = activeIdRef.current;
    if (!threadId) return;
    setBtw((prev) => {
      if (!(threadId in prev)) return prev;
      const next = { ...prev };
      delete next[threadId];
      return next;
    });
  }, []);

  // Built per use: every action is an arrow, so it resolves the handler at call time.
  const slashCommands = (): SlashCommand[] =>
    buildCommands<AsideTabId>({
      panels: ASIDE_META,
      openPanel: (id) => openPanel(id),
      setTab: (t) => setTab(t),
      openSettings: () => openSettings(),
      askBtw: (q) => askBtw(q),
      toggleAutoMode: () => thread && toggle("autoMode", !thread.autoMode),
      togglePeerMailbox: () => thread && toggle("peerMailbox", !thread.peerMailbox),
      stopTurn: () => stopTurn(),
      newThread: () => newThread(),
      saveDefaults: () => saveThreadDefaults(),
    });
  const slashQ = slashQuery(input);
  const slashMatches: SlashCommand[] =
    slashQ !== null && slashClosed !== input ? matchCommands(slashCommands(), slashQ) : [];
  const slashOpen = slashMatches.length > 0;
  const slashSel = Math.min(slashIdx, slashMatches.length - 1);
  const btwEntry = activeId ? (btw[activeId] ?? null) : null;

  /** Tab and Enter on a command that takes text complete its name; a bare
   * command runs at once (Enter or click). */
  const pickSlash = (cmd: SlashCommand, runNow: boolean) => {
    if (cmd.args || !runNow) {
      setInput(`/${cmd.name}${cmd.args ? " " : ""}`);
      return;
    }
    setInput("");
    cmd.run("");
  };

  const taRef = useRef<HTMLTextAreaElement>(null);
  // Thread whose pre-run setup the user confirmed; the composer stays hidden until then.
  const [setupReadyId, setSetupReadyId] = useState<string | null>(null);
  // One playful verb per thread's setup view, picked when the thread changes.
  const startVerb = useMemo(() => START_VERBS[Math.floor(Math.random() * START_VERBS.length)], [activeId]);
  const pendingCaret = useRef<number | null>(null);
  const pasteSeq = useRef(0);
  // Grow the composer with its text (CSS max-height caps it), then restore any caret we placed.
  useLayoutEffect(() => {
    const el = taRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
    if (pendingCaret.current !== null) {
      el.setSelectionRange(pendingCaret.current, pendingCaret.current);
      pendingCaret.current = null;
    }
  }, [input]);

  const replacePasteToken = (t: { start: number; end: number; id: number }, text: string) => {
    setInput(input.slice(0, t.start) + text + input.slice(t.end));
    pendingCaret.current = t.start + text.length;
    setPastes((prev) => {
      const rest = { ...prev[draftKey] };
      delete rest[t.id];
      return { ...prev, [draftKey]: rest };
    });
  };
  const onComposerPaste = (e: ReactClipboardEvent<HTMLTextAreaElement>) => {
    const text = e.clipboardData.getData("text/plain");
    const lines = text.split("\n").length;
    if (lines <= 6 && text.length <= 500) return;
    e.preventDefault();
    const { selectionStart: start, selectionEnd: end } = e.currentTarget;
    const id = ++pasteSeq.current;
    const token = `[Pasted text #${id} +${lines} lines]`;
    setInput(input.slice(0, start) + token + input.slice(end));
    pendingCaret.current = start + token.length;
    setPastes((prev) => ({ ...prev, [draftKey]: { ...prev[draftKey], [id]: text } }));
  };
  const onComposerClick = (e: MouseEvent<HTMLTextAreaElement>) => {
    const pos = e.currentTarget.selectionStart;
    const t = pasteTokens(input).find((tok) => pos >= tok.start && pos <= tok.end);
    const text = t && pastes[draftKey]?.[t.id];
    if (t && text !== undefined) replacePasteToken(t, text);
  };
  // Dictated text lands after whatever is already typed.
  // Functional update: one onresult can carry several final phrases, and each
  // must build on the previous one rather than the render-time draft.
  const appendDictation = (text: string) => {
    const spoken = text.trim();
    if (!spoken || !activeId) return;
    setDrafts((prev) => {
      const cur = prev[draftKey] ?? "";
      const next = cur && !/\s$/.test(cur) ? `${cur} ${spoken}` : `${cur}${spoken}`;
      return { ...prev, [draftKey]: next };
    });
  };
  const voice = useHoldToTalk({
    enabled: !!activeId,
    onText: appendDictation,
    onUnavailable: (msg) => setError(msg),
  });
  const listening = voice.listening;
  // The textarea stays mounted (hidden while recording), so refocus it when a take ends.
  const wasListening = useRef(false);
  useEffect(() => {
    if (wasListening.current && !listening) taRef.current?.focus();
    wasListening.current = listening;
  }, [listening]);
  useRotatingPlaceholder(taRef, {
    rotate: tab === "chat" && !!activeId && running.length === 0 && !input,
    idle:
      running.length > 0
        ? "Steer the orchestrator — it lands on the next pass…"
        : "Ask the team to build something, or ! to run a shell command…",
  });
  // The @ menu: an @-token ends at the caret, and is closed for commands.
  const draftAttachments = attachments[draftKey] ?? [];
  const mention = commandMode ? null : mentionAt(input, caret);
  const openMention = mention && mention.start !== mentionDismissed ? mention : null;
  const mentionQuery = openMention?.query ?? null;
  const menuMatches =
    openMention && mentionResults?.query === openMention.query ? mentionResults.matches : null;
  useEffect(() => {
    const rpc = rpcRef.current;
    if (mentionQuery === null || !rpc || !activeId) return;
    let stale = false;
    // Debounced, and any keystroke after this one cancels the request.
    const timer = window.setTimeout(() => {
      rpc
        .call<{ matches: FileMatch[] }>("files.search", {
          threadId: activeId,
          query: mentionQuery,
          limit: 12,
        })
        .then((res) => {
          if (!stale) setMentionResults({ query: mentionQuery, matches: res.matches });
        })
        .catch((e) => {
          if (stale) return;
          setMentionResults({ query: mentionQuery, matches: [] });
          setError(e instanceof Error ? e.message : String(e));
        });
    }, 60);
    return () => {
      stale = true;
      window.clearTimeout(timer);
    };
  }, [mentionQuery, activeId]);
  const pickMention = (m: FileMatch) => {
    if (!openMention) return;
    if (draftAttachments.length >= MAX_ATTACHMENTS) {
      setError(`Attach at most ${MAX_ATTACHMENTS} files or folders per turn.`);
      return;
    }
    setInput(dropMention(input, openMention, caret));
    pendingCaret.current = openMention.start;
    setCaret(openMention.start);
    setMentionDismissed(null);
    setAttachments((prev) => {
      const list = prev[draftKey] ?? [];
      if (list.some((a) => a.path === m.path)) return prev;
      return { ...prev, [draftKey]: [...list, { path: m.path, dir: m.dir }] };
    });
  };
  const removeAttachment = (path: string) =>
    setAttachments((prev) => ({
      ...prev,
      [draftKey]: (prev[draftKey] ?? []).filter((a) => a.path !== path),
    }));

  const onComposerKeyDown = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    // Enter confirms an IME candidate during composition; it must not submit.
    if (e.nativeEvent.isComposing || e.keyCode === 229) return;
    if (slashOpen) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const n = slashMatches.length;
        setSlashIdx((slashSel + (e.key === "ArrowDown" ? 1 : -1) + n) % n);
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        setSlashClosed(input);
        return;
      }
      if (e.key === "Tab" || (e.key === "Enter" && !e.shiftKey)) {
        e.preventDefault();
        pickSlash(slashMatches[slashSel], e.key === "Enter");
        return;
      }
    }
    if (openMention && menuMatches && menuMatches.length > 0) {
      const n = menuMatches.length;
      // Clamp like the render does: a stale index from a longer list would otherwise stall the wrap.
      const cur = Math.min(mentionActive, n - 1);
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        setMentionActive(e.key === "ArrowDown" ? (cur + 1) % n : (cur - 1 + n) % n);
        return;
      }
      if (e.key === "Tab" || (e.key === "Enter" && !e.shiftKey)) {
        e.preventDefault();
        pickMention(menuMatches[cur]);
        return;
      }
    }
    if (openMention && e.key === "Escape") {
      e.preventDefault();
      setMentionDismissed(openMention.start);
      return;
    }
    // A token is one unit: Backspace/Delete at its edge removes it whole.
    const { selectionStart: pos, selectionEnd } = e.currentTarget;
    if ((e.key === "Backspace" || e.key === "Delete") && pos === selectionEnd) {
      const t = pasteTokens(input).find((tok) =>
        e.key === "Backspace" ? tok.end === pos : tok.start === pos,
      );
      if (t) {
        e.preventDefault();
        replacePasteToken(t, "");
        return;
      }
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      send();
    }
  };

  /** Makes the view on screen the default for threads that have not set their own. */
  const saveThreadDefaults = () => {
    setTabDefault(tab);
    setAsideTabsDefault(asideTabs);
    setAsideActiveDefault(asideActive);
    setLayoutDefault(layout);
    setCouncilSetupDefault(councilSetup);
  };

  const openSettings = () => {
    setSettingsOpen(true);
    (async () => {
      try {
        const [s] = await Promise.all([
          loadSettings(),
          loadHarnesses(),
          loadProjects(),
          loadAppInfo(),
          loadDeps(),
        ]);
        const used = Array.from(new Set((s?.roles ?? []).map((r) => r.harness)));
        await Promise.all(used.map((h) => refreshModels(h)));
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      }
    })();
  };

  /** Persists the shell's keep-running preference; a background daemon makes
   * detached turns the right default. */
  const toggleKeepRunning = (enabled: boolean) => {
    setKeepRunningState(enabled);
    emitKeepRunning(enabled);
    if (enabled) setRunInBackground(true);
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
      approvedModels,
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

  /** Pins the master orchestrator's runtime; empty fields inherit the role. */
  const persistLeadRuntime = async (runtime: LeadRuntimeView) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    const s = await rpc.call<SettingsView>("settings.set", {
      lead: {
        harness: runtime.harness ?? "",
        model: runtime.model ?? "",
        variant: runtime.variant ?? "",
      },
    });
    setSettings(s);
    flashSaved();
  };

  // Required roles cannot be disabled; the daemon refuses them too.
  const toggleRoleEnabled = async (row: RoleConfigView, enabled: boolean) => {
    const rpc = rpcRef.current;
    if (!rpc || row.required) return;
    updateRole(row.id, { enabled });
    try {
      const s = await rpc.call<SettingsView>("settings.set", {
        roleEnabled: { id: row.id, enabled },
      });
      setSettings(s);
      flashSaved();
    } catch {
      // Reload so the switch shows what the daemon actually holds.
      loadSettings().catch(() => undefined);
    }
  };

  const toggleExperimentalRoles = async (enabled: boolean) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    const s = await rpc.call<SettingsView>("settings.set", { experimentalRoles: enabled });
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
      <div className="title-meta">
        <div className={`conn ${status}`}>
          <span className="led" />
          <span>
            {enabledHarnesses.length} harness
            {enabledHarnesses.length === 1 ? "" : "es"} · {roles.length} roles
          </span>
        </div>
        {conn && (
          <div
            className={`daemon-badge ${conn.attached ? "attached" : "local"}`}
            title={
              conn.attached
                ? `Attached to a daemon this window did not start (${conn.host || "unknown"})`
                : "The daemon runs inside this app"
            }
          >
            <span className="led" />
            {conn.attached ? (
              <>
                attached <span className="host">{conn.host || "remote"}</span>
              </>
            ) : (
              "local daemon"
            )}
          </div>
        )}
      </div>
      <UpdateChip
        status={update}
        busy={updateBusy}
        error={updateError}
        onInstall={installUpdate}
      />
      <HarnessUpdateChip
        report={harnessUpdates}
        busy={harnessUpdateBusy}
        error={harnessUpdateError}
        onUpdate={updateHarnesses}
      />
    </div>
  );

  // Open projects live in a persistent tab strip, so switching projects is one
  // click and never loses what the other project was doing.
  // Home pages (Projects, Orchestrator) sit in the strip even with no project open.
  const homeTabOn = (page: HomePage) => projectId === null && homePage === page;
  const projectTabs = (
    <div className="project-tabs">
      <div
        className={`ptab pt-home${homeTabOn("projects") ? " on" : ""}`}
        {...tabProps(homeTabOn("projects"), showProjects)}
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
      <div
        className={`ptab pt-home${homeTabOn("orchestrator") ? " on" : ""}`}
        {...tabProps(homeTabOn("orchestrator"), () => showHome("orchestrator"))}
        title="Master orchestrator"
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
          <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />
        </svg>
        Orchestrator
      </div>
      <div
        className={`ptab pt-home${homeTabOn("stats") ? " on" : ""}`}
        {...tabProps(homeTabOn("stats"), () => showHome("stats"))}
        title="Harness stats across all projects"
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
          <path d="M4 20V10M10 20V4M16 20v-7M22 20H2" />
        </svg>
        Stats
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
      <button className="pt-add" onClick={showProjects} title="Projects" aria-label="Projects">
        +
      </button>
    </div>
  );

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
  const sentComments = comments.length - pendingComments.length;
  const reviewComments = (
    <div className="section">
      <h3>
        Review comments <span>{pendingComments.length}</span>
      </h3>
      {pendingComments.length === 0 && (
        <p className="hint">None pending. Comment on a line in Review or the Diff panel.</p>
      )}
      {pendingComments.map((c) => (
        <div className="comment-item" key={c.id}>
          <div className="comment-file">
            {c.file}:{lineRange(c.line, c.endLine)}
          </div>
          <div className="comment-body">{c.body}</div>
        </div>
      ))}
      {pendingComments.length > 0 && (
        <button
          className="primary"
          disabled={busy || running.length > 0}
          onClick={() => submitReview().catch(() => undefined)}
        >
          Submit review
        </button>
      )}
      {pendingComments.length > 0 && running.length > 0 && (
        <p className="hint">Submit once the running turn finishes.</p>
      )}
      {sentComments > 0 && <p className="hint">{sentComments} already sent to the team.</p>}
    </div>
  );

  // Stats are their own page, not part of the projects grid.
  if (!projectId && homePage === "stats") {
    return (
      <div className="screen">
        {titlebar}
        {projectTabs}
        <div className="screen-body">
          <div className="screen-head">
            <div className="screen-title">
              <h2>Stats</h2>
              <p className="screen-sub">Across all projects</p>
            </div>
          </div>
          <StatsPage rpc={rpcRef.current} />
        </div>
      </div>
    );
  }

  // The master orchestrator has its own page; it never shares the projects grid.
  if (!projectId && homePage === "orchestrator") {
    return (
      <div className="screen">
        {titlebar}
        {projectTabs}
        <div className="screen-body master-page">
          <MasterChat
            rpc={rpcRef.current}
            projects={projects}
            onOpenThread={(projectId, threadId) => {
              openProject(projectId);
              lastThreadByProject.current[projectId] = threadId;
              setActiveId(threadId);
            }}
          />
        </div>
      </div>
    );
  }

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
          </div>
          <div className="project-grid">
            <div
              className="new-card"
              aria-label="Add project"
              title="Add project"
              {...clickable(addProject)}
            >
              <span className="new-card-plus" aria-hidden="true">
                +
              </span>
            </div>
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
                placeholder="Search threads…"
                value={threadFilter}
                onChange={(e) => setThreadFilter(e.target.value)}
              />
              <div className="view-toggle" role="group" aria-label="Thread layout">
                <button
                  className={threadView === "list" ? "primary" : undefined}
                  aria-pressed={threadView === "list"}
                  onClick={() => setThreadView("list")}
                >
                  List
                </button>
                <button
                  className={threadView === "board" ? "primary" : undefined}
                  aria-pressed={threadView === "board"}
                  onClick={() => setThreadView("board")}
                >
                  Board
                </button>
              </div>
              {threadView === "board" && <button onClick={newThread}>New thread</button>}
              <button
                className={showWorktrees ? "primary" : undefined}
                aria-pressed={showWorktrees}
                onClick={() => setShowWorktrees((v) => !v)}
              >
                Worktrees
              </button>
            </div>
          </div>
          {showWorktrees && (
            <div className="section worktrees-section">
              <WorktreesPanel
                projects={selectedProject ? [{ id: selectedProject.id, name: selectedProject.name }] : []}
                worktrees={worktrees}
                threads={threads}
                loading={worktreesLoading}
                error={worktreesError}
                onRefresh={() => projectId && loadWorktrees([projectId]).catch(() => undefined)}
                onOpenThread={openWorktreeThread}
                onRemove={(projectId, path, force) =>
                  worktreeAction(projectId, "worktree.remove", { path, force })
                }
                onPrune={(projectId) => worktreeAction(projectId, "worktree.prune", {})}
              />
            </div>
          )}
          {threadView === "list" && projectThreads.length === 0 && query !== "" && (
            <div className="empty">No threads match that filter.</div>
          )}
          {threadView === "board" ? (
            <ThreadBoard
              threads={projectThreads}
              states={threadStates}
              formatUpdated={fmtUpdated}
              onOpen={openBoardThread}
              onArchive={(ids, archived) => void setThreadsArchived(ids, archived)}
              onDelete={(ids) => void deleteThreadsById(ids)}
            />
          ) : (
          <div className="thread-cards">
            <div
              className="new-card"
              aria-label="New thread"
              title="New thread"
              {...clickable(newThread)}
            >
              <span className="new-card-plus" aria-hidden="true">
                +
              </span>
            </div>
            {projectThreads.map((t) => (
              <div
                className={`thread-card${t.stale ? " is-stale" : ""}`}
                key={t.id}
                style={t.depth ? { marginLeft: t.depth * 16 } : undefined}
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
          )}
          {threadOverlays}
        </div>
      </div>
    );
  }

  // A new thread shows its setup until the user presses Start; then the setup
  // view and the hidden composer both give way to the normal empty chat.
  const setupPending =
    tab === "chat" && !!thread && (thread.messages?.length ?? 0) === 0 && running.length === 0;
  const setupMode = setupPending && setupReadyId !== activeId;
  const composerHidden = setupMode;
  const asideCollapsed = !layout.asideOpen || setupMode;

  return (
    <div
      className={`app${layout.sidebarOpen ? "" : " sidebar-closed"}${asideCollapsed ? " aside-closed" : ""}`}
      ref={shellRef}
      style={
        {
          "--sidebar-w": `${layout.sidebarW}px`,
          "--aside-w": `${layout.asideW}px`,
        } as CSSProperties
      }
    >
      {titlebar}
      {projectTabs}

      <div className={`col sidebar${layout.sidebarOpen ? "" : " collapsed"}`}>
        <div className="col-head">
          <PanelToggle
            side="left"
            open={layout.sidebarOpen}
            onToggle={() => setLayout((l) => ({ ...l, sidebarOpen: !l.sidebarOpen }))}
          />
          <button onClick={newThread}>New</button>
        </div>
        <div className="sidebar-search">
          <input
            className="thread-filter"
            placeholder="Search threads…"
            value={threadFilter}
            onChange={(e) => setThreadFilter(e.target.value)}
          />
        </div>
        <div className="col-body">
          {projectThreads.length === 0 && (
            <div className="empty">
              {query !== "" ? "No threads match that search." : "No threads yet"}
            </div>
          )}
          {visibleThreadRows(projectThreads, collapsedThreads, (t) => t.id).map((t) => (
            <div
              key={t.id}
              className={`thread-row ${t.id === activeId ? "active" : ""} ${t.stale ? "stale" : ""}`}
              style={t.depth ? { marginLeft: t.depth * 16 } : undefined}
              {...clickable(() => {
                if (t.subthreads > 0) toggleSubthreads(t.id);
                selectThread(t.id);
              })}
              onContextMenu={(e) => openThreadMenu(t, e)}
            >
              <div className="t" title={t.title}>
                {t.title}
                {t.subthreads > 0 && (
                  <span className="sub-caret" aria-label={collapsedThreads.has(t.id) ? "Show subthreads" : "Hide subthreads"}>
                    {collapsedThreads.has(t.id) ? "▸" : "▾"} {t.subthreads}
                  </span>
                )}
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
          <LimitsMeter rpc={rpcRef.current} />
          <RemoteSection rpc={rpcRef.current} />
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

      <SplitHandle
        side="left"
        width={layout.sidebarW}
        min={PANEL_BOUNDS.sidebar.min}
        max={PANEL_BOUNDS.sidebar.max}
        shellRef={shellRef}
        disabled={!layout.sidebarOpen}
        onCommit={(px) => setLayout((l) => ({ ...l, sidebarW: px }))}
      />

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
            {projectId && (
              <button title="Open this project's board" onClick={showProjectBoard}>
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
                  <rect x="3" y="4" width="5" height="16" rx="1.5" />
                  <rect x="10" y="4" width="5" height="11" rx="1.5" />
                  <rect x="17" y="4" width="4" height="7" rx="1.5" />
                </svg>
                Project board
              </button>
            )}
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
            <div
              className="stream"
              ref={streamRef}
              onScroll={(e) => setAtBottom(nearBottom(e.currentTarget))}
            >
              {setupMode && thread && (
                <div className="setup-view">
                  <ThreadSetup
                    settings={settings}
                    thread={thread}
                    toggle={toggle}
                    addLead={addLead}
                    addingLead={addingLead}
                    setLeadRuntime={setLeadRuntime}
                    updateRole={updateRole}
                    persistRole={persistRole}
                    refreshModels={refreshModels}
                    addModel={addModel}
                    modelOptions={modelOptions}
                    harnessOptionsFor={harnessOptionsFor}
                    toggleRoleEnabled={toggleRoleEnabled}
                    setStatusPeriod={setStatusPeriod}
                  />
                  {composerHidden && (
                    <button
                      type="button"
                      className="setup-continue"
                      onClick={() => setSetupReadyId(activeId)}
                    >
                      Start {startVerb}...
                    </button>
                  )}
                </div>
              )}
              {!setupMode && thread && (thread.messages?.length ?? 0) === 0 && running.length === 0 && (
                <div className="stream-empty">
                  <img src="/brand-mark.svg" alt="" />
                  <div className="stream-empty-title">Start a thread</div>
                  <div className="stream-empty-sub">
                    Describe the task. The orchestrator will plan it and dispatch your team.
                  </div>
                </div>
              )}
              {groupTurns(thread?.messages ?? []).map((turn) => (
                <section className="turn" key={turn[0].id}>
                  {turn.map((m) => (
                  <div
                    key={m.id}
                    className={`msg ${m.author === "user" ? "user" : m.author === "steering" ? "steering" : m.author === "shell" ? "shell" : m.author === "status" ? "status" : ""}${m.author === "steering" && !m.consumed ? " pending" : ""}${isFailedBlock(m) ? " failed" : ""}`}
                  >
                    <div className="who">
                      <span className="role">
                        {m.author === "user"
                          ? "You"
                          : m.author === "steering"
                            ? m.consumed
                              ? "Steering"
                              : "Steering · queued"
                            : m.author === "system"
                              ? "System"
                              : m.author === "shell"
                                ? "Shell"
                                : m.author === "subthread"
                                  ? "Subthread"
                                  : m.author === "status"
                                    ? "Status"
                                    : "Orchestrator"}
                      </span>
                    </div>
                    <div className="msg-actions">
                      <button
                        onClick={() =>
                          navigator.clipboard.writeText(m.text).catch(() => setError("Could not copy"))
                        }
                        title="Copy this block"
                      >
                        Copy
                      </button>
                    </div>
                    <div className="body">
                      <Markdown text={m.text} />
                    </div>
                  </div>
                  ))}
                </section>
              ))}
              {(thread?.questions ?? []).map((q) => (
                <QuestionCard
                  key={q.questionId}
                  question={q}
                  askedBy={agentLabel(q.agentId)}
                  onAnswer={(selected, text) => answerQuestion(q.questionId, selected, text)}
                  onDismiss={() => dismissQuestion(q.questionId)}
                />
              ))}
              {running.length > 0 && (
                <div className={`activity agents${logsOpen ? "" : " collapsed"}`}>
                  <div className="act-head">
                    <span className="act-led" />
                    <span className="act-label">
                      Working —{" "}
                      {running.map((a) => a.name || rolesById[a.role] || a.role).join(", ")}
                    </span>
                    <Elapsed since={running[0]?.startedAt} />
                    <button
                      className="act-toggle"
                      onClick={toggleLogs}
                      aria-expanded={logsOpen}
                      aria-label={logsOpen ? "Hide logs" : "Show logs"}
                      title={logsOpen ? "Hide logs" : "Show logs"}
                    >
                      {logsOpen ? "▾" : "▸"} Logs
                    </button>
                    <button
                      className="stop-btn"
                      onClick={stopTurn}
                      title="Stop this turn — running agents are cancelled"
                    >
                      Stop
                    </button>
                  </div>
                  {logsOpen && (
                    <div className="act-body" ref={actBodyRef}>
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
                  )}
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
              {!atBottom && (
                <button
                  type="button"
                  className="scroll-bottom"
                  onClick={scrollToBottom}
                  aria-label="Scroll to latest message"
                  title="Scroll to latest"
                >
                  ↓
                </button>
              )}
            </div>

            <div
              className={`composer${commandMode ? " command-mode" : running.length > 0 ? " agent-mode" : ""}`}
              hidden={composerHidden}
            >
              {btwEntry && <BtwPanel entry={btwEntry} onClose={closeBtw} />}
              {slashOpen && (
                <SlashMenu
                  items={slashMatches}
                  index={slashSel}
                  onPick={(cmd) => pickSlash(cmd, true)}
                />
              )}
              {openMention && menuMatches && (
                <MentionMenu
                  matches={menuMatches}
                  active={Math.min(mentionActive, Math.max(menuMatches.length - 1, 0))}
                  onPick={pickMention}
                  onActive={setMentionActive}
                />
              )}
              {draftAttachments.length > 0 && (
                <div className="attach-row">
                  {draftAttachments.map((a) => (
                    <span key={a.path} className="attach-chip" title={a.path}>
                      <span className="attach-kind">{a.dir ? "folder" : "file"}</span>
                      <span className="attach-name">{a.dir ? `${a.path}/` : a.path}</span>
                      <button
                        type="button"
                        className="attach-remove"
                        onClick={() => removeAttachment(a.path)}
                        aria-label={`Remove ${a.path}`}
                      >
                        ×
                      </button>
                    </span>
                  ))}
                </div>
              )}
              <div className="box">
                <textarea
                  ref={taRef}
                  rows={1}
                  hidden={listening}
                  onPaste={onComposerPaste}
                  onClick={onComposerClick}
                  value={input}
                  onChange={(e) => {
                    setInput(e.target.value);
                    setSlashIdx(0);
                    setCaret(e.target.selectionStart);
                    setMentionActive(0);
                  }}
                  onSelect={(e) => setCaret(e.currentTarget.selectionStart)}
                  onKeyDown={onComposerKeyDown}
                />
                {listening && (
                  <VoiceRecorder
                    hint={`${HOLD_MOD_LABEL} + T`}
                    onStop={voice.stop}
                    onCancel={voice.cancel}
                  />
                )}
              </div>
              <div className="composer-foot">
                {selectedProject && (
                  <span className="ctx-chip" title={selectedProject.path}>
                    {selectedProject.name}
                  </span>
                )}
                {(conn?.attached || keepRunning) && (
                  <label
                    className={`bg-turn${runInBackground || conn?.attached ? " on" : ""}`}
                    title={
                      conn?.attached
                        ? "Attached to an external daemon: turns always run in the background"
                        : "The turn keeps running after you close the window"
                    }
                  >
                    <input
                      type="checkbox"
                      checked={runInBackground || !!conn?.attached}
                      disabled={!!conn?.attached}
                      onChange={(e) => setRunInBackground(e.target.checked)}
                    />
                    Run in background
                    <span className="bg-hint">keeps running after you close the window</span>
                  </label>
                )}
                <button
                  className={`model-btn${modelsOpen ? " open" : ""}`}
                  onClick={toggleModels}
                  aria-expanded={modelsOpen}
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
              </div>
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
              onLineClick={(file, line, endLine, side) =>
                setDraft({ file, line, endLine, side, body: "" })
              }
              onDraftBody={(body) => setDraft((d) => (d ? { ...d, body } : d))}
              onAddComment={() => addComment().catch(() => undefined)}
              onCancelDraft={() => setDraft(null)}
              onRefresh={() => loadDiff(activeId)}
              threads={reviewAnchors}
              people={reviewPeople}
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
            onSubmitGroup={sendGroup}
          />
        )}
      </div>

      <SplitHandle
        side="right"
        width={layout.asideW}
        min={PANEL_BOUNDS.aside.min}
        max={PANEL_BOUNDS.aside.max}
        shellRef={shellRef}
        disabled={asideCollapsed}
        onCommit={(px) => setLayout((l) => ({ ...l, asideW: px }))}
      />

      <div className={`col aside${asideCollapsed ? " collapsed" : ""}`}>
        <div className="aside-tabs" role="tablist" aria-label="Thread details">
          <PanelToggle
            side="right"
            open={!asideCollapsed}
            onToggle={() => {
              if (!setupMode) setLayout((l) => ({ ...l, asideOpen: !l.asideOpen }));
            }}
          />
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
          {/* Kept mounted while hidden so unsaved edits survive a tab switch. */}
          {activeId && (
            <div className="section files-section" hidden={asideActive !== "files"}>
              <FileEditor
                key={activeId}
                rpc={rpcRef.current}
                threadId={activeId}
                active={asideActive === "files"}
              />
            </div>
          )}
          {asideActive === "models" ? (
            <div className="aside-models">
              <ThreadSetup
                settings={settings}
                thread={thread}
                toggle={toggle}
                addLead={addLead}
                addingLead={addingLead}
                setLeadRuntime={setLeadRuntime}
                updateRole={updateRole}
                persistRole={persistRole}
                refreshModels={refreshModels}
                addModel={addModel}
                modelOptions={modelOptions}
                harnessOptionsFor={harnessOptionsFor}
                toggleRoleEnabled={toggleRoleEnabled}
                setStatusPeriod={setStatusPeriod}
              />
            </div>
          ) : asideActive === "agents" ? (
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
                <h3>
                  Roles{" "}
                  <span>
                    {roleRows.filter((r) => r.running.length > 0).length} active
                  </span>
                </h3>
                {!settings && <p className="hint">Loading roles…</p>}
                <ul className="roles">
                  {roleRows.map((r) => (
                    <li
                      key={r.id}
                      className={r.latestId ? "clickable" : undefined}
                      {...(r.latestId ? clickable(() => setDetailId(r.latestId)) : {})}
                    >
                      <div className="role-head">
                        <span className={`dot ${r.running.length > 0 ? "running" : r.latestStatus}`} />
                        <span className="role-name">{r.name}</span>
                        <span className="harness" title={`${r.harness} · ${r.model}`}>
                          {r.harness}
                          {r.model ? ` · ${r.model}` : ""}
                        </span>
                      </div>
                      <div className="role-meta">
                        {r.running.length > 0 ? `${r.running.length} running` : "idle"}
                        {r.mine.length > 0 &&
                          ` · ${r.done}/${r.mine.length} done · ${fmtTokens(r.tokens)} tok`}
                      </div>
                      {r.current && (
                        <div className="role-task" title={r.current.task}>
                          {r.current.task}
                        </div>
                      )}
                    </li>
                  ))}
                </ul>
                {settings?.autoMode && (
                  <p className="hint">Auto mode is on: the orchestrator picks each task's runtime.</p>
                )}
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
        ) : asideActive === "stats" ? (
          <div className="section stats-section">
            <HarnessStatsView
              key={activeId ?? ""}
              rpc={rpcRef.current}
              threadId={activeId}
              projectId={projectId}
            />
          </div>
        ) : asideActive === "pr" ? (
          <div className="section pr-section">
            <PRPanel
              listing={prState?.threadId === activeId ? prState : null}
              loading={prLoading}
              error={prError}
              onRefresh={() => loadPR(activeId).catch(() => undefined)}
            />
          </div>
        ) : asideActive === "files" ? null : asideActive === "council" ? (
          <div className="section council-section">
            {!activeId && <p className="hint">Open a thread to convene a council.</p>}
            <div className="council-pane">
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
                  disabled={!activeId || !councilTopic.trim() || councilMembers.length < 2 || busy}
                  onClick={startCouncil}
                >
                  Convene council
                </button>
              </div>
            </div>
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
                onLineClick={(file, line, endLine, side) =>
                  setDraft({ file, line, endLine, side, body: "" })
                }
                onDraftBody={(body) => setDraft((d) => (d ? { ...d, body } : d))}
                onAddComment={() => addComment().catch(() => undefined)}
                onCancelDraft={() => setDraft(null)}
                onRefresh={() => loadDiff(activeId)}
                threads={reviewAnchors}
                people={reviewPeople}
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
                <h4>New thread defaults</h4>
                <p className="hint">
                  New threads open with the tab, panels, layout, and council setup saved here. A
                  thread you change keeps its own view.
                </p>
                <div className="sheet-actions">
                  <button onClick={saveThreadDefaults}>Save this view as default</button>
                </div>
              </section>
              {!conn?.attached && (
                <section className="set-section">
                  <h4>Desktop</h4>
                  <p className="hint">
                    When this is on, closing the window leaves the daemon (and any running
                    turns) alive in the background so <code>at</code> can still reach it.
                    Turn it off to stop the daemon with the window.
                  </p>
                  <div
                    className={`toggle ${keepRunning ? "on" : ""}`}
                    {...switchProps(keepRunning, () => toggleKeepRunning(!keepRunning))}
                  >
                    <span className="sw" /> Keep daemon running after the window closes
                  </div>
                </section>
              )}
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
                <div className="field" style={{ marginTop: 12 }}>
                  <span>Approved models for auto mode</span>
                  <ApprovedModelsPicker
                    value={approvedModels}
                    onChange={setApprovedModels}
                    models={harnesses.models}
                    harnessOrder={harnesses.all}
                  />
                </div>
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
                <h4>Master orchestrator</h4>
                <p className="hint">
                  The team lead runs on this harness and model. Leave it on the Orchestrator role
                  to inherit that role's default; each thread can still override its own leads.
                </p>
                <div className="role-edit">
                  <span className="rn">Lead agent</span>
                  <Dropdown
                    value={leadSettings.harness ?? ""}
                    options={[
                      {
                        value: "",
                        label: `Orchestrator role (${orchestratorRole?.harness ?? "default"})`,
                      },
                      ...harnessOptionsFor(leadSettings.harness ?? ""),
                    ]}
                    onChange={(v) => {
                      persistLeadRuntime({ harness: v, model: "", variant: "" }).catch(
                        () => undefined,
                      );
                      if (v) refreshModels(v).catch(() => undefined);
                    }}
                  />
                  <Dropdown
                    value={leadSettings.model ?? ""}
                    options={modelOptions(leadHarness, leadSettings.model ?? "")}
                    searchable
                    align="end"
                    placeholder="model (default)"
                    footNote="Discovered from the harness CLI"
                    onRefresh={() => leadHarness && refreshModels(leadHarness).catch(() => undefined)}
                    addLabel="Add model"
                    onAdd={(m) => {
                      addModel(leadHarness, m)
                        .then(() =>
                          persistLeadRuntime({
                            harness: leadSettings.harness ?? "",
                            model: m,
                            variant: leadSettings.variant ?? "",
                          }),
                        )
                        .catch(() => undefined);
                    }}
                    onChange={(v) =>
                      persistLeadRuntime({
                        harness: leadSettings.harness ?? "",
                        model: v,
                        variant: leadSettings.variant ?? "",
                      }).catch(() => undefined)
                    }
                  />
                </div>
              </section>

              <section className="set-section">
                <h4>Dependencies</h4>
                <p className="hint">
                  The forge CLIs the Pull request tab shells out to. Versions are read from the
                  tools on PATH.
                </p>
                {depsError && <p className="hint warn">{depsError}</p>}
                {deps === null && !depsError && <p className="hint">Checking…</p>}
                {(deps ?? []).map((d) => (
                  <div className="dep-row" key={d.name}>
                    <span className="dn">{d.name}</span>
                    <span className={`dv ${d.status}`}>
                      {d.status === "missing"
                        ? "not installed"
                        : `${d.version || "unknown version"}${
                            d.status === "ok" ? "" : ` — ${d.minVersion} or newer required`
                          }`}
                    </span>
                    <span
                      className={`dot ${
                        d.status === "ok" ? "clean" : d.status === "outdated" ? "dirty" : "missing"
                      }`}
                      title={d.status}
                    />
                  </div>
                ))}
                <div className="sheet-actions">
                  <button onClick={() => loadDeps().catch(() => undefined)} disabled={depsBusy}>
                    {depsBusy ? "Checking…" : "Recheck"}
                  </button>
                </div>
              </section>

              <section className="set-section">
                <h4>Roles</h4>
                <p className="hint">
                  Each subagent role runs on its own harness and model; the Orchestrator role is the
                  master orchestrator above. Changes apply to the next turn.
                </p>
                <div
                  className={`toggle ${settings.experimentalRoles ? "on" : ""}`}
                  {...switchProps(settings.experimentalRoles, () =>
                    toggleExperimentalRoles(!settings.experimentalRoles).catch(() => undefined),
                  )}
                  style={{ marginBottom: 10 }}
                >
                  <span className="sw" /> Experimental roles
                </div>
                <p className="hint">
                  Experimental roles are off by default. Turn this on to dispatch them.
                </p>
                {modelsNotice && <p className="hint warn">{modelsNotice}</p>}
                {settings.roles
                  .filter((r) => r.id !== "orchestrator")
                  .map((r) => (
                    <div className="role-edit" key={r.id}>
                      <span className="rn">
                        {r.name}
                        {r.experimental && <span className="badge experimental">Experimental</span>}
                      </span>
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
                        align="end"
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

              <section className="set-section about-section">
                <h4>About</h4>
                <p className="hint">
                  AgentTunnel {appVersion ?? "dev"} · Built by Wael Rabah
                </p>
                <p className="hint">
                  <a href="https://github.com/WaelRabah">github.com/WaelRabah</a>
                </p>
              </section>
            </div>
          </div>
        </div>
      )}

      {threadOverlays}
    </div>
  );
}
