import { useCallback, useEffect, useMemo, useRef, useState, type MouseEvent } from "react";
import { discover, pickFolder, Rpc, type Conn } from "./lib/rpc";
import { bentoTile, useBentoColumns } from "./lib/bento";
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
import { DiffView } from "./components/DiffView";
import { Dropdown, type DropdownOption } from "./components/Dropdown";
import { FilesPanel, type FileContentLike, type FileEntryLike } from "./components/FilesPanel";
import { Markdown } from "./components/Markdown";
import { AgentDetail } from "./components/AgentDetail";
import { ThreadInfoDialog } from "./components/ThreadInfoDialog";

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
  messages: Message[] | null;
  agents: Record<string, Agent> | null;
  agentOrder: string[] | null;
  usage: Record<string, UsageEntry> | null;
  steps: Record<string, UsageStep[]> | null;
  turn: number;
  turns: TurnUsage[] | null;
  shared: Record<string, string> | null;
  reviewComments: ReviewComment[] | null;
};
type ThreadSummary = {
  id: string;
  title: string;
  projectId?: string;
  updatedAt?: string;
  messages: number;
  agents: number;
  autoMode: boolean;
};
type Project = { id: string; name: string; path: string; threads: number };
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
};

/** Aside panel tabs: agents + usage are pinned; the rest are addable. */
type AsideTabId = "agents" | "usage" | "context" | "files" | "diff";
const ASIDE_PINNED: AsideTabId[] = ["agents", "usage"];
const ASIDE_META: Record<AsideTabId, { label: string; key: string }> = {
  agents: { label: "Agents", key: "A" },
  usage: { label: "Token usage", key: "U" },
  context: { label: "Context", key: "C" },
  files: { label: "Files", key: "F" },
  diff: { label: "Diff", key: "D" },
};
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

  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [detailId, setDetailId] = useState<string | null>(null);
  // Aside panel tabs: agents + usage are pinned; context, files, and diff can
  // be added via the "+" menu (or single-letter shortcuts) and closed again.
  const [asideTabs, setAsideTabs] = useState<AsideTabId[]>(loadAsideTabs);
  const [asideActive, setAsideActive] = useState<AsideTabId>("agents");
  const [panelMenu, setPanelMenu] = useState<{ x: number; y: number } | null>(null);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [live, setLive] = useState<{ agentId: string; kind: string; text: string }[]>([]);

  const [tab, setTab] = useState<"chat" | "review">("chat");
  const [diff, setDiff] = useState<Diff | null>(null);
  const [diffError, setDiffError] = useState<string | null>(null);
  const [draft, setDraft] = useState<{ file: string; line: number; body: string } | null>(null);

  // File browser state for the aside Files panel.
  const [fileTree, setFileTree] = useState<FileTreeState | null>(null);
  const [fileTreeLoading, setFileTreeLoading] = useState(false);
  const [fileTreeError, setFileTreeError] = useState<string | null>(null);
  const [selectedFile, setSelectedFile] = useState<string | null>(null);
  const [fileContent, setFileContent] = useState<FileContentLike | null>(null);
  const [fileContentLoading, setFileContentLoading] = useState(false);
  const [fileContentError, setFileContentError] = useState<string | null>(null);

  const [settingsOpen, setSettingsOpen] = useState(false);
  const [modelConfigOpen, setModelConfigOpen] = useState(false);
  const [settings, setSettings] = useState<SettingsView | null>(null);
  const [workdirInput, setWorkdirInput] = useState("");
  const [approvedInput, setApprovedInput] = useState("");
  const [saved, setSaved] = useState(false);
  const [modelsNotice, setModelsNotice] = useState<string | null>(null);

  const [projects, setProjects] = useState<Project[]>([]);
  const [projectId, setProjectId] = useState<string | null>(null);
  const [showNewProject, setShowNewProject] = useState(false);
  const [newProjectName, setNewProjectName] = useState("");
  const [newProjectPath, setNewProjectPath] = useState("");

  // Bento sizing for the projects and threads screens: unequal pseudo-random
  // spans per card, reflowed against the columns that fit the current width.
  const projectsGrid = useBentoColumns(240);
  const threadsGrid = useBentoColumns(280);

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
    setProjectId(res.project.id);
    setActiveId(null);
  };

  const deleteProject = async (id: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    setProjects(await rpc.call<Project[]>("project.delete", { id }));
    if (projectId === id) setProjectId(null);
  };

  const openProject = (id: string) => {
    setProjectId(id);
    setActiveId(null);
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
      setActiveId(null);
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
        // Start at the projects screen; a thread is opened explicitly.
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      }
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

  // Subscribe to the active thread and keep the local view fresh. Transient
  // "agent.output" frames append to a live buffer instead of refetching state.
  useEffect(() => {
    const rpc = rpcRef.current;
    if (!rpc || !activeId) return;
    setLive([]);
    rpc.call("thread.subscribe", { threadId: activeId }).catch(() => undefined);
    const off = rpc.onThreadEvent((raw) => {
      const ev = raw as {
        type?: string;
        payload?: { agentId?: string; kind?: string; text?: string };
      };
      if (ev?.type === "agent.output" && ev.payload) {
        const line = {
          agentId: String(ev.payload.agentId ?? ""),
          kind: String(ev.payload.kind ?? "text"),
          text: String(ev.payload.text ?? ""),
        };
        setLive((prev) => {
          // Harnesses emit text in chunks; grow the last block of the same kind
          // rather than stacking one line per chunk, so it reads as a stream.
          const last = prev[prev.length - 1];
          if (
            last &&
            last.agentId === line.agentId &&
            last.kind === line.kind &&
            (line.kind === "text" || line.kind === "reasoning")
          ) {
            const merged = { ...last, text: last.text + line.text };
            return [...prev.slice(0, -1), merged].slice(-400);
          }
          return [...prev.slice(-399), line];
        });
        return;
      }
      scheduleRefresh(activeId);
    });
    refresh(activeId).catch(() => undefined);
    return off;
  }, [activeId, refresh, scheduleRefresh]);

  // The diff backs both the center Review tab and the aside Diff panel.
  useEffect(() => {
    if (tab === "review" || asideActive === "diff") loadDiff(activeId);
  }, [tab, asideActive, activeId, loadDiff]);

  // The file tree loads when its panel is shown.
  useEffect(() => {
    if (asideActive === "files") loadFileTree(activeId);
  }, [asideActive, activeId, loadFileTree]);

  // Remember the open panels across restarts.
  useEffect(() => {
    try {
      window.localStorage.setItem("at.asideTabs", JSON.stringify(asideTabs));
    } catch {
      /* private mode: stay in memory */
    }
  }, [asideTabs]);

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
    if (!rpc || !detailId) return;
    const load = async () => {
      const l = await rpc.call<LogEntry[]>("agents.logs", { agentId: detailId }).catch(() => null);
      if (l) setLogs(l);
    };
    load();
    const timer = window.setInterval(load, 800);
    return () => window.clearInterval(timer);
  }, [detailId]);

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
  const running = useMemo(() => agents.filter((a) => a.status === "running"), [agents]);
  const selectedProject = projects.find((p) => p.id === projectId) ?? null;
  const projectThreads = useMemo(
    () => threads.filter((t) => t.projectId === projectId),
    [threads, projectId],
  );
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
    setActiveId(created.threadId);
  };

  const send = async () => {
    const rpc = rpcRef.current;
    const text = input.trim();
    if (!rpc || !activeId || !text) return;
    setInput("");
    // While a turn is running, a message steers the orchestrator's next pass
    // rather than starting a competing turn.
    const steer = running.length > 0;
    setBusy(true);
    try {
      await rpc.call(steer ? "thread.steer" : "thread.turn.start", { threadId: activeId, text });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
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
        <div className="mark">
          Agent<span style={{ color: "var(--accent)" }}>Tunnel</span>
        </div>
        <div>{status === "connecting" ? "Starting the daemon…" : "Daemon unavailable"}</div>
        {error && <div className="err-text">{error}</div>}
      </div>
    );
  }

  const titlebar = (
    <div className="titlebar">
      <div className="brand">
        Agent<span className="dot">Tunnel</span>
      </div>
      <div className={`conn ${status}`}>
        <span className="led" />
        <span>
          {enabledHarnesses.length} harness
          {enabledHarnesses.length === 1 ? "" : "es"} · {roles.length} roles
        </span>
      </div>
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

  // Screen 1 — the projects configured in this environment.
  if (!projectId) {
    return (
      <div className="screen">
        {titlebar}
        <div className="screen-body">
          <div className="screen-head">
            <h2>Projects</h2>
            <button onClick={openPicker}>New project</button>
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
          <div className="project-grid" ref={projectsGrid.ref} style={projectsGrid.gridStyle}>
            {projects.map((p) => (
              <div
                className="project-card"
                key={p.id}
                style={bentoTile(p.id, projectsGrid.columns)}
                onClick={() => openProject(p.id)}
              >
                <div className="pc-name">{p.name}</div>
                <div className="pc-path">{p.path || "no path set"}</div>
                <div className="pc-meta">
                  {p.threads} thread{p.threads === 1 ? "" : "s"}
                </div>
                <button
                  className="pc-del"
                  onClick={(e) => {
                    e.stopPropagation();
                    deleteProject(p.id).catch(() => undefined);
                  }}
                >
                  Delete
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
        <div className="screen-body">
          <div className="screen-head">
            <button className="back" onClick={() => setProjectId(null)}>
              ← Projects
            </button>
            <h2>{selectedProject?.name ?? "Threads"}</h2>
            <button className="primary" onClick={newThread}>
              New thread
            </button>
          </div>
          {selectedProject?.path && <div className="screen-sub">{selectedProject.path}</div>}
          {projectThreads.length === 0 && (
            <div className="empty">No threads yet. Start one for this project.</div>
          )}
          <div className="thread-cards" ref={threadsGrid.ref} style={threadsGrid.gridStyle}>
            {projectThreads.map((t) => (
              <div
                className="thread-card"
                key={t.id}
                style={bentoTile(t.id, threadsGrid.columns)}
                onClick={() => setActiveId(t.id)}
                onContextMenu={(e) => openThreadMenu(t, e)}
              >
                <div className="tc-head">
                  <div className="tc-title">{t.title}</div>
                  {fmtUpdated(t.updatedAt) && (
                    <div className="tc-updated">Updated {fmtUpdated(t.updatedAt)}</div>
                  )}
                </div>
                <div className="tc-meta">
                  {t.messages} messages · {t.agents} agents{t.autoMode ? " · auto" : ""}
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

      <div className="col sidebar">
        <div className="col-head">
          <button className="back-mini" onClick={() => setActiveId(null)}>
            ← {selectedProject?.name ?? "Threads"}
          </button>
          <button onClick={newThread}>New</button>
        </div>
        <div className="col-body">
          {projectThreads.length === 0 && <div className="empty">No threads yet</div>}
          {projectThreads.map((t) => (
            <div
              key={t.id}
              className={`thread-row ${t.id === activeId ? "active" : ""}`}
              onClick={() => setActiveId(t.id)}
              onContextMenu={(e) => openThreadMenu(t, e)}
            >
              <div className="t">{t.title}</div>
              <div className="m">
                {t.messages} msgs · {t.agents} agents{t.autoMode ? " · auto" : ""}
              </div>
              {fmtUpdated(t.updatedAt) && (
                <div className="u">Updated {fmtUpdated(t.updatedAt)}</div>
              )}
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
          <div className="tabs">
            <div className={`tab ${tab === "chat" ? "on" : ""}`} onClick={() => setTab("chat")}>
              Chat
            </div>
            <div className={`tab ${tab === "review" ? "on" : ""}`} onClick={() => setTab("review")}>
              Review
              {comments.length > 0 && <span className="pill">{comments.length}</span>}
            </div>
          </div>
          <div className="toggles">
            <div
              className={`toggle ${thread?.peerMailbox ? "on" : ""}`}
              onClick={() => thread && toggle("peerMailbox", !thread.peerMailbox)}
              title="Let subagents message each other (off by default)"
            >
              <span className="sw" /> Peer chat
            </div>
          </div>
        </div>

        {tab === "chat" ? (
          <>
            <div className="stream">
              {(!thread || (thread.messages?.length ?? 0) === 0) && (
                <div className="empty">
                  Describe the task. The orchestrator will plan it and dispatch your team.
                </div>
              )}
              {(thread?.messages ?? []).map((m) => (
                <div
                  key={m.id}
                  className={`msg ${m.author === "user" ? "user" : m.author === "steering" ? "steering" : ""}`}
                >
                  <div className="who">
                    <span className="role">
                      {m.author === "user"
                        ? "You"
                        : m.author === "steering"
                          ? "Steering"
                          : m.author === "system"
                            ? "System"
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
              {error && <div className="empty err-text">{error}</div>}
            </div>

            <div className="composer">
              <div className="box">
                <textarea
                  rows={1}
                  placeholder={
                    running.length > 0
                      ? "Steer the orchestrator — it lands on the next pass…"
                      : "Ask the team to build something…"
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
                        onClick={() => thread && toggle("autoMode", !thread.autoMode)}
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
        ) : (
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
          </div>
        )}
      </div>

      <div className="col aside">
        <div className="aside-tabs">
          {asideTabs.map((id) => (
            <div
              key={id}
              className={`tab ${asideActive === id ? "on" : ""}`}
              onClick={() => setAsideActive(id)}
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
                  title={`Close ${ASIDE_META[id].label}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    closePanel(id);
                  }}
                >
                  ×
                </span>
              )}
            </div>
          ))}
          <div
            className="tab tab-add"
            title="Add panel"
            onClick={(e) => {
              const r = e.currentTarget.getBoundingClientRect();
              setPanelMenu({ x: r.left, y: r.bottom + 4 });
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

        {asideActive === "agents" ? (
          <div className="section">
            <h3>
              Workflow{" "}
              <span style={{ color: "var(--faint)" }}>
                {settled}/{agents.length} settled
              </span>
            </h3>
            {lanes.length === 0 && (
              <div style={{ color: "var(--faint)", fontSize: 12 }}>No agents yet</div>
            )}
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
                    <div className="wf-row" key={a.id} onClick={() => setDetailId(a.id)}>
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
                <div style={{ color: "var(--faint)", fontSize: 12 }}>No turns yet.</div>
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
          </>
        ) : asideActive === "context" ? (
          <div className="section">
            <h3>Context</h3>
            {sharedEntries.length === 0 ? (
              <div style={{ color: "var(--faint)", fontSize: 12 }}>
                No shared context yet. Agents record findings here as they work.
              </div>
            ) : (
              sharedEntries.map(([key, value]) => (
                <div className="ctx-entry" key={key}>
                  <div className="ctx-key">{key}</div>
                  <div className="ctx-val">{value}</div>
                </div>
              ))
            )}
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
        )}

        <div className="section">
          <h3>
            Review comments <span style={{ color: "var(--faint)" }}>({comments.length})</span>
          </h3>
          {comments.length === 0 && (
            <div style={{ color: "var(--faint)", fontSize: 12 }}>
              None yet. Open Review and comment on a line.
            </div>
          )}
          {comments.map((c) => (
            <div className="agent" key={c.id} style={{ cursor: "default" }}>
              <div className="meta" style={{ marginTop: 0 }}>
                {c.file}:{c.line}
              </div>
              <div className="task" style={{ whiteSpace: "normal" }}>
                {c.body}
              </div>
            </div>
          ))}
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

        {globalUsage && (
          <div className="section">
            <h3>Usage — environment</h3>
            <div className="usage-grid">
              <div className="stat">
                <div className="k">Orchestrators</div>
                <div className="v">{fmtTokens(totalTokens(globalUsage.orchestrator))}</div>
              </div>
              <div className="stat">
                <div className="k">Subagents</div>
                <div className="v">{fmtTokens(totalTokens(globalUsage.subagents))}</div>
              </div>
            </div>
          </div>
        )}
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
                  onClick={() => setSettings({ ...settings, autoMode: !settings.autoMode })}
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
                      onClick={() => toggleHarness(h, !isHarnessEnabled(h))}
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
