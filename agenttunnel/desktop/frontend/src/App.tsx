import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent,
  type ReactNode,
} from "react";
import { Window } from "@wailsio/runtime";
import {
  discover,
  inDesktopShell,
  pickFolder,
  rediscover,
  Rpc,
  setKeepRunning as emitKeepRunning,
  type Conn,
} from "./lib/rpc";
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
import { copyText as writeClipboard } from "./lib/clipboard";
import { ContextMenu, type MenuItem } from "./components/ContextMenu";
import { ThreadSetup } from "./components/ThreadSetup";
import { ThreadWizard } from "./components/ThreadWizard";
import { clickable, switchProps } from "./lib/switchProps";
import {
  PANEL_BOUNDS,
  PanelToggle,
  SplitHandle,
  clampWidth,
} from "./components/PanelSplit";
import { AgentGraph } from "./components/AgentGraph";
import { DrawerToggle } from "./components/DrawerToggle";
import { useDrawers } from "./lib/useDrawers";
import { PeerChat } from "./components/PeerChat";
import { useHoldToTalk } from "./lib/dictation";
import { KanbanBoard } from "./components/KanbanBoard";
import { TaskList, type TaskItem } from "./components/TaskList";
import { DiffView, lineRange, type DraftLike } from "./components/DiffView";
import { reviewAnchorKey, type ReviewThreadLike } from "./components/ReviewThreads";
import type { DropdownOption } from "./components/Dropdown";
import { FileEditor } from "./components/FileEditor";
import { PRPanel, type PRListing } from "./components/PRPanel";
import {
  type BoardIssue,
  type BoardIssues,
  type ThreadState as BoardThreadState,
} from "./components/ThreadBoard";
import { DraftEditor } from "./components/DraftEditor";
import { HarnessStatsView } from "./components/HarnessStatsPanel";
import { StatsPage } from "./components/StatsPage";
import { WorktreesPage } from "./components/WorktreesPage";
import { PrunePrompt } from "./components/PrunePrompt";
import { pruneReady, worktreeRows, type WorktreeInfo, type WorktreeRow } from "./lib/worktrees";
import { DocLinkContext, Markdown } from "./components/Markdown";
import type { MarkdownDocLike } from "./components/MarkdownViewer";
import { docPathFromHref } from "./lib/markdownLinks";
// The viewer is only needed once a markdown file is opened, so it loads on demand.
const MarkdownViewer = lazy(() => import("./components/MarkdownViewer"));
import { QuestionCard } from "./components/QuestionCard";
import type { AgentQuestion } from "./lib/questions";
import { AgentDetail } from "./components/AgentDetail";
import { AgentTree, type DetailTab } from "./components/AgentTree";
import { TeamProgress } from "./components/TeamProgress";
import { ThreadInfoDialog } from "./components/ThreadInfoDialog";
import { CheatSheet, showCheatSheetOnLaunch } from "./components/CheatSheet";
import { FolderBrowser } from "./components/FolderBrowser";
import { useAttention } from "./lib/attention";
import { useEscapeClose } from "./lib/escape";
import { parseRoute, useUrlRoute, type Route, type RouteTab } from "./lib/route";
import { loadVimKeys, saveVimKeys, useVimKeys } from "./lib/vimKeys";
import { AppUpdate } from "./components/UpdateChip";
import { HarnessUpdates } from "./components/HarnessUpdateChip";
import { SidebarFooter } from "./components/SidebarFooter";
import { NextStep } from "./components/NextStep";
import { EmptyChat } from "./components/EmptyChat";
import { FirstRunTour } from "./components/FirstRunTour";
import { Breadcrumb, HistoryButtons, PaletteButton, StatusPopover, type Crumb } from "./components/NavChrome";
import { useHistory } from "./lib/history";
import { ALT, centerTabFromKey, centerTabHint, isPaletteKey, isTyping } from "./lib/navKeys";
import { threadStatus } from "./lib/threadStatus";
import { recentIds, recordRecent } from "./lib/recent";
import type { FileMatch } from "./lib/mentions";
import { useScrollMemory } from "./lib/scrollMemory";
import type { MailboxMessage } from "./lib/graph";
import { soloHue } from "./lib/soloIdentity";
import { getPref, setPref } from "./lib/prefs";
import { applyTheme, loadTheme } from "./lib/themes";
import {
  buildCommands,
  parseSlash,
  type SlashCommand,
} from "./lib/commands";
import { CommandPalette } from "./components/CommandPalette";
import {
  buildCommands as buildPaletteCommands,
  composerItems,
  groupItems,
  projectItems,
  threadItems,
  withRecent,
  type PaletteItem,
  type PaletteSection,
} from "./lib/palette";
import type { BtwEntry } from "./components/BtwPanel";
import { SuperView, isAllThreadsShortcut } from "./components/SuperView";
import { TERMINAL } from "./lib/graphEvents";
import {
  groupTurns,
  MessageActionsContext,
  MessageImageContext,
  TurnBlock,
  type Message,
  type MessageActions,
  type MessageImageConn,
} from "./components/Transcript";
import { MasterChat } from "./components/MasterChat";
import type { ToolCall } from "./lib/toolCalls";
import { Composer } from "./components/Composer";
import {
  composerStore,
  draftKeyOf,
  draftText,
  expandPastes,
  setAttachments,
  setCaret,
  setDraftImages,
  setDrafts,
  setPastes,
  setRecall,
  unqueue,
} from "./lib/composerStore";
import { useLatestCallback } from "./lib/store";
import { TurnActivity } from "./components/TurnActivity";
import { InheritedPanel } from "./components/InheritedPanel";
import { CouncilPanel, councilSetup, councilTopic } from "./components/CouncilPanel";
import { RenameDialog } from "./components/RenameDialog";
import {
  SettingsSheet,
  fillSettingsForm,
  flashSaved,
  setModelsNotice,
} from "./components/SettingsSheet";
import { ThreadList, ThreadSearch } from "./components/Sidebar/ThreadList";
import { ProjectThreads } from "./components/ProjectThreads";
import { appendLive, dropSnapshotOverlap, resetLive, type ReplySource } from "./lib/liveLog";
import { changesThreadList, foldThreadEvent, parseThreadEvent, stateVersion } from "./lib/threadFold";
import { useAgentLog } from "./lib/useAgentLog";
import { useDialogFocus } from "./lib/dialogFocus";

type Role = {
  id: string;
  name: string;
  defaultHarness: string;
  defaultModel: string;
  builtin: boolean;
};
type Agent = {
  id: string;
  threadId?: string;
  parentAgentId?: string;
  name?: string;
  role: string;
  harness: string;
  model: string;
  /** The harness's own id for the latest run; set once the harness reports it. */
  harnessSession?: string;
  task: string;
  dependsOn?: string[];
  status: string;
  reason?: string;
  pipelineId?: string;
  startedAt?: string;
  endedAt?: string;
};
type LogEntry = { agentId: string; kind: string; text: string; at: string; tool?: ToolCall };
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
  /** The thread's own auto-mode allowlist; empty inherits the environment's. */
  autoApprovedModels?: Record<string, string[]> | null;
  peerMailbox: boolean;
  /** Seconds between workflow status reports while a turn runs; 0 is off. */
  statusPeriodSeconds?: number;
  /** Orchestrator team size; zero or absent means one lead. */
  leads?: number;
  /** Per-lead runtime overrides by lead index; an empty harness inherits the Orchestrator role. */
  leadRuntimes?: LeadRuntimeView[];
  /** Solo override: one agent on one harness, no orchestrator team. Absent means orchestrator mode. */
  soloRuntime?: LeadRuntimeView;
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
  /** Who started this thread; absent on daemons that predate it means user. */
  owner?: "user" | "orchestrator";
  /** Agent questions for the human, oldest first. Pending ones await an answer. */
  questions?: AgentQuestion[] | null;
  /** The agreed task list, in the order the leads added it. */
  tasks?: TaskItem[] | null;
};
export type ThreadSummary = {
  id: string;
  title: string;
  projectId?: string;
  /** The first human message, truncated by the daemon for list search. */
  firstPrompt?: string;
  /** Set on a subthread: the thread that started it. */
  parentThreadId?: string;
  /** Who started this thread; absent (older threads) means user. */
  owner?: "user" | "orchestrator";
  updatedAt?: string;
  messages: number;
  agents: number;
  /** The only agent's harness on a solo thread; drives the row's tint. */
  harness?: string;
  autoMode: boolean;
  /** Filed by the user; shown in the board's Archived column. */
  archived?: boolean;
  runningAgents?: number;
  staleAfterSeconds?: number;
  stale?: boolean;
  /** When the user last opened the thread; absent means never. */
  lastSeenAt?: string;
  /** A finished turn ended after the last view (derived by the daemon). */
  unseen?: boolean;
  /** Agent questions still waiting on the human. */
  pendingQuestions?: number;
  /** The thread's own worktree; its branch is what a PR is opened from. */
  worktree?: { path: string; branch: string; base: string } | null;
  /** A draft has not had its first turn yet. */
  draft?: boolean;
  /** The GitLab issue the thread was started from, if any. */
  issue?: { provider: string; iid: number; title: string; url: string } | null;
  /** Solo override: one agent on one harness, no orchestrator team. Absent means orchestrator mode. */
  soloRuntime?: LeadRuntimeView;
  /** Set while the thread is scheduled: it starts once these threads finish. */
  waiting?: { dependsOn: string[]; prompt: string };
  /** Thread ids whose completion has triggered relaunches of this one. */
  triggeredFrom?: string[];
  /** The thread that relaunched this one, when another thread controls it. */
  controlledBy?: string;
};

export type Project = { id: string; name: string; path: string; threads: number };

/** The top-level pages shown when no project is open. */
type HomePage = "projects" | "orchestrator" | "worktrees" | "stats";
const HOME_LABEL: Record<HomePage, string> = {
  projects: "Projects",
  orchestrator: "Orchestrator",
  worktrees: "Worktrees",
  stats: "Stats",
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
export type HarnessInfo = {
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
  /** Every harness key, each with its sorted approved model list. Saved per harness. */
  approvedByHarness?: Record<string, string[]>;
  /** Union across harnesses, kept for older readers. Do not send it back. */
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
 * (The team graph and the file editor live as center tabs beside Chat and Review.
 * See `tab`.) */
type AsideTabId =
  | "agents"
  | "usage"
  | "context"
  | "inherited"
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
  diff: { label: "Diff", key: "D" },
  pr: { label: "Pull request", key: "P" },
  stats: { label: "Harness stats", key: "H" },
  models: { label: "Models", key: "L" },
  council: { label: "Council", key: "O" },
};
/** Environment-level pages, left of the open projects in the tab strip. "all"
 * is the all-threads overlay rather than a page. */
const ENV_PAGES: { page: HomePage | "all"; label: string; title: string; icon: ReactNode }[] = [
  {
    page: "projects",
    label: "Projects",
    title: "All projects",
    icon: (
      <>
        <rect x="3" y="3" width="7" height="7" rx="1.5" />
        <rect x="14" y="3" width="7" height="7" rx="1.5" />
        <rect x="3" y="14" width="7" height="7" rx="1.5" />
        <rect x="14" y="14" width="7" height="7" rx="1.5" />
      </>
    ),
  },
  {
    page: "orchestrator",
    label: "Orchestrator",
    title: "Master orchestrator: work across projects",
    icon: <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />,
  },
  {
    page: "worktrees",
    label: "Worktrees",
    title: "Every project's worktrees",
    icon: (
      <>
        <line x1="6" y1="3" x2="6" y2="15" />
        <circle cx="18" cy="6" r="3" />
        <circle cx="6" cy="18" r="3" />
        <path d="M18 9a9 9 0 0 1-9 9" />
      </>
    ),
  },
  {
    page: "stats",
    label: "Stats",
    title: "Harness stats across all projects",
    icon: <path d="M4 20V10M10 20V4M16 20v-7M22 20H2" />,
  },
  {
    page: "all",
    label: "All threads",
    title: `All threads across projects (${ALT}Tab)`,
    icon: <path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01" />,
  },
];

/** How the project's threads are laid out: the card list or the status board. */
type ThreadView = "list" | "board";
/** A modal sheet over a dimmed overlay: role, modal, name, and focus held inside. */
function ModalSheet({
  label,
  onDismiss,
  children,
}: {
  label: string;
  onDismiss: () => void;
  children: ReactNode;
}) {
  const box = useDialogFocus<HTMLDivElement>();
  return (
    <div className="overlay" onClick={onDismiss}>
      <div
        ref={box}
        className="sheet"
        role="dialog"
        aria-modal="true"
        aria-label={label}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
      >
        {children}
      </div>
    </div>
  );
}

function loadThreadView(): ThreadView {
  if (getPref("at.threadView") === "board") return "board";
  return "list";
}

/** When the daily worktree scan last completed, or 0 when it never has. */
function loadWorktreeScanAt(): number {
  try {
    const at = Number(window.localStorage.getItem(WORKTREE_SCAN_KEY));
    return Number.isFinite(at) && at > 0 ? at : 0;
  } catch {
    /* private mode: scan once per launch */
  }
  return 0;
}

function saveWorktreeScanAt(at: number) {
  try {
    window.localStorage.setItem(WORKTREE_SCAN_KEY, String(at));
  } catch {
    /* private mode: the next launch scans again */
  }
}

/** How often the board re-reads PR state while it is open. */
const THREAD_PR_POLL_MS = 20_000;
/** The Worktrees page leans on PR state for its merged highlights, so it checks
 * sooner and more often than the board. pr.states is a cache peek; the forge
 * refresh keeps its own TTL, so polling does not add CLI calls. */
const WORKTREE_PR_POLL_MS = 10_000;
const WORKTREE_PR_WARM_MS = 3_000;
/** Worktree lists run git in every checkout, so the global page loads a few
 * projects at a time instead of forking every project's git at once. */
const WORKTREE_PROJECT_BATCH = 3;
/** The daily prune scan runs in the background, once per day per client. It
 * looks at every project's checkouts, waits for pr.states to answer (the first
 * peek starts the forge fetch), then asks before removing anything. */
const WORKTREE_SCAN_MS = 24 * 60 * 60_000;
const WORKTREE_SCAN_CHECK_MS = 15 * 60_000;
const WORKTREE_SCAN_RETRY_MS = 4_000;
const WORKTREE_SCAN_TIMEOUT_MS = 90_000;
const WORKTREE_SCAN_START_DELAY_MS = 2_500;
const WORKTREE_SCAN_KEY = "at.worktreeScan";
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
  return getPref("at.logsOpen") !== "false";
}

function sameThreadState(a: BoardThreadState, b: BoardThreadState): boolean {
  if (a.dirty !== b.dirty || a.prKnown !== b.prKnown) return false;
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

/** Structural equality for wire JSON: walks both values and stops at the first difference, allocating nothing. */
function sameValue(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (typeof a !== "object" || typeof b !== "object" || a === null || b === null) return false;
  if (Array.isArray(a)) {
    if (!Array.isArray(b) || a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) if (!sameValue(a[i], b[i])) return false;
    return true;
  }
  if (Array.isArray(b)) return false;
  const ra = a as Record<string, unknown>;
  const rb = b as Record<string, unknown>;
  const keys = Object.keys(ra);
  if (keys.length !== Object.keys(rb).length) return false;
  for (const k of keys) {
    if (!Object.prototype.hasOwnProperty.call(rb, k) || !sameValue(ra[k], rb[k])) return false;
  }
  return true;
}

/** Returns `prev` when `next` holds the same data, so state setters keep the old reference and skip the re-render. */
function keepSame<T>(prev: T, next: T): T {
  return sameValue(prev, next) ? prev : next;
}

/** Projects opened as tabs, remembered across restarts. */
function loadOpenProjects(): string[] {
  try {
    const raw = getPref("at.openProjects");
    if (raw) {
      const ids = JSON.parse(raw) as unknown;
      if (Array.isArray(ids)) return ids.filter((id): id is string => typeof id === "string");
    }
  } catch {
    /* corrupt: start with no tabs */
  }
  return [];
}

function loadAsideTabs(): AsideTabId[] {
  try {
    const raw = getPref("at.asideTabs");
    if (raw) {
      const ids = (JSON.parse(raw) as string[]).filter((id): id is AsideTabId => id in ASIDE_META);
      for (const p of ASIDE_PINNED) if (!ids.includes(p)) ids.unshift(p);
      if (ids.length > 0) return ids;
    }
  } catch {
    /* corrupt: fall through to defaults */
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
    const raw = getPref("at.layout");
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

// Release notes for the build that an update just installed. The daemon hands
// them out once, on the first start of that version.
interface ReleaseNotes {
  version: string;
  body: string;
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

/** UI state that belongs to one thread. A thread with no value of its own reads
 * the global default; writing sets only that thread's value. Writing while no
 * thread is selected changes the default. `storageKey` keeps the default across
 * launches; a thread's own values live for the session. */
function useThreadScoped<T>(threadId: string | null, initial: T | (() => T), storageKey?: string) {
  const [fallback, setFallback] = useState<T>(() => {
    if (storageKey) {
      try {
        const raw = getPref(storageKey);
        if (raw !== null) return JSON.parse(raw) as T;
      } catch {
        /* corrupt: use the built-in default */
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
    setPref(storageKey, JSON.stringify(fallback));
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

// The jump-to-top pill appears once the stream is scrolled this far down.
const TOP_SHOW = 240;

export default function App() {
  const [status, setStatus] = useState<"connecting" | "online" | "reconnecting" | "offline">("connecting");
  // Short drops are covered by the status popover; past this grace the live stream is visibly paused.
  const [linkDown, setLinkDown] = useState(false);
  useEffect(() => {
    if (status !== "reconnecting") return;
    const timer = window.setTimeout(() => setLinkDown(true), 2_000);
    return () => {
      window.clearTimeout(timer);
      setLinkDown(false);
    };
  }, [status]);
  // Bumped after a dropped socket comes back: views that follow the stream
  // read their state again, since frames sent while it was down are lost.
  const [resync, setResync] = useState(0);
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
  const [releaseNotes, setReleaseNotes] = useState<ReleaseNotes | null>(null);

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
  const scheduleBoardReloadRef = useRef<() => void>(() => undefined);
  const noteDetachedTurn = useCallback(
    (threadId: string) => {
      // A turn that never starts sends no event; read the list once the
      // start grace is over so its busy flag still clears.
      window.setTimeout(() => scheduleBoardReloadRef.current(), DETACHED_START_GRACE_MS + 500);
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
  const [detailTab, setDetailTab] = useState<DetailTab>("output");
  const openAgent = useCallback(
    (id: string, tab?: DetailTab) => {
      setDetailTab(tab ?? "output");
      setDetailId(id);
    },
    [setDetailId],
  );
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
  // Compact mode is a focus view, not a layout choice: it lasts this session
  // and drops the titlebar, project tabs, and side panes (Cmd/Ctrl+S).
  const [compact, setCompact] = useState(false);
  const shellRef = useRef<HTMLDivElement>(null);
  // Narrow windows turn the side panels into slide-over drawers; opening a
  // thread closes them.
  const drawers = useDrawers(shellRef, activeId);
  const [panelMenu, setPanelMenu] = useState<{ x: number; y: number } | null>(null);
  // The live log box can be collapsed to a header; the choice persists per viewer.
  const [logsOpen, setLogsOpen] = useState(loadLogsOpen);
  const toggleLogs = () => {
    const next = !logsOpen;
    setLogsOpen(next);
    setPref("at.logsOpen", String(next));
  };

  const [tab, setTab, setTabDefault] = useThreadScoped<
    "chat" | "review" | "files" | "graph" | "board" | "doc" | "p2p"
  >(activeId, "chat", "at.tab");
  // The event subscription closes over the first render's tab, so it reads visibility here.
  const diffVisibleRef = useRef(false);
  diffVisibleRef.current = tab === "review" || asideActive === "diff";
  const [diff, setDiff] = useState<Diff | null>(null);
  const [diffError, setDiffError] = useState<string | null>(null);
  const [draft, setDraft] = useThreadScoped<DraftLike | null>(activeId, null);
  /** The markdown file open in the center viewer tab, relative to the thread's root. */
  const [docPath, setDocPath] = useThreadScoped<string | null>(activeId, null);
  const [doc, setDoc] = useState<MarkdownDocLike | null>(null);
  const [docError, setDocError] = useState<string | null>(null);
  const [docLoading, setDocLoading] = useState(false);
  const docReqRef = useRef(0);

  // Pull requests the active thread opened; tagged with its thread so a late reply never shows elsewhere.
  const [prState, setPRState] = useState<PRListing | null>(null);
  const [prLoading, setPRLoading] = useState(false);
  const [prError, setPRError] = useState<string | null>(null);
  // Worktrees: per project, live git facts from worktree.list. They own the
  // global Worktrees page; loaded on demand and after each action, never on a
  // timer, because every refresh runs git in each worktree.
  const [worktrees, setWorktrees] = useState<Record<string, WorktreeInfo[] | undefined>>({});
  const [worktreesLoading, setWorktreesLoading] = useState(false);
  const [worktreesError, setWorktreesError] = useState<string | null>(null);
  // The daily scan's answer, shown as a prompt rather than removed silently.
  const [prunePrompt, setPrunePrompt] = useState<{
    candidates: WorktreeRow[];
    dirtyCount: number;
  } | null>(null);
  const [pruneBusy, setPruneBusy] = useState(false);
  const [pruneError, setPruneError] = useState<string | null>(null);
  const scanningWorktrees = useRef(false);

  const [settingsOpen, setSettingsOpen] = useState(false);
  // Appearance is shared by every client of this daemon through the prefs
  // store; the settings sheet changes it.
  useEffect(() => applyTheme(loadTheme()), []);
  // A short tour opens on its own only until it has been seen once; /help
  // opens the full how-to sheet.
  const [tourOpen, setTourOpen] = useState(showCheatSheetOnLaunch);
  const [cheatSheetOpen, setCheatSheetOpen] = useState(false);
  const helpOpen = cheatSheetOpen || tourOpen;
  // /vim turns on Vimium-style keys (see lib/vimKeys.ts). Off by default so the single-letter panel keys keep working.
  const [vimKeys, setVimKeys] = useState(loadVimKeys);
  useVimKeys(vimKeys);
  const modelsOpen = asideActive === "models" && asideTabs.includes("models");
  const [settings, setSettings] = useState<SettingsView | null>(null);
  // Shell capability state: where the daemon lives and whether it should
  // outlive the window. Connection data only — thread data stays on the
  // daemon WebSocket.
  const [conn, setConn] = useState<Conn | null>(null);
  const [keepRunning, setKeepRunningState] = useState(false);


  const [projects, setProjects] = useState<Project[]>([]);
  const [projectId, setProjectId] = useState<string | null>(null);
  // Composer text and attachments live in composerStore (see Composer), so a
  // keystroke does not re-render App. App reads them only when it acts on them.
  const draftKey = draftKeyOf(projectId, activeId);
  const draftKeyRef = useRef(draftKey);
  draftKeyRef.current = draftKey;
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
  // Browser fallback for the Add Project flow: no native folder picker there,
  // so the path is typed into a small sheet instead.
  const [newProjectOpen, setNewProjectOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<ThreadSummary | null>(null);
  const [cleanupConfirm, setCleanupConfirm] = useState(false);
  const [cleaningThreads, setCleaningThreads] = useState(false);
  const [infoTarget, setInfoTarget] = useState<{ id: string; tab: "usage" | "agents" } | null>(
    null,
  );
  const [infoState, setInfoState] = useState<{ loading: boolean; thread: ThreadState | null }>({
    loading: false,
    thread: null,
  });

  const rpcRef = useRef<Rpc | null>(null);
  /** The client still making its first connection; the splash's Retry pokes it. */
  const bootRpcRef = useRef<Rpc | null>(null);
  // The daemon's HTTP origin + token, for image uploads and fetches that must
  // not ride the WebSocket.
  const connRef = useRef<Conn | null>(null);
  const debounce = useRef<number | undefined>(undefined);
  // Chat stream auto-scroll: pinned to bottom while the user is already there.
  const streamRef = useRef<HTMLDivElement | null>(null);
  // Only the edge is stored, so scrolling re-renders when the user crosses it.
  const [atBottom, setAtBottom] = useState(true);
  const nearBottom = (el: HTMLElement) => el.scrollHeight - el.scrollTop - el.clientHeight < 120;
  // Where the user was before the stream last grew: measured after a tall
  // block (a question card) lands, the distance to the bottom says the user
  // scrolled away when they did not.
  const pinnedRef = useRef(true);
  // Jump-to-top pill shows once the stream is scrolled this far down.
  const [atTop, setAtTop] = useState(false);

  // A thread opens at its latest message, whatever the last thread's scroll was.
  useEffect(() => {
    pinnedRef.current = true;
  }, [activeId]);

  // Only scroll events move pinnedRef. Measuring distance here would misread
  // layout changes: sending shrinks the composer, which grows the stream and can
  // bring a user who scrolled up within reach of the bottom, yanking them down.
  useEffect(() => {
    const el = streamRef.current;
    if (!el) return;
    if (pinnedRef.current) el.scrollTop = el.scrollHeight;
    setAtBottom(pinnedRef.current);
  }, [
    activeId,
    thread?.messages?.length,
    thread?.messages?.[thread.messages.length - 1]?.text,
    thread?.questions?.length,
  ]);
  // The live log reads its own store; it reports new lines so the transcript
  // keeps following the newest output.
  const followStream = useCallback(() => {
    const el = streamRef.current;
    if (!el) return;
    if (pinnedRef.current) el.scrollTop = el.scrollHeight;
    setAtBottom(pinnedRef.current);
  }, []);

  const scrollToBottom = () => {
    const el = streamRef.current;
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
  };

  const scrollToTop = () => {
    const el = streamRef.current;
    if (el) el.scrollTo({ top: 0, behavior: "smooth" });
  };

  /** Scrolls the previous (-1) or next (1) message into view. Rows carry
   * data-mid so the DOM can be walked at click time only; navigating never
   * re-renders the transcript. */
  const scrollToMessage = useCallback(
    (messageId: string, dir: -1 | 1) => {
      const el = streamRef.current;
      if (!el) return;
      const nodes = Array.from(el.querySelectorAll<HTMLElement>(".msg[data-mid]"));
      const idx = nodes.findIndex((n) => n.dataset.mid === messageId);
      if (idx < 0) return;
      const target = nodes[idx + dir];
      if (target) target.scrollIntoView({ block: "start", behavior: "smooth" });
    },
    [streamRef],
  );

  const refresh = useCallback(async (id: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    const [fetched, list, usage] = await Promise.all([
      rpc.call<ThreadState>("thread.get", { threadId: id }),
      rpc.call<ThreadSummary[]>("thread.list"),
      rpc.call<ThreadUsage>("usage.get"),
    ]);
    // Events folded while the read was in flight may already be past it.
    const cached = threadCacheRef.current[id];
    const state = cached && stateVersion(cached) > stateVersion(fetched) ? cached : fetched;
    threadCacheRef.current[id] = state;
    // A reply for a thread the user has since left must not replace the one on screen.
    if (activeIdRef.current === id) setThread(state);
    setThreads((prev) => keepSame(prev, list));
    settleDetachedTurns(list);
    setGlobalUsage((prev) => keepSame(prev, usage));
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

  /** Branches the open thread at one message: thread.fork starts a new thread
   * carrying the transcript up to and including that message, which is then
   * selected like any other thread in the project (selecting it also reloads
   * the thread list). The source thread is only read. */
  const forkFromMessage = useCallback(
    async (messageId: string) => {
      const rpc = rpcRef.current;
      const id = activeIdRef.current;
      if (!rpc || !id) return;
      let forked: { threadId: string };
      try {
        forked = await rpc.call<{ threadId: string }>("thread.fork", { threadId: id, messageId });
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e), id);
        return;
      }
      if (projectId) lastThreadByProject.current[projectId] = forked.threadId;
      setActiveId(forked.threadId);
    },
    [projectId, setError],
  );
  // Edit-and-resend: the prompt goes back into this thread's draft, caret at
  // the end, focus in the composer. Nothing is sent until the user does.
  const editPrompt = useLatestCallback((text: string) => {
    setDrafts((prev) => ({ ...prev, [draftKeyRef.current]: text }));
    pendingCaret.current = text.length;
    taRef.current?.focus();
  });
  const openReview = useLatestCallback(() => setTab("review"));
  const messageActions = useMemo<MessageActions>(
    () => ({
      copyText: (text) => writeClipboard(text),
      forkFromMessage,
      scrollToMessage,
      editPrompt,
      openReview,
    }),
    [forkFromMessage, scrollToMessage, editPrompt, openReview],
  );

  // Flat DOM position and total of the rendered messages, so each row knows
  // whether a prev/next neighbor exists. Props only — a position map in a
  // context would re-render every row each refresh.
  const messageIndex = useMemo(() => {
    const map = new Map<string, number>();
    (thread?.messages ?? []).forEach((m, i) => map.set(m.id, i));
    return map;
  }, [thread?.messages]);
  const messageTotal = messageIndex.size;

  // The connection details transcript images fetch with, memoized so memoized
  // message rows only re-render when the connection or thread changes.
  const messageImages = useMemo<MessageImageConn | null>(
    () =>
      conn && activeId
        ? { httpUrl: conn.httpUrl, token: conn.token, threadId: activeId }
        : null,
    [conn, activeId],
  );

  // Turn groupings memoized on the message list: App re-renders on keystrokes
  // and live log lines, but only a changed transcript regroups, so memoized
  // turns and their message rows stay untouched.
  const turnBlocks = useMemo(() => groupTurns(thread?.messages ?? []), [thread?.messages]);

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


  const loadDoc = useCallback(async (id: string | null, path: string) => {
    const rpc = rpcRef.current;
    if (!rpc || !id) return;
    // Only the newest request may write: a slow earlier read must not overwrite a later document.
    const req = ++docReqRef.current;
    const current = () => activeIdRef.current === id && req === docReqRef.current;
    setDoc(null);
    setDocError(null);
    setDocLoading(true);
    try {
      const d = await rpc.call<MarkdownDocLike>("markdown.read", { threadId: id, path });
      if (current()) setDoc(d);
    } catch (e) {
      if (current()) setDocError(e instanceof Error ? e.message : String(e));
    } finally {
      if (current()) setDocLoading(false);
    }
  }, []);

  const openDoc = useCallback(
    (path: string) => {
      setDocPath(path);
      setTab("doc");
    },
    [setDocPath, setTab],
  );
  // Chat links carry raw hrefs (percent-encoded, maybe with #fragments); the viewer's own links are already resolved.
  const openChatDoc = useCallback((href: string) => openDoc(docPathFromHref(href)), [openDoc]);
  const closeDoc = useCallback(() => {
    setDocPath(null);
    setTab("chat");
  }, [setDocPath, setTab]);

  // Waits for the RPC: a document persisted as the open tab loads once status turns online.
  useEffect(() => {
    if (tab === "doc" && docPath && status === "online") loadDoc(activeId, docPath).catch(() => undefined);
  }, [tab, docPath, activeId, status, loadDoc]);

  const revealAside = drawers.reveal;
  const openPanel = useCallback((id: AsideTabId) => {
    // Seeds the council topic from the draft.
    const draft = id === "council" ? draftText(draftKeyRef.current) : "";
    if (draft) councilTopic.set(activeIdRef.current, draft);
    setAsideTabs((prev) => (prev.includes(id) ? prev : [...prev, id]));
    setAsideActive(id);
    // On a narrow window the aside is a drawer: show it, or the panel opens unseen.
    revealAside("aside");
  }, [revealAside]);

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

  /** Adding a project is the native folder picker; the folder's name is the project name. */
  const addProject = async () => {
    // Browsers have no native picker: open the daemon-backed folder browser.
    if (!inDesktopShell()) {
      setNewProjectOpen(true);
      return;
    }
    const path = await pickFolder();
    if (!path) return;
    await createProjectAt(path);
  };

  /** Creates a project for a user-supplied path, shared by the native picker
   * and the web UI's folder browser. */
  const createProjectAt = async (path: string) => {
    const rpc = rpcRef.current;
    if (!rpc || !path) return;
    const name = path.split("/").filter(Boolean).pop() ?? path;
    try {
      const res = await rpc.call<{ project: Project; projects: Project[] }>("project.create", {
        name,
        path,
      });
      setProjects(res.projects);
      setNewProjectOpen(false);
      openProject(res.project.id);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
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

  /** Loads the worktrees of the given projects. Each batch lands as it finishes,
   * so the page fills in instead of waiting on every project; a failing project
   * does not hide the others, and the first failure is reported once. It returns
   * what it loaded so a caller (the daily scan) can act without waiting for
   * React state to settle. */
  const loadWorktrees = useCallback(async (ids: string[]) => {
    const rpc = rpcRef.current;
    const loaded: Record<string, WorktreeInfo[]> = {};
    if (!rpc) return loaded;
    setWorktreesLoading(true);
    setWorktreesError(null);
    let failure: string | null = null;
    // Projects load in small batches: each list runs git in every one of its
    // worktrees, and the environment-wide page can have many projects.
    for (let i = 0; i < ids.length; i += WORKTREE_PROJECT_BATCH) {
      const batch = ids.slice(i, i + WORKTREE_PROJECT_BATCH);
      const results = await Promise.allSettled(
        batch.map((id) => rpc.call<WorktreeInfo[]>("worktree.list", { projectId: id })),
      );
      results.forEach((r, j) => {
        if (r.status === "fulfilled") loaded[batch[j]] = r.value;
      });
      setWorktrees((prev) => {
        const next = { ...prev };
        for (const [id, list] of Object.entries(loaded)) next[id] = list;
        return next;
      });
      const failed = results.find((r): r is PromiseRejectedResult => r.status === "rejected");
      if (failed && !failure) {
        failure = failed.reason instanceof Error ? failed.reason.message : String(failed.reason);
      }
    }
    if (failure) setWorktreesError(failure);
    setWorktreesLoading(false);
    return loaded;
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

  /** Removes every merged, clean checkout in one pass, then reloads each
   * affected project once. Dirty worktrees never reach here: they need the
   * per-card discard confirmation, and the daemon refuses them unforced. */
  const pruneMergedWorktrees = async (targets: { projectId: string; path: string }[]) => {
    const rpc = rpcRef.current;
    if (!rpc || targets.length === 0) return { failure: null, failed: [] as string[] };
    let failure: string | null = null;
    const failed: string[] = [];
    for (const target of targets) {
      try {
        await rpc.call("worktree.remove", { projectId: target.projectId, path: target.path, force: false });
      } catch (e) {
        failed.push(target.path);
        if (!failure) failure = e instanceof Error ? e.message : String(e);
      }
    }
    await loadWorktrees([...new Set(targets.map((t) => t.projectId))]);
    if (failure) setWorktreesError(failure);
    return { failure, failed };
  };

  /** The once-a-day background pass behind the prune prompt. It reads every
   * project's worktrees, asks the daemon for each owner's cached PR state, and
   * waits for the first forge answer so a cold cache does not look like "no
   * PRs". Nothing is removed here: the prompt asks first. */
  const scanWorktrees = useCallback(async () => {
    const rpc = rpcRef.current;
    if (!rpc || projects.length === 0 || scanningWorktrees.current) return;
    scanningWorktrees.current = true;
    try {
      const loaded = await loadWorktrees(projects.map((p) => p.id));
      if (Object.keys(loaded).length === 0) return;
      const ids = [
        ...new Set(
          Object.values(loaded)
            .flatMap((list) => list ?? [])
            .flatMap((w) => w.threadIds),
        ),
      ];
      let states: Record<string, BoardThreadState> = {};
      let forgeAnswered = ids.length === 0;
      if (ids.length > 0) {
        const deadline = Date.now() + WORKTREE_SCAN_TIMEOUT_MS;
        for (;;) {
          states = await rpc.call<Record<string, BoardThreadState>>("pr.states", { threadIds: ids });
          setThreadStates((prev) => mergeThreadStates(prev, states));
          const waiting = ids.some((id) => states[id] && !states[id].prKnown);
          if (!waiting) {
            forgeAnswered = true;
            break;
          }
          if (Date.now() >= deadline) break;
          await new Promise((resolve) => window.setTimeout(resolve, WORKTREE_SCAN_RETRY_MS));
        }
      }
      // A cold or slow forge is not "no merged PRs": leave the day unmarked so
      // the next background check asks again.
      if (!forgeAnswered) return;
      const rows = worktreeRows(projects, loaded, states);
      const candidates = pruneReady(rows);
      const dirtyCount = rows.filter((r) => r.tone === "merged-dirty").length;
      saveWorktreeScanAt(Date.now());
      if (candidates.length > 0) {
        setPruneError(null);
        setPrunePrompt({ candidates, dirtyCount });
      }
    } catch {
      // A scan is opportunistic: the page still polls PRs while it is open, and
      // the next daily check retries.
    } finally {
      scanningWorktrees.current = false;
    }
  }, [projects, loadWorktrees]);

  const pruneFromPrompt = async () => {
    if (!prunePrompt) return;
    setPruneBusy(true);
    setPruneError(null);
    try {
      const { failure, failed } = await pruneMergedWorktrees(
        prunePrompt.candidates.map(({ project, worktree }) => ({
          projectId: project.id,
          path: worktree.path,
        })),
      );
      if (failure) {
        // Keep the ones that survived so a retry does not aim at paths already
        // gone. The message is usually a running turn in that checkout.
        const kept = new Set(failed);
        setPrunePrompt((prev) =>
          prev ? { ...prev, candidates: prev.candidates.filter((c) => kept.has(c.worktree.path)) } : prev,
        );
        setPruneError(failure);
      } else {
        setPrunePrompt(null);
      }
    } finally {
      setPruneBusy(false);
    }
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
  // The thread list (sidebar, project tabs, board). Stable, so it can sit in
  // the event effect's deps. A failed read is retried by the next event.
  const reloadBoardThreads = useCallback(async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      const list = await rpc.call<ThreadSummary[]>("thread.list");
      setThreads((prev) => keepSame(prev, list));
      settleDetachedTurns(list);
    } catch {
      /* the next thread event or refresh reads again */
    }
  }, [settleDetachedTurns]);
  // Other threads keep working while not on screen, so a list-changing event
  // from any thread (the daemon's thread.changed) re-reads the list — no
  // polling. Throttled, not debounced: a busy
  // turn emits events faster than a quiet-period debounce would ever fire. The
  // first event reads at once; later ones wait for the interval, and one
  // trailing read picks up whatever arrived in between.
  const BOARD_RELOAD_INTERVAL_MS = 1000;
  const threadViewRef = useRef<ThreadView>("list");
  const boardReloadTimer = useRef<number | undefined>(undefined);
  const boardReloadLast = useRef(0);
  const scheduleBoardReload = useCallback(() => {
    if (boardReloadTimer.current !== undefined) return;
    const wait = BOARD_RELOAD_INTERVAL_MS - (Date.now() - boardReloadLast.current);
    const read = () => {
      boardReloadTimer.current = undefined;
      boardReloadLast.current = Date.now();
      reloadBoardThreads();
    };
    if (wait <= 0) read();
    else boardReloadTimer.current = window.setTimeout(read, wait);
  }, [reloadBoardThreads]);
  // For callbacks registered once (the connect effect), which must reach the
  // latest closure.
  scheduleBoardReloadRef.current = scheduleBoardReload;

  /** Undo and redo for reversible changes. A step refreshes what it touched. */
  const history = useHistory({
    onError: (message) => setError(message),
    onApplied: () => {
      reloadThreads().catch(() => undefined);
      if (activeIdRef.current) refresh(activeIdRef.current).catch(() => undefined);
    },
  });
  /** RPC for a history step. Reads the live connection, so a step after a
   * reconnect talks to the new one. */
  const callDaemon = (method: string, params: unknown): Promise<void> => {
    const rpc = rpcRef.current;
    if (!rpc) return Promise.reject(new Error("Not connected to the daemon"));
    return rpc.call(method, params).then(() => undefined);
  };

  const beginRename = (t: ThreadSummary) => {
    setRenameTarget(t);
  };

  const confirmRename = useLatestCallback(async (title: string) => {
    const rpc = rpcRef.current;
    if (!rpc || !renameTarget || !title) return;
    try {
      await rpc.call("thread.rename", { threadId: renameTarget.id, title });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return;
    }
    const renamed = renameTarget;
    const from = renamed.title;
    const renameTo = (name: string) => () => callDaemon("thread.rename", { threadId: renamed.id, title: name });
    history.record({ label: "rename", undo: renameTo(from), redo: renameTo(title) });
    setRenameTarget(null);
    await reloadThreads();
    if (activeId === renamed.id) refresh(activeId).catch(() => undefined);
  });
  const renameThread = useCallback(
    (title: string) => void confirmRename(title).catch(() => undefined),
    [confirmRename],
  );
  const cancelRename = useCallback(() => setRenameTarget(null), []);

  /** Archives or restores several threads in one go. A failure stops the batch
   * and reports the first error; threads already changed stay changed and
   * stay in the history, so undo reverts exactly what changed. */
  const setThreadsArchived = async (ids: string[], archived: boolean) => {
    const rpc = rpcRef.current;
    if (!rpc || ids.length === 0) return;
    const wasArchived = new Map(ids.map((id) => [id, !!threads.find((t) => t.id === id)?.archived]));
    const changed: string[] = [];
    try {
      for (const threadId of ids) {
        await rpc.call("thread.setArchived", { threadId, enabled: archived });
        changed.push(threadId);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
    if (changed.length > 0) {
      const n = changed.length === 1 ? "thread" : `${changed.length} threads`;
      history.record({
        label: `${archived ? "archive" : "restore"} ${n}`,
        undo: async () => {
          for (const threadId of changed) {
            await callDaemon("thread.setArchived", { threadId, enabled: wasArchived.get(threadId) ?? false });
          }
        },
        redo: async () => {
          for (const threadId of changed) await callDaemon("thread.setArchived", { threadId, enabled: archived });
        },
      });
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

  /** Deletes from a thread row's ×. A thread that never had a message goes at
   *  once; one with history asks first, since that is the work being thrown
   *  away. */
  const requestDeleteThread = (t: ThreadSummary) => {
    if (t.messages === 0) void deleteThreadsById([t.id]);
    else setDeleteTarget(t);
  };

  /** Deletes every thread in the current project. Best-effort: threads that
   *  refuse deletion (for example running ones) are skipped and reported. */
  const cleanupAllProjectThreads = async () => {
    const rpc = rpcRef.current;
    if (!rpc || !projectId) return;
    const ids = threads.filter((t) => t.projectId === projectId).map((t) => t.id);
    if (ids.length === 0) {
      setCleanupConfirm(false);
      return;
    }
    setCleaningThreads(true);
    const deleted: string[] = [];
    let failures = 0;
    let firstError = "";
    for (const threadId of ids) {
      try {
        await rpc.call("thread.delete", { threadId });
        deleted.push(threadId);
      } catch (e) {
        failures++;
        if (!firstError) firstError = e instanceof Error ? e.message : String(e);
      }
    }
    await reloadThreads();
    if (activeId && deleted.includes(activeId)) {
      selectThread(null);
      setThread(null);
    }
    if (failures > 0) {
      setError(`Cleaned up ${deleted.length}/${ids.length} threads. ${firstError}`);
    }
    setCleaningThreads(false);
    setCleanupConfirm(false);
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
    fillSettingsForm(s);
    return s;
  }, []);

  // Connect to the embedded daemon once the shell hands us its address.
  // A daemon that is not up yet (or a web UI loaded before it) keeps the
  // splash on "Connecting…" with the reason, retrying with the reconnect
  // backoff; the splash's Retry skips the wait.
  useEffect(() => {
    let cancelled = false;
    let boot: Rpc | null = null;
    const failed = (e: unknown) => {
      if (cancelled) return;
      setStatus("offline");
      setError(e instanceof Error ? e.message : String(e));
    };
    const stopWake: { off?: () => void } = {};
    discover()
      .catch((e) => {
        // The web UI's /conn did not answer; the retries ask it again.
        failed(e);
        return null;
      })
      .then(async (found: Conn | null) => {
      if (cancelled) return;
      const rpc = new Rpc(found, rediscover);
      boot = rpc;
      bootRpcRef.current = rpc;
      stopWake.off = rpc.retryOnWake();
      await rpc.connect(failed);
      if (cancelled) return;
      const conn = rpc.connection;
      setError(null);
      rpcRef.current = rpc;
      connRef.current = conn;
      setStatus("online");
      // A dropped socket must not leave the UI claiming "online" while every
      // call fails. Say so and keep the state: the client reconnects with
      // backoff, and on its return everything on screen reads again.
      rpc.onStatus((s) => setStatus(s === "online" ? "online" : "reconnecting"));
      rpc.onReconnect(() => {
        connRef.current = rpc.connection;
        setConn(rpc.connection);
        setResync((n) => n + 1);
        scheduleBoardReloadRef.current();
      });
      // Any thread's durable event may change its list row; this replaces polling.
      rpc.onNotification(({ method, params }) => {
        if (method !== "thread.changed") return;
        const type = (params as { type?: string } | null)?.type ?? "";
        if (changesThreadList(type)) scheduleBoardReloadRef.current();
      });
      setConn(conn);
      setKeepRunningState(!!conn.keepRunning);
      // The titlebar's AppUpdate and HarnessUpdates check for updates themselves.
      // Read-and-clear on the daemon: a null answer means nothing is pending.
      rpc
        .call<ReleaseNotes | null>("update.takeReleaseNotes")
        .then((n) => {
          if (n && !cancelled) setReleaseNotes(n);
        })
        .catch(() => {});
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
        // A web page opened on a thread or project path (a reload, a shared
        // link) restores that place instead; the route resolves once loaded.
        const deepLinked = !inDesktopShell() && parseRoute(window.location.pathname).kind !== "home";
        if (conn.launchDir && !deepLinked) {
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
        } else if (conn.launchThread) {
          // `agenttunnel open <thread>` booted the app: select that thread in its project.
          const target = list.find((t) => t.id === conn.launchThread);
          const pid = target?.projectId;
          if (!target || !pid) throw new Error(`thread ${conn.launchThread} is not listed`);
          setOpenProjectIds((prev) => (prev.includes(pid) ? prev : [...prev, pid]));
          setProjectId(pid);
          lastThreadByProject.current[pid] = target.id;
          setActiveId(target.id);
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
    })
      .catch(failed);
    return () => {
      cancelled = true;
      stopWake.off?.();
      boot?.close();
    };
  }, []);
  const retryConnect = useCallback(() => bootRpcRef.current?.retryNow(), []);

  const scheduleRefresh = useCallback(
    (id: string) => {
      window.clearTimeout(debounce.current);
      debounce.current = window.setTimeout(() => {
        refresh(id).catch(() => undefined);
      }, 120);
    },
    [refresh],
  );

  // Environment-wide usage (the titlebar meter) moves only with usage steps;
  // a burst of them reads it once a second at most.
  const usageReloadTimer = useRef<number | undefined>(undefined);
  const scheduleUsageReload = useCallback(() => {
    if (usageReloadTimer.current !== undefined) return;
    usageReloadTimer.current = window.setTimeout(() => {
      usageReloadTimer.current = undefined;
      rpcRef.current
        ?.call<ThreadUsage>("usage.get")
        .then(setGlobalUsage)
        .catch(() => undefined);
    }, 1000);
  }, []);

  // Subscribe to the active thread and keep the local view fresh. The activity
  // log box restores this thread's persisted output on switch, then appends
  // "agent.output" frames for this thread as they stream. Durable events fold
  // into the thread's state locally (lib/threadFold); only a sequence gap or
  // an event the fold does not mirror reads thread.get again.
  // Switching threads drops the other thread's data. The new thread paints from
  // its cached state (or nothing if never loaded) while refresh reloads it, and
  // late replies for the old thread are ignored by the loaders. A reconnect
  // (`resync`) runs it again: frames sent while the socket was down are lost.
  useEffect(() => {
    setThread(activeId ? (threadCacheRef.current[activeId] ?? null) : null);
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
    let buffered: LogEntry[] | null = [];
    resetLive([]);
    rpc.call("thread.subscribe", { threadId: activeId }).catch(() => undefined);
    // Opening a thread clears its "finished but unseen" state server-side. The
    // resulting event re-reads the list and its badge; the call is
    // fire-and-forget, so a refused mark never blocks the view.
    rpc.call("thread.markSeen", { threadId: activeId }).catch(() => undefined);
    // Snapshot and buffered frames are both raw chunks, so overlap is compared
    // before the chunks are merged into display lines.
    const applyBuffered = (snapshot: LogEntry[]) => {
      const fresh = dropSnapshotOverlap(snapshot, buffered ?? []);
      buffered = null;
      resetLive([...snapshot, ...fresh]);
    };
    rpc
      .call<LogEntry[]>("thread.logs", { threadId: activeId })
      .then((entries) => {
        if (stale) return;
        applyBuffered(entries ?? []);
      })
      .catch(() => {
        if (stale) return;
        applyBuffered([]);
      });
    const off = rpc.onThreadEvent((raw) => {
      const ev = parseThreadEvent(raw);
      if (!ev || stale) return;
      if (ev.type === "agent.output") {
        // Output from another thread belongs to that thread's log; it is
        // restored from the daemon when the user switches back.
        if (ev.threadId !== activeId) return;
        const line = {
          agentId: String(ev.payload.agentId ?? ""),
          kind: String(ev.payload.kind ?? "text"),
          text: String(ev.payload.text ?? ""),
          at: ev.at,
          tool: ev.payload.tool as ToolCall | undefined,
        };
        if (buffered) buffered.push(line);
        else appendLive(line);
        return;
      }
      // A thread visited earlier stays subscribed: fold into its cached
      // state, or drop the cache so its next visit paints only fresh data.
      const cached = threadCacheRef.current[ev.threadId];
      const folded = cached ? foldThreadEvent(cached, ev) : null;
      if (ev.threadId !== activeId) {
        if (folded?.kind === "applied") threadCacheRef.current[ev.threadId] = folded.state;
        else if (folded?.kind === "refetch") delete threadCacheRef.current[ev.threadId];
        return;
      }
      // A turn that ends while the user is watching counts as seen the moment
      // its agents go terminal: the open-time markSeen above only fires on
      // thread switches, so without this the thread would flip to "not seen
      // yet" the instant it finishes. The server drops the call when nothing
      // finished after the last view, so extra calls stay free.
      if (ev.type === "agent.status-changed" && TERMINAL.has(String(ev.payload.status ?? ""))) {
        rpc.call("thread.markSeen", { threadId: activeId }).catch(() => undefined);
        // The diff is a snapshot of the working tree: the lead finishing is when it goes stale.
        const agentId = String(ev.payload.agentId ?? "");
        const lead = (folded?.kind === "applied" ? folded.state : cached)?.agents?.[agentId];
        if (lead?.role === "orchestrator" && diffVisibleRef.current) loadDiff(activeId);
      }
      // The daemon's thread.changed covers this too; reading here keeps an
      // older daemon without it honest about the thread on screen.
      if (changesThreadList(ev.type)) scheduleBoardReload();
      if (ev.type === "agent.usage-recorded") scheduleUsageReload();
      if (folded?.kind === "applied") {
        threadCacheRef.current[activeId] = folded.state;
        setThread(folded.state);
      } else if (folded?.kind !== "stale") {
        scheduleRefresh(activeId);
      }
    });
    refresh(activeId).catch(() => undefined);
    return () => {
      stale = true;
      off();
    };
  }, [activeId, resync, refresh, scheduleRefresh, scheduleBoardReload, scheduleUsageReload, loadDiff]);

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


  // The Worktrees page is environment-wide: every project's list loads when the
  // page opens, and again when a project is added. Git facts are live runs, so
  // there is no timer; the PR poll below keeps the merged highlights current.
  useEffect(() => {
    if (projectId || homePage !== "worktrees" || projects.length === 0) return;
    loadWorktrees(projects.map((p) => p.id)).catch(() => undefined);
  }, [projectId, homePage, projects, loadWorktrees]);

  // The daily prune check lives with the client because the prompt is a client
  // surface. It stays quiet while a sheet is up, then asks only when the forge
  // has answered for every checkout. One completed pass per 24 hours, whether
  // or not it found anything.
  useEffect(() => {
    if (status !== "online" || projects.length === 0 || helpOpen || settingsOpen) return;
    let cancelled = false;
    const check = () => {
      if (cancelled || scanningWorktrees.current || prunePrompt) return;
      if (Date.now() - loadWorktreeScanAt() < WORKTREE_SCAN_MS) return;
      scanWorktrees().catch(() => undefined);
    };
    const first = window.setTimeout(check, WORKTREE_SCAN_START_DELAY_MS);
    const timer = window.setInterval(check, WORKTREE_SCAN_CHECK_MS);
    return () => {
      cancelled = true;
      window.clearTimeout(first);
      window.clearInterval(timer);
    };
  }, [status, projects, helpOpen, settingsOpen, prunePrompt, scanWorktrees]);

  // A stored aside tab that no longer exists (e.g. the removed Worktrees tab)
  // would otherwise render the diff fallback with no tab highlighted.
  useEffect(() => {
    if (!(asideActive in ASIDE_META)) setAsideActive("agents");
  }, [asideActive, setAsideActive]);


  // Remember which projects are open as tabs.
  useEffect(() => {
    setPref("at.openProjects", JSON.stringify(openProjectIds));
  }, [openProjectIds]);

  // Drop tabs whose project was removed elsewhere (settings, another window).
  useEffect(() => {
    if (projects.length === 0) return;
    setOpenProjectIds((prev) => {
      const next = prev.filter((id) => projects.some((p) => p.id === id));
      return next.length === prev.length ? prev : next;
    });
  }, [projects]);

  // Single-letter panel shortcuts (shown in the "+" menu), inactive while typing.
  useEffect(() => {
    if (!activeId || helpOpen) return;
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
      // F opens the Files center tab; the aside letters below come from ASIDE_META.
      // With /vim on, F is the link-hint key instead (Files stays on /files).
      if (e.key.toLowerCase() === "f") {
        if (!vimKeys) setTab("files");
        return;
      }
      const id = (Object.keys(ASIDE_META) as AsideTabId[]).find(
        (k) => ASIDE_META[k].key.toLowerCase() === e.key.toLowerCase(),
      );
      if (id) openPanel(id);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [activeId, openPanel, helpOpen, setTab, vimKeys]);

  // While an agent is open in the detail panel, its log reads once and then
  // follows the agent's streamed output.
  useAgentLog(rpcRef.current, activeId, detailId, resync);

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
          off: r.experimental && !settings?.experimentalRoles,
        };
      }),
    [settings, agents, thread],
  );
  const detailAgent = agents.find((a) => a.id === detailId) ?? null;
  const closeAgent = useCallback(() => setDetailId(null), [setDetailId]);
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
  // The reply a running lead is writing, shown in the transcript where the
  // final message lands. The final message replaces it the moment it folds in.
  const replyLeads = running.filter((a) => !a.parentAgentId).map((a) => a.id).join("\u001f");
  const replyAuthor = thread?.soloRuntime ? "Solo" : "Orchestrator";
  const lastTurn = turnBlocks[turnBlocks.length - 1];
  const replySince =
    lastTurn && !lastTurn.some((m) => m.author === "orchestrator" || m.author === "solo")
      ? lastTurn[0].at
      : undefined;
  // A last turn that ended with no reply says how it ended, so a Stop or a
  // failed harness never leaves the prompt with nothing under it.
  const endedLead =
    replySince && running.length === 0
      ? agents.find(
          (a) =>
            !a.parentAgentId &&
            (a.status === "cancelled" || a.status === "failed") &&
            !!a.endedAt &&
            Date.parse(a.endedAt) >= Date.parse(replySince),
        )
      : undefined;
  const turnOutcome = !endedLead
    ? undefined
    : endedLead.status === "failed"
      ? `Failed${endedLead.reason ? ` — ${endedLead.reason}` : ""}`
      : endedLead.reason === "daemon restarted"
        ? "Stopped — the daemon restarted"
        : "Stopped";
  // Stable until the leads, the turn or the author change, so the reply row
  // (which reads the live log itself) is the only thing a streamed chunk renders.
  const reply = useMemo<ReplySource | null>(
    () =>
      replySince && replyLeads
        ? { leads: new Set(replyLeads.split("\u001f")), since: replySince, author: replyAuthor }
        : null,
    [replyLeads, replySince, replyAuthor],
  );
  // The running turn's start, for the status line's timer: the prompt that
  // opened it, or the first running agent when the transcript has none.
  const turnStartMs =
    lastTurn?.[0]?.author === "user"
      ? Date.parse(lastTurn[0].at)
      : Date.parse(running[0]?.startedAt ?? "");
  const pendingQuestions = (thread?.questions ?? []).filter((q) => q.status === "pending").length;
  const toggleLogsStable = useLatestCallback(() => toggleLogs());
  const stopTurnStable = useLatestCallback(() => void stopTurn());
  // Brings the oldest open question into view and focuses its first control.
  const jumpToQuestion = useCallback(() => {
    const card = streamRef.current?.querySelector<HTMLElement>(".question-card:not(.closed)");
    if (!card) return;
    card.scrollIntoView({ block: "center", behavior: "smooth" });
    card.querySelector<HTMLElement>("input, textarea, button")?.focus({ preventScroll: true });
  }, []);
  // After an answer or skip lands, focus moves to the next open question, or
  // back to the composer once none is left.
  const afterQuestion = useCallback(() => {
    requestAnimationFrame(() => {
      const next = streamRef.current?.querySelector<HTMLElement>(".question-card.pending");
      const target = next?.querySelector<HTMLElement>("input, textarea") ?? taRef.current;
      target?.focus({ preventScroll: !!next });
    });
  }, []);
  /** The active thread's list row, where waiting/controlledBy announce
   * themselves; null while the list has not loaded it yet. */
  const activeSummary = activeId ? threads.find((t) => t.id === activeId) ?? null : null;
  /** The environment's other top-level threads this thread may wait on. */
  const scheduleCandidates = useMemo(
    () => threads.filter((t) => t.id !== activeId && !t.parentThreadId && !t.archived),
    [threads, activeId],
  );
  /** A thread id to its title, falling back to the raw id when the thread is
   * gone (deleted or from another environment). */
  const threadTitle = useCallback(
    (id: string) => threads.find((t) => t.id === id)?.title ?? id,
    [threads],
  );
  const selectedProject = projects.find((p) => p.id === projectId) ?? null;

  // The board is an alternative to the card list, not a replacement: the choice
  // is remembered, and both views read the same thread list.
  const [threadView, setThreadViewState] = useState<ThreadView>(loadThreadView);
  const setThreadView = (view: ThreadView) => {
    setThreadViewState(view);
    setPref("at.threadView", view);
  };
  const [threadStates, setThreadStates] = useState<Record<string, BoardThreadState>>({});
  // The Worktrees tab carries this count so a merged PR is visible without
  // opening the page. The daily scan fills the lists even when it is closed.
  const pruneReadyCount = useMemo(
    () => pruneReady(worktreeRows(projects, worktrees, threadStates)).length,
    [projects, worktrees, threadStates],
  );
  useEffect(() => {
    threadViewRef.current = threadView;
  }, [threadView]);
  // The draft being edited, with the description read when its editor opened.
  const [draftEdit, setDraftEdit] = useState<{ id: string; description: string } | null>(null);
  // Description saves still in flight, by thread: a start waits for them so the
  // first prompt is built from the saved text.
  const describePending = useRef(new Map<string, Promise<unknown>>());
  const openDraftEditor = useCallback(
    async (id: string) => {
      const rpc = rpcRef.current;
      if (!rpc) return;
      try {
        const st = await rpc.call<{ description?: string }>("thread.get", { threadId: id });
        setDraftEdit({ id, description: st.description ?? "" });
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e), null);
      }
    },
    [setError],
  );
  const openBoardThread = useCallback(
    (id: string, draft: boolean) => {
      if (draft) {
        openDraftEditor(id).catch(() => undefined);
        return;
      }
      setActiveId(id);
      if (projectId) lastThreadByProject.current[projectId] = id;
    },
    [projectId, openDraftEditor],
  );

  // Starting a draft: its first turn runs its title and description. The card
  // moves to Running at once and falls back if the start is refused.
  const startDraft = async (id: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    setThreads((prev) =>
      prev.map((t) => (t.id === id ? { ...t, draft: false, runningAgents: 1 } : t)),
    );
    setDraftEdit((prev) => (prev?.id === id ? null : prev));
    try {
      await describePending.current.get(id);
      await rpc.call("thread.turn.start", { threadId: id });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e), null);
    } finally {
      reloadBoardThreads().catch(() => undefined);
    }
  };
  const saveDraftDescription = (id: string, description: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    const saved = rpc
      .call("thread.describe", { threadId: id, description })
      .catch((e) => setError(e instanceof Error ? e.message : String(e), null));
    describePending.current.set(id, saved);
    saved.finally(() => {
      if (describePending.current.get(id) === saved) describePending.current.delete(id);
    });
  };
  const renameDraft = (id: string, title: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    rpc
      .call("thread.rename", { threadId: id, title })
      .then(() => reloadBoardThreads())
      .catch((e) => setError(e instanceof Error ? e.message : String(e), null));
  };
  const newDraft = async () => {
    const rpc = rpcRef.current;
    if (!rpc || !projectId) return;
    try {
      const created = await rpc.call<{ threadId: string }>("thread.create", {
        projectId,
        draft: true,
      });
      await reloadBoardThreads();
      setDraftEdit({ id: created.threadId, description: "" });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e), null);
    }
  };

  // GitLab issues assigned to the user, read when the board opens or the project
  // changes. Tagged with their project so a late reply cannot show on another.
  const [boardIssues, setBoardIssues] = useState<(BoardIssues & { projectId: string }) | null>(null);
  const [issuesLoading, setIssuesLoading] = useState(false);
  // Issue web URLs being turned into drafts: hidden from the column until done.
  const [issuesPending, setIssuesPending] = useState<string[]>([]);
  const loadIssues = useCallback(() => {
    const rpc = rpcRef.current;
    if (!rpc || !projectId) return;
    const pid = projectId;
    setIssuesLoading(true);
    rpc
      .call<BoardIssues>("issues.list", { projectId: pid })
      .then((r) => setBoardIssues({ ...r, projectId: pid }))
      .catch((e) =>
        setBoardIssues({
          projectId: pid,
          status: "error",
          message: e instanceof Error ? e.message : String(e),
          issues: [],
        }),
      )
      .finally(() => setIssuesLoading(false));
  }, [projectId]);
  useEffect(() => {
    if (threadView === "board") loadIssues();
  }, [threadView, loadIssues]);
  // An issue already linked to a thread in this project is not offered again.
  const boardIssueList = useMemo<BoardIssues | null>(() => {
    if (!boardIssues || boardIssues.projectId !== projectId) return null;
    const taken = new Set<string>(issuesPending);
    for (const t of threads) if (t.projectId === projectId && t.issue) taken.add(t.issue.url);
    return { ...boardIssues, issues: boardIssues.issues.filter((i) => !taken.has(i.webUrl)) };
  }, [boardIssues, issuesPending, threads, projectId]);
  const dropIssue = (issue: BoardIssue) => {
    const rpc = rpcRef.current;
    if (!rpc || !projectId) return;
    const url = issue.webUrl;
    if (issuesPending.includes(url)) return;
    setIssuesPending((prev) => [...prev, url]);
    rpc
      .call<{ threadId: string }>("thread.create", {
        projectId,
        draft: true,
        title: issue.title,
        description: issue.description,
        issue: { provider: "gitlab", iid: issue.iid, title: issue.title, url },
      })
      .catch((e) => setError(e instanceof Error ? e.message : String(e), null))
      .finally(() => {
        reloadBoardThreads().catch(() => undefined);
        setIssuesPending((prev) => prev.filter((u) => u !== url));
      });
  };
  // Only threads with a worktree can have a branch PR. The joined id string keeps
  // the poll from restarting when unrelated thread fields change, or when the
  // search narrows the board.
  const boardPRIds = useMemo(
    () =>
      threads
        .filter((t) => t.projectId === projectId && t.worktree)
        .map((t) => t.id)
        .join(","),
    [threads, projectId],
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
  // The Worktrees page reads the same cached PR snapshots as the board. Only
  // the top-level threads that own a checkout are asked about. The poll keeps
  // merged highlights current while the page is open; pr.states is a cache peek,
  // so this costs no git or forge process of its own.
  const worktreePRIds = useMemo(
    () =>
      projectId || homePage !== "worktrees"
        ? ""
        : [
            ...new Set(
              Object.values(worktrees)
                .flatMap((list) => list ?? [])
                .flatMap((w) => w.threadIds),
            ),
          ].join(","),
    [projectId, homePage, worktrees],
  );
  useEffect(() => {
    if (worktreePRIds === "") return;
    let live = true;
    const load = () => {
      rpcRef.current
        ?.call<Record<string, BoardThreadState>>("pr.states", { threadIds: worktreePRIds.split(",") })
        .then((next) => {
          if (live) setThreadStates((prev) => mergeThreadStates(prev, next));
        })
        .catch(() => undefined);
    };
    load();
    // The first peek starts the forge fetch; a short warm check picks up a fast
    // answer without waiting a whole interval.
    const warm = setTimeout(load, WORKTREE_PR_WARM_MS);
    const timer = setInterval(load, WORKTREE_PR_POLL_MS);
    return () => {
      live = false;
      clearTimeout(warm);
      clearInterval(timer);
    };
  }, [worktreePRIds]);
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

  // Roles only offer enabled harnesses. A role already pointing at a disabled
  // harness keeps it as an option so its current value stays visible.
  // Both change only with the harness list, so memoized panels re-render when it does.
  const harnessOptionsFor = useCallback(
    (current: string): DropdownOption[] => {
      const enabled = harnesses.enabled ?? harnesses.all;
      return harnesses.all
        .filter((h) => enabled.includes(h) || h === current)
        .map((h) => ({
          value: h,
          label: enabled.includes(h) ? h : `${h} (disabled)`,
        }));
    },
    [harnesses],
  );

  const modelOptions = useCallback(
    (harness: string, current: string): DropdownOption[] => {
      const opts: DropdownOption[] = [{ value: "", label: "model (default)", group: "Default" }];
      const list = harnesses.models?.[harness] ?? [];
      const merged = current && !list.includes(current) ? [current, ...list] : list;
      for (const m of merged) {
        const slash = m.indexOf("/");
        opts.push({ value: m, label: m, group: slash > 0 ? m.slice(0, slash) : "Other" });
      }
      return opts;
    },
    [harnesses],
  );

  const newThread = async () => {
    const rpc = rpcRef.current;
    if (!rpc || !projectId) return;
    // No title: the server names it, and the orchestrator model renames it from
    // the first prompt, then again once that turn completes.
    const created = await rpc.call<{ threadId: string }>("thread.create", { projectId });
    const list = await rpc.call<ThreadSummary[]>("thread.list");
    setThreads(list);
    selectThread(created.threadId);
  };

  // Stable handles for the memoized thread lists.
  const newThreadStable = useLatestCallback(() => void newThread());
  const selectThreadStable = useLatestCallback((id: string) => selectThread(id));
  const openThreadMenuStable = useLatestCallback(openThreadMenu);
  const startDraftStable = useLatestCallback((id: string) => void startDraft(id).catch(() => undefined));
  const dropIssueStable = useLatestCallback(dropIssue);
  const archiveThreadsStable = useLatestCallback(
    (ids: string[], archived: boolean) => void setThreadsArchived(ids, archived),
  );
  const deleteThreadsStable = useLatestCallback((ids: string[]) => void deleteThreadsById(ids));
  const requestDeleteThreadStable = useLatestCallback(requestDeleteThread);

  const send = async () => {
    const rpc = rpcRef.current;
    const threadId = activeId;
    const { drafts, pastes, attachments, draftImages } = composerStore.get();
    const input = drafts[draftKey] ?? "";
    const text = expandPastes(input, pastes[draftKey]).trim();
    if (!rpc || !threadId || !text) return;
    setRecall(null);
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
    // Images ride the same draft lifecycle: blocked mid-upload or after a
    // failed upload, cleared on send, put back if the turn never starts.
    const sentImages = command ? [] : (draftImages[draftKey] ?? []);
    if (sentImages.some((img) => img.uploading)) {
      setError("Images are still uploading — send again in a moment.", threadId);
      return;
    }
    if (sentImages.some((img) => img.error !== undefined)) {
      setError("Remove the image that failed to upload before sending.", threadId);
      return;
    }
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
    if (sentImages.length > 0) {
      // Previews are revoked here and rebuilt from the kept bytes if the
      // turn fails and the draft has to come back.
      for (const img of sentImages) URL.revokeObjectURL(img.previewUrl);
      setDraftImages((prev) => {
        const rest = { ...prev };
        delete rest[draftKey];
        return rest;
      });
    }
    // Every turn belongs to the daemon, not to the socket that started it:
    // closing the window or dropping the connection must not cancel work.
    const detached = !command && !steer;
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
          images: sentImages.flatMap((img) => (img.ref ? [img.ref.id] : [])),
        });
      } else {
        const params: Record<string, unknown> = {
          threadId,
          text,
          attachments: sentFiles.map((a) => a.path),
        };
        // Image attachments ride as stored ids, before the detach flag: the
        // daemon resolves them when the turn starts.
        if (sentImages.length > 0) {
          params.images = sentImages
            .map((img) => img.ref?.id)
            .filter((id): id is string => id !== undefined);
        }
        // Detached turns run on the daemon's lifetime context, so they keep
        // going when this client goes away.
        params.detach = true;
        await rpc.call("thread.turn.start", params);
      }
    } catch (e) {
      // Put the typed text back into its own thread's draft, even if the user has moved on.
      const key = draftKeyOf(projectId, threadId);
      setDrafts((prev) => ({ ...prev, [key]: input }));
      setPastes((prev) => ({ ...prev, [key]: sentPastes ?? {} }));
      if (sentFiles.length > 0) setAttachments((prev) => ({ ...prev, [key]: sentFiles }));
      if (sentImages.length > 0)
        setDraftImages((prev) => ({
          ...prev,
          [key]: sentImages.map((img) => ({ ...img, previewUrl: URL.createObjectURL(img.file) })),
        }));
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
    const detached = !steer;
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
    const threadId = activeId;
    const method = field === "autoMode" ? "thread.setAutoMode" : "thread.setPeerMailbox";
    await rpc.call(method, { threadId, enabled: value });
    // Callers flip the current value, so the previous one is its negation.
    const name = field === "autoMode" ? "auto mode" : "peer messages";
    history.record({
      label: `${name} ${value ? "on" : "off"}`,
      undo: () => callDaemon(method, { threadId, enabled: !value }),
      redo: () => callDaemon(method, { threadId, enabled: value }),
    });
    refresh(activeId).catch(() => undefined);
  };

  /** Replaces the active thread's auto-mode allowlist; empty inherits the
   * environment's approved models. */
  const setAutoApprovedModels = async (models: Record<string, string[]>) => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId) return;
    try {
      await rpc.call("thread.setAutoApprovedModels", { threadId: activeId, models });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
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

  /** Schedules the active thread: it starts once every listed thread finishes. */
  const scheduleThread = useCallback(async (dependsOn: string[], prompt: string) => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId) return;
    await rpc.call("thread.schedule", { threadId: activeId, dependsOn, prompt });
    refresh(activeId).catch(() => undefined);
  }, [activeId, refresh]);

  /** Cancels the active thread's schedule, back to a plain thread. */
  const unscheduleThread = useCallback(async () => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId) return;
    await rpc.call("thread.unschedule", { threadId: activeId });
    refresh(activeId).catch(() => undefined);
  }, [activeId, refresh]);

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

  /** Turns solo mode on (a non-empty harness) or off (an empty harness);
   * from the next turn the thread runs one agent with no orchestrator team. */
  const setSolo = async (rt: LeadRuntimeView | null) => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId) return;
    try {
      await rpc.call("thread.setSolo", {
        threadId: activeId,
        harness: rt?.harness ?? "",
        model: rt?.model ?? "",
        variant: rt?.variant ?? "",
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
  const stopAgentStable = useLatestCallback(stopAgent);

  /** The shell line that continues an agent's latest run in its harness CLI. */
  const resumeAgent = async (agentId: string): Promise<string> => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId) throw new Error("Not connected to the daemon");
    const res = await rpc.call<{ command: string }>("agents.resume", { threadId: activeId, agentId });
    return res.command;
  };
  const resumeAgentStable = useLatestCallback(resumeAgent);

  /** Convenes the council: members debate in parallel rounds, then a
   *  facilitator synthesizes the outcome into the thread. */
  const startCouncil = useLatestCallback(async () => {
    const rpc = rpcRef.current;
    const threadId = activeId;
    const topic = councilTopic.get(threadId).trim();
    const { rounds: councilRounds, members: councilMembers } = councilSetup.get(threadId);
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
  });

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

  // /btw side answers, a per-thread conversation (ephemeral: never a thread
  // event or message). Each ask appends an entry; closing clears the list.
  const [btw, setBtw] = useState<Record<string, BtwEntry[]>>({});
  const btwSeq = useRef(0);
  // Cmd/Ctrl+P palette: whether it is open, whether it opened from the composer
  // (which swaps the right pane to composer features), and where focus returns.
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [paletteFromComposer, setPaletteFromComposer] = useState(false);
  const paletteReturn = useRef<HTMLElement | null>(null);
  const closePalette = useCallback(() => {
    setPaletteOpen(false);
    const el = paletteReturn.current;
    paletteReturn.current = null;
    if (el?.isConnected) el.focus();
  }, []);
  // The super view: every project's threads in one overlay, grouped running /
  // finished-but-unseen / quiet. Rides the existing 4s thread.list poll.
  const [superViewOpen, setSuperViewOpen] = useState(false);

  // Option+Tab (Alt+Tab elsewhere) opens the all-threads view from anywhere.
  // While it is open, SuperView owns the same chord to step through rows, and
  // releasing the modifier leaves the overlay open.
  useEffect(() => {
    if (superViewOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (!isAllThreadsShortcut(e)) return;
      e.preventDefault();
      setSuperViewOpen(true);
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [superViewOpen]);

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
        if (!cur || cur[cur.length - 1]?.id !== id) return prev;
        return {
          ...prev,
          [threadId]: cur.map((e, i) => (i === cur.length - 1 ? { ...e, ...patch } : e)),
        };
      });
    setBtw((prev) => ({
      ...prev,
      [threadId]: [...(prev[threadId] ?? []), { id, question: q, answer: null, error: null }],
    }));
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
      openHelp: () => setCheatSheetOpen(true),
      askBtw: (q) => askBtw(q),
      toggleAutoMode: () => thread && toggle("autoMode", !thread.autoMode),
      togglePeerMailbox: () => thread && toggle("peerMailbox", !thread.peerMailbox),
      stopTurn: () => stopTurn(),
      newThread: () => newThread(),
      saveDefaults: () => saveThreadDefaults(),
      toggleCompact: () => setCompact((v) => !v),
      toggleVimKeys: () => {
        const next = !vimKeys;
        saveVimKeys(next);
        setVimKeys(next);
      },
    });
  const btwList = activeId ? (btw[activeId] ?? null) : null;

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

  /** Opens the palette; `fromKeys` lets the composer swap in its own features
   * when that is where the shortcut was pressed. */
  const openPalette = useCallback((fromKeys: boolean) => {
    const active = document.activeElement;
    paletteReturn.current = active instanceof HTMLElement ? active : null;
    setPaletteFromComposer(fromKeys && active === taRef.current);
    setPaletteOpen(true);
  }, []);
  // Cmd/Ctrl+K (or P) opens the palette from anywhere. The palette's own input
  // handles the same keys (close) and calls preventDefault, so the
  // defaultPrevented guard stops this listener from reopening it.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.isComposing || !isPaletteKey(e)) return;
      e.preventDefault();
      openPalette(true);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [openPalette]);
  // Where the user goes, for the palette's Recent group. The project is
  // recorded first so a jump into another project's thread puts the thread on top.
  useEffect(() => {
    if (projectId) recordRecent(`project:${projectId}`);
  }, [projectId]);
  useEffect(() => {
    if (activeId) recordRecent(`thread:${activeId}`);
  }, [activeId]);
  // A file picked in the palette: the Files tab opens it (FileEditor reveal).
  const [fileReveal, setFileReveal] = useState<{ threadId: string; path: string; seq: number } | null>(null);
  // A deep link's tab, applied once its thread is the active one (the tab
  // setter writes to whichever thread is active when it runs).
  const pendingTab = useRef<{ threadId: string; tab: RouteTab } | null>(null);
  useEffect(() => {
    const p = pendingTab.current;
    if (!p || p.threadId !== activeId) return;
    pendingTab.current = null;
    setTab(p.tab);
  }, [activeId, setTab]);
  // A starter prompt from the empty chat lands in the composer, unsent.
  const pickStarter = useLatestCallback((text: string) => {
    setInput(text);
    pendingCaret.current = text.length;
    taRef.current?.focus();
  });
  // The Review tab unmounts when left; each thread keeps its place in it.
  const reviewScroll = useScrollMemory(`review:${activeId ?? ""}`);
  const paletteSearchFiles = useLatestCallback(async (query: string): Promise<PaletteItem[]> => {
    const rpc = rpcRef.current;
    const threadId = activeId;
    if (!rpc || !threadId) return [];
    const res = await rpc.call<{ matches: FileMatch[] }>("files.search", { threadId, query, limit: 20 });
    return res.matches
      .filter((m) => !m.dir)
      .map((m) => ({
        id: `file:${m.path}`,
        kind: "file" as const,
        group: "Files",
        label: m.path.slice(m.path.lastIndexOf("/") + 1),
        detail: m.path.includes("/") ? m.path.slice(0, m.path.lastIndexOf("/")) : undefined,
        run: () => {
          setTab("files");
          setFileReveal({ threadId, path: m.path, seq: Date.now() });
        },
      }));
  });
  // Alt/Option+1..6 picks a center tab, even from the composer; "/" outside
  // text fields jumps to the thread search.
  useEffect(() => {
    if (!activeId) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented) return;
      const next = centerTabFromKey(e);
      if (next) {
        e.preventDefault();
        setTab(next);
        return;
      }
      if (e.key === "/" && !e.metaKey && !e.ctrlKey && !e.altKey && !isTyping(e.target)) {
        const search = document.querySelector<HTMLInputElement>(".sidebar .thread-filter");
        if (!search || search.offsetParent === null) return;
        e.preventDefault();
        search.focus();
        search.select();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [activeId, setTab]);
  // Cmd/Ctrl+S toggles compact mode on the thread screen. The files editor
  // claims this keystroke for saving (it calls preventDefault), so editing
  // still saves instead of flipping the layout. Preventing the default also
  // stops the browser's Save Page dialog in the web build, where the same
  // keystroke belongs to the page rather than a native menu.
  useEffect(() => {
    if (!projectId || !activeId) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || !(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey || e.isComposing) return;
      if (e.key.toLowerCase() !== "s") return;
      e.preventDefault();
      setCompact((v) => !v);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [projectId, activeId]);
  // Cmd/Ctrl+Z steps back through reversible changes, Cmd/Ctrl+Shift+Z forward.
  // Text fields keep their own undo, so the keystroke is ignored while typing.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || !(e.metaKey || e.ctrlKey) || e.altKey || e.isComposing) return;
      if (e.key.toLowerCase() !== "z" || isTyping(e.target)) return;
      e.preventDefault();
      if (e.shiftKey) history.redo();
      else history.undo();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [history.undo, history.redo]);
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
  // The memoized composer gets stable handlers that call the latest closures,
  // so a streamed thread update does not re-render it.
  const voiceStop = useLatestCallback(() => voice.stop());
  const voiceCancel = useLatestCallback(() => voice.cancel());
  const composerToggleModels = useLatestCallback(() => toggleModels());
  const composerSend = useLatestCallback(() => {
    send();
  });
  // A queued draft starts as the next turn the moment this thread's turn ends.
  // Only a transition seen on this thread sends it; a queued draft found on an
  // idle thread (after switching back) just becomes a plain draft again.
  const turnIdle = running.length === 0 && !busy;
  const idleSeen = useRef({ key: draftKey, idle: turnIdle });
  useEffect(() => {
    const prev = idleSeen.current;
    idleSeen.current = { key: draftKey, idle: turnIdle };
    if (!turnIdle || !composerStore.get().queued[draftKey]) return;
    unqueue(draftKey);
    if (prev.key === draftKey && !prev.idle) send();
  }, [turnIdle, draftKey]);
  const composerError = useLatestCallback((message: string, threadId?: string) =>
    threadId === undefined ? setError(message) : setError(message, threadId),
  );
  const composerSlashCommands = useLatestCallback(() => slashCommands());
  const composerPickSlash = useLatestCallback((cmd: SlashCommand, runNow: boolean) => pickSlash(cmd, runNow));
  const composerUserPrompts = useLatestCallback(() =>
    (thread?.messages ?? []).filter((m) => m.author === "user").map((m) => m.text),
  );

  /** Makes the view on screen the default for threads that have not set their own. */
  const saveThreadDefaults = useLatestCallback(() => {
    setTabDefault(tab);
    setAsideTabsDefault(asideTabs);
    setAsideActiveDefault(asideActive);
    setLayoutDefault(layout);
    councilSetup.setDefault(councilSetup.get(activeId));
  });

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
  const closeSettings = useCallback(() => setSettingsOpen(false), []);

  /** Persists the shell's keep-running preference. */
  const toggleKeepRunning = useCallback((enabled: boolean) => {
    setKeepRunningState(enabled);
    emitKeepRunning(enabled);
  }, []);

  const updateRole = useCallback((id: string, patch: Partial<RoleConfigView>) => {
    setSettings((prev) =>
      prev
        ? { ...prev, roles: prev.roles.map((r) => (r.id === id ? { ...r, ...patch } : r)) }
        : prev,
    );
  }, []);

  const persistRole = useCallback(async (row: RoleConfigView) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    const s = await rpc.call<SettingsView>("settings.set", {
      role: { id: row.id, harness: row.harness, model: row.model, variant: row.variant },
    });
    setSettings(s);
    flashSaved();
  }, []);

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


  // Where you are, always: an environment page, or project > thread with the
  // thread's live status. The open thread is being seen, so it never reads
  // "Done"; idle and new say nothing.
  const crumbs: Crumb[] = !projectId
    ? [{ label: HOME_LABEL[homePage] }]
    : [
        { label: "Projects", onClick: showProjects },
        {
          label: selectedProject?.name ?? "Project",
          title: selectedProject?.path || selectedProject?.name,
          onClick: activeId ? () => selectThread(null) : undefined,
        },
        ...(activeId ? [{ label: activeSummary?.title || "Thread" }] : []),
      ];
  const liveStatus = activeSummary
    ? threadStatus({
        ...activeSummary,
        unseen: false,
        runningAgents: running.length > 0 || shellThread === activeId ? 1 : 0,
        pendingQuestions: thread ? pendingQuestions : activeSummary.pendingQuestions,
      })
    : null;
  const crumbStatus = liveStatus === "idle" || liveStatus === "new" ? null : liveStatus;
  const titlebar = (
    <div className="titlebar">
      <div className="brand">
        <img src="/brand-mark.svg" alt="" />
        <span>
          Agent<b>Tunnel</b>
        </span>
      </div>
      <HistoryButtons
        canUndo={history.canUndo}
        canRedo={history.canRedo}
        undoLabel={history.undoLabel}
        redoLabel={history.redoLabel}
        onUndo={history.undo}
        onRedo={history.redo}
      />
      <Breadcrumb crumbs={crumbs} status={crumbStatus} />
      <AppUpdate rpc={rpcRef.current} />
      <HarnessUpdates rpc={rpcRef.current} />
      <PaletteButton onOpen={() => openPalette(false)} />
      <StatusPopover
        status={status}
        attached={!!conn?.attached}
        host={conn?.host ?? ""}
        harnesses={enabledHarnesses}
        roles={roles.length}
        onSettings={openSettings}
      />
      <button
        type="button"
        className="title-gear"
        onClick={openSettings}
        aria-label="Settings"
        title="Settings"
      >
        <svg
          viewBox="0 0 24 24"
          width="16"
          height="16"
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
      </button>
    </div>
  );

  // Open projects live in a persistent tab strip, so switching projects is one
  // click and never loses what the other project was doing.
  // Home pages (Projects, Orchestrator) sit in the strip even with no project open.
  const homeTabOn = (page: HomePage) => projectId === null && homePage === page;
  const projectTabs = (
    <div className="project-tabs">
      <div className="env-switch" role="tablist" aria-label="Environment">
        {ENV_PAGES.map((pg) => {
          const on = pg.page !== "all" && homeTabOn(pg.page);
          const open = pg.page === "all" ? () => setSuperViewOpen(true) : pg.page === "projects" ? showProjects : () => showHome(pg.page as HomePage);
          return (
            <div
              key={pg.page}
              className={`ptab pt-home${on ? " on" : ""}`}
              {...tabProps(on, open)}
              title={pg.title}
              aria-label={pg.label}
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
                {pg.icon}
              </svg>
              <span className="ptab-label">{pg.label}</span>
              {pg.page === "worktrees" && pruneReadyCount > 0 && (
                <span className="pt-count ok" title={`${pruneReadyCount} ready to prune`}>
                  {pruneReadyCount}
                </span>
              )}
            </div>
          );
        })}
      </div>
      <span className="pt-divider" aria-hidden="true" />
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
      {draftEdit && threads.some((t) => t.id === draftEdit.id) && (
        <DraftEditor
          key={draftEdit.id}
          title={threads.find((t) => t.id === draftEdit.id)?.title ?? ""}
          description={draftEdit.description}
          onRename={(title) => renameDraft(draftEdit.id, title)}
          onDescribe={(description) => saveDraftDescription(draftEdit.id, description)}
          onStart={() => startDraft(draftEdit.id).catch(() => undefined)}
          onClose={() => setDraftEdit(null)}
        />
      )}
      {renameTarget && (
        <RenameDialog
          key={renameTarget.id}
          initial={renameTarget.title}
          onRename={renameThread}
          onCancel={cancelRename}
        />
      )}
      {deleteTarget && (
        <ModalSheet label="Delete thread" onDismiss={() => setDeleteTarget(null)}>
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
        </ModalSheet>
      )}
      {cleanupConfirm && (
        <ModalSheet label="Clean up threads" onDismiss={() => !cleaningThreads && setCleanupConfirm(false)}>
            <div className="sheet-head">
              <strong>Clean up threads</strong>
              <button disabled={cleaningThreads} onClick={() => setCleanupConfirm(false)}>
                Cancel
              </button>
            </div>
            <div className="sheet-body">
              <p className="hint">
                Delete all {threads.filter((t) => t.projectId === projectId).length} thread
                {threads.filter((t) => t.projectId === projectId).length === 1 ? "" : "s"} in
                this project? Threads disappear from the app and cannot be reopened. Running
                threads may be skipped.
              </p>
              <div className="sheet-actions">
                <button
                  className="danger cleanup-confirm-btn"
                  disabled={cleaningThreads}
                  onClick={() => cleanupAllProjectThreads().catch(() => undefined)}
                >
                  {cleaningThreads ? "Cleaning up…" : "Clean up all threads"}
                </button>
              </div>
            </div>
        </ModalSheet>
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

  // Cmd/Ctrl+P palette. Built only while open, per render, so every action is
  // an arrow that resolves the current handlers and draft.
  const attachFromPalette = () => {
    if (!activeId) return;
    const input = composerStore.get().drafts[draftKey] ?? "";
    const sep = input === "" || /\s$/.test(input) ? "" : " ";
    const next = `${input}${sep}@`;
    setInput(next);
    pendingCaret.current = next.length;
    setCaret(next.length);
    taRef.current?.focus();
  };
  const refreshAll = () => {
    loadProjects().catch(() => undefined);
    if (activeId) refresh(activeId).catch(() => undefined);
  };
  const goToThread = (t: { id: string; projectId?: string }) => {
    if (t.projectId) openWorktreeThread(t.projectId, t.id);
    else selectThread(t.id);
  };
  // Turns that finish or ask while the user is away: title count, dock badge,
  // notifications (lib/attention.ts). A click on a notification opens the thread.
  useAttention({
    threads,
    activeId,
    openThread: (id) => {
      const t = threads.find((x) => x.id === id);
      if (t) goToThread(t);
    },
  });
  // Escape closes the topmost of these confirmations and sheets.
  useEscapeClose(() => setDeleteTarget(null), !!deleteTarget);
  useEscapeClose(() => !cleaningThreads && setCleanupConfirm(false), cleanupConfirm);
  useEscapeClose(() => setReleaseNotes(null), !!releaseNotes);
  // Web UI only: the open thread or project lives in the URL (lib/route.ts).
  useUrlRoute({
    enabled: !inDesktopShell(),
    loaded: status === "online" && (threads.length > 0 || projects.length > 0),
    projectId,
    threadId: activeId,
    tab,
    open: (route: Route) => {
      if (route.kind === "home") {
        showProjects();
        return true;
      }
      if (route.kind === "project") {
        if (!projects.some((p) => p.id === route.id)) return false;
        openProject(route.id);
        setActiveId(null);
        return true;
      }
      const t = threads.find((x) => x.id === route.id);
      if (!t) return false;
      if (t.id === activeId) setTab(route.tab ?? "chat");
      else pendingTab.current = { threadId: t.id, tab: route.tab ?? "chat" };
      goToThread(t);
      return true;
    },
  });

  // A dropped socket keeps the app on screen while it reconnects; only a
  // daemon that never answered shows the splash.
  if (status === "connecting" || status === "offline") {
    return (
      <div className="splash">
        <img src="/brand-mark.svg" alt="" />
        <div className="mark">AgentTunnel</div>
        <div className="splash-sub">
          {status === "connecting" ? "Starting the daemon…" : "Connecting…"}
        </div>
        {status === "offline" && (
          <>
            {error && <div className="err-text">{error}</div>}
            <button onClick={retryConnect}>Retry</button>
          </>
        )}
      </div>
    );
  }
  // Floats under the titlebar (.link-banner): it takes no grid row or flex slot, and stays clear of the composer.
  const linkBanner = linkDown ? (
    <div className="link-banner" role="status" aria-live="polite">
      Reconnecting to the daemon. Live output is paused until it returns.
    </div>
  ) : null;
  const buildPaletteView = (): { left: PaletteSection[]; right: PaletteSection[]; rightTitle: string } => {
    const left = groupItems(
      buildPaletteCommands<AsideTabId>({
        hasProject: !!projectId,
        hasThread: !!activeId,
        busy,
        autoMode: !!thread?.autoMode,
        peerMailbox: !!thread?.peerMailbox,
        compact,
        panels: ASIDE_META,
        pinned: ASIDE_PINNED,
        isPanelOpen: (id) => asideTabs.includes(id),
        togglePanel: (id) => (asideTabs.includes(id) ? closePanel(id) : openPanel(id)),
        setTab: (t) => setTab(t),
        newThread: () => newThread(),
        createProject: () => addProject(),
        openSettings: () => openSettings(),
        openSuperView: () => setSuperViewOpen(true),
        refresh: () => refreshAll(),
        stopTurn: () => stopTurn(),
        toggleAutoMode: () => thread && toggle("autoMode", !thread.autoMode),
        togglePeerMailbox: () => thread && toggle("peerMailbox", !thread.peerMailbox),
        toggleCompact: () => setCompact((v) => !v),
        goToPage: (page) => showHome(page),
        openHelp: () => setCheatSheetOpen(true),
      }),
    );
    if (paletteFromComposer && activeId) {
      const right = groupItems(
        composerItems({
          attach: () => attachFromPalette(),
          modelConfig: () => toggleModels(),
          slash: slashCommands(),
          runSlash: (cmd) => pickSlash(cmd, true),
        }),
      );
      return { left, right, rightTitle: "Composer" };
    }
    const names = new Map(projects.map((p) => [p.id, p.name]));
    // Every place, wherever the palette opens: the current project's threads
    // lead, and the places opened last lead everything.
    const threadRows: PaletteItem[] = threadItems(
      threads.filter((t) => t.id !== activeId && !t.archived),
      names,
      projectId,
      goToThread,
    );
    const projectRows: PaletteItem[] = projectItems(
      projects.filter((p) => p.id !== projectId),
      (id) => openProject(id),
    );
    return {
      left,
      right: groupItems(withRecent([...threadRows, ...projectRows], recentIds())),
      rightTitle: "Go to",
    };
  };
  const palette = paletteOpen ? buildPaletteView() : null;
  const runPalette = (item: PaletteItem) => {
    closePalette();
    item.run();
  };
  const tourDialog = tourOpen && <FirstRunTour onClose={() => setTourOpen(false)} />;
  // /help, rendered on every screen since the palette can open it from any.
  const helpSheet = cheatSheetOpen && (
    <CheatSheet panels={Object.values(ASIDE_META)} onClose={() => setCheatSheetOpen(false)} />
  );
  const paletteDialog = palette && (
    <CommandPalette
      left={palette.left}
      right={palette.right}
      rightTitle={palette.rightTitle}
      searchFiles={activeId && !paletteFromComposer ? paletteSearchFiles : undefined}
      onRun={runPalette}
      onClose={closePalette}
    />
  );
  // The super view reads the same thread.list data the views poll; a row opens
  // the thread the way the palette does, including across projects.
  const superViewDialog = superViewOpen && (
    <SuperView
      threads={threads}
      projects={projects}
      initialThreadId={activeId}
      onOpen={(t) => {
        setSuperViewOpen(false);
        // A row click always clears the unseen badge, even for the thread
        // already open: switching to the same activeId does not re-run the
        // open-time markSeen in the subscription effect.
        const rpc = rpcRef.current;
        if (rpc) rpc.call("thread.markSeen", { threadId: t.id }).catch(() => undefined);
        goToThread(t);
      }}
      onClose={() => setSuperViewOpen(false)}
    />
  );

  // Add Project in a plain browser: the daemon-backed folder browser stands in
  // for the native picker. Rendered beside the palette on every screen the
  // palette's create-project command can run from.
  const newProjectDialog = newProjectOpen && (
    <FolderBrowser
      rpc={rpcRef.current}
      onPick={(path) => createProjectAt(path)}
      onClose={() => setNewProjectOpen(false)}
    />
  );

  // Rendered on every screen: settings are app-global, so the titlebar gear
  // must open the sheet wherever the user is.
  const settingsSheet = settingsOpen && settings && (
    <SettingsSheet
      rpc={rpcRef.current}
      settings={settings}
      setSettings={setSettings}
      harnesses={harnesses}
      loadHarnesses={loadHarnesses}
      attached={!!conn?.attached}
      keepRunning={keepRunning}
      onToggleKeepRunning={toggleKeepRunning}
      onSaveThreadDefaults={saveThreadDefaults}
      onClose={closeSettings}
      harnessOptionsFor={harnessOptionsFor}
      modelOptions={modelOptions}
      refreshModels={refreshModels}
      addModel={addModel}
      updateRole={updateRole}
      persistRole={persistRole}
    />
  );

  // The daily scan's question is app-global: it can arrive on any screen, so it
  // renders beside the settings sheet rather than on the Worktrees page alone.
  const pruneDialog = prunePrompt && (
    <PrunePrompt
      candidates={prunePrompt.candidates}
      dirtyCount={prunePrompt.dirtyCount}
      busy={pruneBusy}
      error={pruneError}
      onPrune={() => pruneFromPrompt().catch(() => undefined)}
      onReview={() => {
        setPrunePrompt(null);
        setPruneError(null);
        showHome("worktrees");
      }}
      onDismiss={() => {
        setPrunePrompt(null);
        setPruneError(null);
      }}
    />
  );

  // Shared by the per-screen returns below. Later overlays paint over earlier
  // ones at equal z-index, so this order is load-bearing. The main return keeps
  // its own order: it interleaves these with the detail and release sheets.
  const overlays = (
    <>
      {superViewDialog}
      {newProjectDialog}
      {paletteDialog}
      {tourDialog}
      {helpSheet}
      {settingsSheet}
      {pruneDialog}
    </>
  );

  // Worktrees are their own page, across every project: the checkouts are a
  // shared resource, so they are visible even with no project open.
  if (!projectId && homePage === "worktrees") {
    return (
      <div className="screen">
        {linkBanner}
        {titlebar}
        {projectTabs}
        <div className="screen-body">
          <div className="screen-head">
            <div className="screen-title">
              <h2>Worktrees</h2>
              <p className="screen-sub">
                Every project's isolated checkouts. Merged pull requests are marked ready to prune.
              </p>
            </div>
          </div>
          <WorktreesPage
            projects={projects}
            worktrees={worktrees}
            threads={threads}
            states={threadStates}
            loading={worktreesLoading}
            error={worktreesError}
            onRefresh={() => loadWorktrees(projects.map((p) => p.id)).catch(() => undefined)}
            onOpenThread={openWorktreeThread}
            onRemove={(projectId, path, force) =>
              worktreeAction(projectId, "worktree.remove", { path, force })
            }
            onPrune={(projectId) => worktreeAction(projectId, "worktree.prune", {})}
            onPruneMerged={pruneMergedWorktrees}
          />
        </div>
        {overlays}
      </div>
    );
  }

  // Stats are their own page, not part of the projects grid.
  if (!projectId && homePage === "stats") {
    return (
      <div className="screen">
        {linkBanner}
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
        {overlays}
      </div>
    );
  }

  // The master orchestrator has its own page; it never shares the projects grid.
  if (!projectId && homePage === "orchestrator") {
    return (
      <div className="screen">
        {linkBanner}
        {titlebar}
        {projectTabs}
        <div className="screen-body master-page">
          <MasterChat
            rpc={rpcRef.current}
            projects={projects}
            harnessOptionsFor={harnessOptionsFor}
            modelOptions={modelOptions}
            refreshModels={refreshModels}
            onOpenThread={(projectId, threadId) => {
              openProject(projectId);
              lastThreadByProject.current[projectId] = threadId;
              setActiveId(threadId);
            }}
          />
        </div>
        {overlays}
      </div>
    );
  }

  // Screen 1 — the projects configured in this environment.
  if (!projectId) {
    return (
      <div className="screen">
        {linkBanner}
        {titlebar}
        {projectTabs}
        <div className="screen-body">
          {projects.length === 0 ? (
            // First run: one thing to do.
            <div className="first-run">
              <img src="/brand-mark.svg" alt="" />
              <h2>Open a folder to start</h2>
              <p>
                A project is a folder on this machine. Each thread in it gets a team of agents and,
                in a git repository, its own worktree.
              </p>
              <button type="button" className="primary first-run-cta" onClick={() => void addProject()}>
                Open a folder…
              </button>
            </div>
          ) : (
          <>
          <div className="screen-head">
            <div className="screen-title">
              <h2>Projects</h2>
              <p className="screen-sub">
                {`${projects.length} project${projects.length === 1 ? "" : "s"}`}
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
          </>
          )}
        </div>
        {overlays}
      </div>
    );
  }

  // Screen 2 — the threads of the selected project.
  if (!activeId) {
    return (
      <div className="screen">
        {linkBanner}
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
              <ThreadSearch />
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
              {threadView === "board" && <button onClick={newDraft}>New draft</button>}
              {threadView === "board" && <button onClick={newThread}>New thread</button>}
              <button
                className={`cleanup-threads-btn${cleaningThreads ? " is-cleaning" : ""}`}
                onClick={() => setCleanupConfirm(true)}
                disabled={cleaningThreads || !threads.some((t) => t.projectId === projectId)}
                title="Clean up all threads in this project"
                aria-label="Clean up all threads"
              >
                <svg
                  className="cleanup-icon"
                  viewBox="0 0 16 16"
                  width="14"
                  height="14"
                  aria-hidden="true"
                  focusable="false"
                >
                  <path
                    d="M6.2 2.2 7.5 1l1.3 1.2-.4 1.1h3.1a.7.7 0 0 1 0 1.4h-.5l-.8 7.2a1.8 1.8 0 0 1-1.8 1.6H7.6a1.8 1.8 0 0 1-1.8-1.6L5 4.7h-.5a.7.7 0 0 1 0-1.4h3.1l-.4-1.1Zm.2 2.5.7 6.8c0 .2.2.4.5.4h.8c.3 0 .5-.2.5-.4l.7-6.8H6.4Zm-3.6.2.5 4.4a.7.7 0 1 1-1.4.2L1.4 5a.7.7 0 1 1 1.4-.1Zm10.8 0a.7.7 0 1 1 1.4.1l-.5 4.5a.7.7 0 1 1-1.4-.2l.5-4.4Z"
                    fill="currentColor"
                  />
                </svg>
                <span>{cleaningThreads ? "Cleaning…" : "Clean up"}</span>
              </button>
            </div>
          </div>
          <ProjectThreads
            threads={threads}
            projectId={projectId}
            view={threadView}
            states={threadStates}
            issues={boardIssueList}
            issuesLoading={issuesLoading}
            onNew={newThreadStable}
            onSelect={selectThreadStable}
            onMenu={openThreadMenuStable}
            onOpen={openBoardThread}
            onStartDraft={startDraftStable}
            onDropIssue={dropIssueStable}
            onRefreshIssues={loadIssues}
            onArchive={archiveThreadsStable}
            onDelete={deleteThreadsStable}
          />
          {threadOverlays}
          {overlays}
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
  // A finished turn in a thread with its own worktree points at what comes
  // next. Only once the turn has ended and nothing waits on the user.
  const lastMsg = lastTurn?.[lastTurn.length - 1];
  const showNextStep =
    !!activeSummary?.worktree &&
    running.length === 0 &&
    shellThread !== activeId &&
    !!lastMsg &&
    lastMsg.author !== "user" &&
    lastMsg.author !== "shell" &&
    pendingQuestions === 0;
  const asideCollapsed = !layout.asideOpen || setupMode;
  // A solo thread's identity hue tints its chat-head tabs (the card and row
  // match) and, as the accent, the whole shell below.
  const soloTint = thread?.soloRuntime ? `solo-identity ${soloHue(thread.id)}` : "";
  const soloShell = thread?.soloRuntime ? ` solo-accent ${soloHue(thread.id)}` : "";

  return (
    <DocLinkContext.Provider value={openChatDoc}>
    <div
      className={`app${layout.sidebarOpen ? "" : " sidebar-closed"}${asideCollapsed ? " aside-closed" : ""}${compact ? " compact" : ""}${drawers.className}${soloShell}`}
      ref={shellRef}
      style={
        {
          "--sidebar-w": `${layout.sidebarW}px`,
          "--aside-w": `${layout.asideW}px`,
        } as CSSProperties
      }
    >
      {linkBanner}
      {titlebar}
      {projectTabs}
      {drawers.open && <div className="drawer-scrim" onClick={drawers.close} aria-hidden="true" />}

      <div className={`col sidebar${layout.sidebarOpen || drawers.sidebarDrawer ? "" : " collapsed"}`}>
        <div className="col-head">
          <PanelToggle
            side="left"
            open={layout.sidebarOpen || drawers.sidebarDrawer}
            onToggle={() =>
              drawers.sidebarDrawer
                ? drawers.close()
                : setLayout((l) => ({ ...l, sidebarOpen: !l.sidebarOpen }))
            }
          />
          <button className="new-thread-btn" onClick={newThread} title="New thread in this project (/new)">
            + New thread
          </button>
        </div>
        <ThreadList
          threads={threads}
          projectId={projectId}
          activeId={activeId}
          onSelect={selectThreadStable}
          onMenu={openThreadMenuStable}
          onDelete={requestDeleteThreadStable}
        />
        <SidebarFooter rpc={rpcRef.current} />
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
          <DrawerToggle side="sidebar" open={drawers.open === "sidebar"} onToggle={drawers.toggle} />
          <div className="tabs" role="tablist" aria-label="Thread view">
            <div
              className={`tab ${tab === "chat" ? "on" : ""}${soloTint ? ` ${soloTint}` : ""}`}
              {...tabProps(tab === "chat", () => setTab("chat"))}
              title={`Chat (${centerTabHint("chat")})`}
            >
              Chat
            </div>
            <div
              className={`tab ${tab === "review" ? "on" : ""}${soloTint ? ` ${soloTint}` : ""}`}
              {...tabProps(tab === "review", () => setTab("review"))}
              title={`Review (${centerTabHint("review")})`}
            >
              Review
              {comments.length > 0 && <span className="pill">{comments.length}</span>}
            </div>
            <div
              className={`tab ${tab === "files" ? "on" : ""}${soloTint ? ` ${soloTint}` : ""}`}
              {...tabProps(tab === "files", () => setTab("files"))}
              title={`Files (${centerTabHint("files")})`}
            >
              Files
            </div>
            <div
              className={`tab ${tab === "graph" ? "on" : ""}${soloTint ? ` ${soloTint}` : ""}`}
              {...tabProps(tab === "graph", () => setTab("graph"))}
              title={`Graph (${centerTabHint("graph")})`}
            >
              Graph
              {(thread?.mailbox?.length ?? 0) > 0 && (
                <span className="pill">{thread?.mailbox?.length}</span>
              )}
            </div>
            <div
              className={`tab ${tab === "p2p" ? "on" : ""}${soloTint ? ` ${soloTint}` : ""}`}
              {...tabProps(tab === "p2p", () => setTab("p2p"))}
              title={`Agent messages (${centerTabHint("p2p")})`}
            >
              P2P
              {(thread?.mailbox?.length ?? 0) > 0 && (
                <span className="pill">{thread?.mailbox?.length}</span>
              )}
            </div>
            <div
              className={`tab ${tab === "board" ? "on" : ""}${soloTint ? ` ${soloTint}` : ""}`}
              {...tabProps(tab === "board", () => setTab("board"))}
              title={`Task board (${centerTabHint("board")})`}
            >
              Board
              {agents.some((a) => a.role !== "orchestrator") && (
                <span className="pill">{agents.filter((a) => a.role !== "orchestrator").length}</span>
              )}
            </div>
            {docPath !== null && (
              <div
                className={`tab ${tab === "doc" ? "on" : ""}`}
                title={docPath}
                {...tabProps(tab === "doc", () => setTab("doc"))}
              >
                {docPath.slice(docPath.lastIndexOf("/") + 1)}
                <button
                  type="button"
                  className="tab-x"
                  title="Close document"
                  aria-label={`Close ${docPath}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    closeDoc();
                  }}
                >
                  ×
                </button>
              </div>
            )}
          </div>
          <div className="toggles">
            <TeamProgress
              agents={agents}
              questions={thread?.questions}
              tasks={thread?.tasks}
              onClick={() => openPanel("agents")}
            />
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
                <span className="pb-label">Project board</span>
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
          {!setupMode && (
            <DrawerToggle side="aside" open={drawers.open === "aside"} onToggle={drawers.toggle} />
          )}
        </div>

        {/* Kept mounted while hidden so unsaved buffers survive a tab switch.
            `active` gates the first fs.list load until the tab is first shown. */}
        {activeId && (
          <div className="files-center" hidden={tab !== "files"}>
            <FileEditor
              key={activeId}
              rpc={rpcRef.current}
              threadId={activeId}
              active={tab === "files"}
              reveal={fileReveal?.threadId === activeId ? fileReveal : null}
            />
          </div>
        )}

        {tab === "files" ? null : tab === "chat" ? (
          <>
            <div
              className="stream"
              ref={streamRef}
              onScroll={(e) => {
                pinnedRef.current = nearBottom(e.currentTarget);
                setAtBottom(pinnedRef.current);
                setAtTop(e.currentTarget.scrollTop > TOP_SHOW);
              }}
            >
              {/* Zero-height sticky anchor: mounting the jump-to-top pill
                  never shifts the transcript (it overlays, unlike a plain
                  sticky sibling, which would push content down). */}
              <div className="scroll-top-anchor">
                {atTop && (
                  <button
                    type="button"
                    className="scroll-top"
                    onClick={scrollToTop}
                    aria-label="Jump to top of thread"
                    title="Jump to top of thread"
                  >
                    ↑
                  </button>
                )}
              </div>
              {activeSummary?.waiting && (
                <div className="schedule-banner">
                  <div className="schedule-banner-main">
                    <div className="schedule-banner-title">
                      <span className="pill wait">◷ Scheduled</span>
                      <span className="schedule-waiting">
                        waiting on{" "}
                        {activeSummary.waiting.dependsOn
                          .map((id) => threads.find((t) => t.id === id)?.title ?? id)
                          .join(", ")}
                      </span>
                    </div>
                    <div className="schedule-prompt" title={activeSummary.waiting.prompt}>
                      {activeSummary.waiting.prompt}
                    </div>
                  </div>
                  <button
                    type="button"
                    className="schedule-cancel"
                    onClick={() => {
                      unscheduleThread().catch((e) =>
                        setError(e instanceof Error ? e.message : String(e)),
                      );
                    }}
                  >
                    Cancel
                  </button>
                </div>
              )}
              {activeSummary?.controlledBy && (
                <button
                  type="button"
                  className="controlled-link"
                  title="Open the thread that relaunched this one"
                  onClick={() => {
                    if (activeSummary.controlledBy) selectThread(activeSummary.controlledBy);
                  }}
                >
                  ⌁ Controlled by{" "}
                  {threads.find((t) => t.id === activeSummary.controlledBy)?.title ??
                    activeSummary.controlledBy}
                </button>
              )}
              {setupMode && thread && (
                <div className="setup-view">
                  <ThreadWizard
                    key={activeId}
                    settings={settings}
                    thread={thread}
                    toggle={toggle}
                    addLead={addLead}
                    addingLead={addingLead}
                    setLeadRuntime={setLeadRuntime}
                    setSolo={setSolo}
                    setAutoApprovedModels={setAutoApprovedModels}
                    harnessInfo={harnesses}
                    updateRole={updateRole}
                    persistRole={persistRole}
                    refreshModels={refreshModels}
                    addModel={addModel}
                    modelOptions={modelOptions}
                    harnessOptionsFor={harnessOptionsFor}
                    toggleRoleEnabled={toggleRoleEnabled}
                    setStatusPeriod={setStatusPeriod}
                    summary={activeSummary}
                    scheduleCandidates={scheduleCandidates}
                    threadTitle={threadTitle}
                    schedule={scheduleThread}
                    unschedule={unscheduleThread}
                    onStart={() => setSetupReadyId(activeId)}
                    startLabel={`Start ${startVerb}...`}
                  />
                </div>
              )}
              {activeId && !thread && !error && <div className="empty-state">Loading thread…</div>}
              {!setupMode && thread && (thread.messages?.length ?? 0) === 0 && running.length === 0 && (
                <EmptyChat solo={!!thread.soloRuntime} onStarter={pickStarter} />
              )}
              <MessageActionsContext.Provider value={messageActions}>
                <MessageImageContext.Provider value={messageImages}>
                  {turnBlocks.map((turn, i) => (
                    <TurnBlock
                      key={turn[0].id}
                      turn={turn}
                      index={messageIndex}
                      total={messageTotal}
                      streaming={i === turnBlocks.length - 1 ? reply : null}
                      onStreamGrow={followStream}
                      changes={i === turnBlocks.length - 1 && running.length === 0 && !busy}
                      outcome={i === turnBlocks.length - 1 ? turnOutcome : undefined}
                    />
                  ))}
                </MessageImageContext.Provider>
              </MessageActionsContext.Provider>
              {(thread?.questions ?? []).map(
                (q) =>
                  q.status !== "pending" && (
                    <QuestionCard
                      key={q.questionId}
                      question={q}
                      askedBy={agentLabel(q.agentId)}
                      onAnswer={(selected, text) => answerQuestion(q.questionId, selected, text)}
                      onDismiss={() => dismissQuestion(q.questionId)}
                    />
                  ),
              )}
              {showNextStep && (
                <NextStep
                  hasPR={!!(activeId && threadStates[activeId]?.pr)}
                  onFiles={() => setTab("files")}
                  onPR={() => {
                    if (activeId && threadStates[activeId]?.pr) {
                      openPanel("pr");
                      return;
                    }
                    setInput("Open a pull request for these changes.");
                    taRef.current?.focus();
                  }}
                />
              )}
              {running.length > 0 && (
                <TurnActivity
                  key={`${activeId}:${turnStartMs}`}
                  leads={replyLeads}
                  since={turnStartMs}
                  helpers={running.length - (replyLeads ? replyLeads.split("\u001f").length : 0)}
                  waiting={pendingQuestions}
                  logsOpen={logsOpen}
                  onToggleLogs={toggleLogsStable}
                  onStop={stopTurnStable}
                  onJumpToQuestion={jumpToQuestion}
                  onLines={followStream}
                  agents={thread?.agents ?? undefined}
                  reply={reply}
                  threadId={activeId}
                />
              )}
              {/* Open questions sit last, next to the composer, so the working
                  log can never push them out of view. */}
              {(thread?.questions ?? []).map(
                (q) =>
                  q.status === "pending" && (
                    <QuestionCard
                      key={q.questionId}
                      question={q}
                      askedBy={agentLabel(q.agentId)}
                      onAnswer={(selected, text) => answerQuestion(q.questionId, selected, text)}
                      onDismiss={() => dismissQuestion(q.questionId)}
                      onSettled={afterQuestion}
                    />
                  ),
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
              {error && <div className="empty-state err-text">Something went wrong: {error}</div>}
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

            <Composer
              activeId={activeId}
              draftKey={draftKey}
              agentMode={running.length > 0 || busy}
              stoppable={running.length > 0 || busy || shellThread === activeId}
              onStop={stopTurnStable}
              hidden={composerHidden}
              taRef={taRef}
              pendingCaret={pendingCaret}
              rpcRef={rpcRef}
              connRef={connRef}
              listening={listening}
              onVoiceStop={voiceStop}
              onVoiceCancel={voiceCancel}
              btwList={btwList}
              onCloseBtw={closeBtw}
              projectName={selectedProject?.name ?? null}
              projectPath={selectedProject?.path ?? null}
              modelsOpen={modelsOpen}
              onToggleModels={composerToggleModels}
              onSend={composerSend}
              onError={composerError}
              slashCommands={composerSlashCommands}
              onPickSlash={composerPickSlash}
              userPrompts={composerUserPrompts}
            />
          </>
        ) : tab === "review" ? (
          <div className="stream" key={activeId ?? ""} ref={reviewScroll.ref} onScroll={reviewScroll.onScroll}>
            {diffError ? (
              <div className="empty-state err-text">Could not load changes: {diffError}</div>
            ) : diff === null ? (
              <div className="empty-state">Loading changes…</div>
            ) : null}
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
            <KanbanBoard agents={agents} roleNames={rolesById} questions={thread?.questions} onOpenAgent={openAgent} />
          </div>
        ) : tab === "doc" && docPath !== null ? (
          <Suspense fallback={<div className="stream empty">Loading…</div>}>
            <MarkdownViewer
              path={docPath}
              doc={doc}
              loading={docLoading}
              error={docError}
              onOpenPath={openDoc}
              onClose={closeDoc}
            />
          </Suspense>
        ) : tab === "p2p" ? (
          <PeerChat
            key={activeId ?? ""}
            agents={agents}
            roleNames={rolesById}
            mailbox={thread?.mailbox ?? null}
            peerMailbox={!!thread?.peerMailbox}
            onTogglePeerMailbox={() =>
              thread && toggle("peerMailbox", !thread.peerMailbox)
            }
          />
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
            onOpenAgent={openAgent}
            questions={thread?.questions}
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

      <div className={`col aside${asideCollapsed && !drawers.asideDrawer ? " collapsed" : ""}`}>
        <div className="aside-tabs" role="tablist" aria-label="Thread details">
          <PanelToggle
            side="right"
            open={!asideCollapsed || drawers.asideDrawer}
            onToggle={() => {
              if (drawers.asideDrawer) drawers.close();
              else if (!setupMode) setLayout((l) => ({ ...l, asideOpen: !l.asideOpen }));
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
          {asideActive === "models" ? (
            <div className="aside-models">
              <ThreadSetup
                settings={settings}
                thread={thread}
                toggle={toggle}
                addLead={addLead}
                addingLead={addingLead}
                setLeadRuntime={setLeadRuntime}
                setSolo={setSolo}
                setAutoApprovedModels={setAutoApprovedModels}
                harnessInfo={harnesses}
                updateRole={updateRole}
                persistRole={persistRole}
                refreshModels={refreshModels}
                addModel={addModel}
                modelOptions={modelOptions}
                harnessOptionsFor={harnessOptionsFor}
                toggleRoleEnabled={toggleRoleEnabled}
                setStatusPeriod={setStatusPeriod}
                summary={activeSummary}
                scheduleCandidates={scheduleCandidates}
                threadTitle={threadTitle}
                schedule={scheduleThread}
                unschedule={unscheduleThread}
              />
            </div>
          ) : asideActive === "agents" ? (
            <>
              <div className="section">
                <h3>
                  Team <span>Σ {fmtTokens(totals.total)} tok</span>
                </h3>
                <TeamProgress agents={agents} questions={thread?.questions} tasks={thread?.tasks} />
                <AgentTree
                  threadId={activeId ?? ""}
                  agents={agents}
                  questions={thread?.questions}
                  usage={thread?.usage}
                  roleNames={rolesById}
                  onOpen={openAgent}
                  onStop={stopAgentStable}
                />
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
                      {...(r.latestId ? clickable(() => openAgent(r.latestId!)) : {})}
                    >
                      <div className="role-head">
                        <span className={`dot ${r.running.length > 0 ? "running" : r.off ? "off" : r.latestStatus}`} />
                        <span className="role-name">{r.name}</span>
                        <span className="harness" title={`${r.harness} · ${r.model}`}>
                          {r.harness}
                          {r.model ? ` · ${r.model}` : ""}
                        </span>
                      </div>
                      <div className="role-meta">
                        {r.running.length > 0
                          ? `${r.running.length} running`
                          : r.off
                            ? "off · enable Experimental roles in Settings"
                            : r.latestId
                              ? `last: ${r.latestStatus}`
                              : "idle"}
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
                        <tr key={a.id} className="rowlink" onClick={() => openAgent(a.id)}>
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
          <InheritedPanel rpcRef={rpcRef} activeId={activeId} onError={setError} />
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
        ) : asideActive === "council" ? (
          <CouncilPanel
            activeId={activeId}
            roles={roles}
            busy={busy}
            harnessOptionsFor={harnessOptionsFor}
            modelOptions={modelOptions}
            refreshModels={refreshModels}
            onStart={startCouncil}
          />
        ) : (
          <>
            <div className="section diff-section">
              {diffError ? (
                <div className="empty-state err-text">Could not load changes: {diffError}</div>
              ) : diff === null ? (
                <div className="empty-state">Loading changes…</div>
              ) : null}
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
          roleName={rolesById[detailAgent.role] || detailAgent.role}
          agents={agents}
          roleNames={rolesById}
          questions={thread?.questions}
          initialTab={detailTab}
          onOpenAgent={openAgent}
          onResume={resumeAgentStable}
          onClose={closeAgent}
        />
      )}

      {releaseNotes && (
        <div className="overlay" onClick={() => setReleaseNotes(null)}>
          <section
            className="sheet notes-sheet"
            role="dialog"
            aria-modal="true"
            aria-labelledby="release-notes-title"
            onClick={(e) => e.stopPropagation()}
          >
            <header className="sheet-head notes-head">
              <button
                type="button"
                className="notes-close"
                onClick={() => setReleaseNotes(null)}
                aria-label="Close release notes"
                title="Close"
              >
                ×
              </button>
              <strong id="release-notes-title">AgentTunnel {releaseNotes.version}</strong>
            </header>
            <div className="sheet-body">
              {releaseNotes.body.trim() ? (
                <Markdown text={releaseNotes.body} />
              ) : (
                <p className="hint">This release has no notes.</p>
              )}
            </div>
          </section>
        </div>
      )}

      {settingsSheet}
      {pruneDialog}

      {threadOverlays}
      {superViewDialog}
      {newProjectDialog}
      {paletteDialog}
      {tourDialog}
      {helpSheet}
    </div>
    </DocLinkContext.Provider>
  );
}
