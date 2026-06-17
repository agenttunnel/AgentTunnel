import { create } from 'zustand';
import type {
  AppConfig,
  HintItem,
  OutputLine,
  OutputLineType,
  PipelineInfo,
  PipelinePhase,
  RoleConfig,
  RoleName,
  SessionStatus,
  SubAgentInfo,
  ViewMode,
} from '../../types/index.js';
import { defaultConfig } from '../../types/index.js';
import type { ContextEntry } from '../SharedContext.js';

let nextLineId = 1;
let nextAgentId = 1;
let inCodeBlock = false;
let inCodeBlockBuf = '';
let inCodeBlockType: OutputLineType = 'system';

interface EditingField {
  section: RoleName;
  field: 'harness' | 'model';
  value: string;
}

export interface StoreState {
  output: OutputLine[];
  /** Per-sub-agent log buffers, keyed by agent id. Hidden from the main log. */
  subAgentLogs: Record<string, OutputLine[]>;
  status: SessionStatus;
  inputValue: string;
  scrollOffset: number;
  error: string | null;
  view: ViewMode;
  config: AppConfig;
  editingField: EditingField | null;
  pipelinePhase: PipelinePhase;
  models: string[];
  subAgents: SubAgentInfo[];
  pipelines: PipelineInfo[];
  contextEntries: ContextEntry[];
  history: string[];
  historyIndex: number;
  showHints: boolean;
  hintIndex: number;
  hintItems: HintItem[];

  /** Currently selected sub-agent in the /sub-agents list. */
  selectedSubAgentId: string | null;
  /** Which sub-agent's log is being viewed in 'sub-agent-detail'. */
  viewAgentId: string | null;
  /** When true, sub-agent output also streams into the main output buffer. */
  showSubAgentLogsInMain: boolean;

  addOutput: (text: string, type: OutputLineType) => void;
  addOutputRaw: (line: OutputLine) => void;
  addSubAgentLog: (agentId: string, text: string, type?: OutputLineType) => void;
  setStatus: (status: SessionStatus) => void;
  setInputValue: (v: string) => void;
  setScrollOffset: (n: number) => void;
  setError: (e: string | null) => void;
  setView: (v: ViewMode) => void;
  setConfig: (cfg: AppConfig) => void;
  setRoleConfig: (role: RoleName, cfg: RoleConfig) => void;
  setEditingField: (f: EditingField | null) => void;
  setShowHints: (b: boolean) => void;
  setHintIndex: (n: number) => void;
  setHintItems: (items: HintItem[]) => void;
  setModels: (m: string[]) => void;
  setPipelinePhase: (p: PipelinePhase) => void;
  setSubAgents: (agents: SubAgentInfo[]) => void;
  setPipelines: (pipelines: PipelineInfo[]) => void;
  setContextEntries: (entries: ContextEntry[]) => void;
  pushHistory: (entry: string) => void;
  setHistoryIndex: (n: number) => void;
  setSelectedSubAgentId: (id: string | null) => void;
  setViewAgentId: (id: string | null) => void;
  setShowSubAgentLogsInMain: (b: boolean) => void;
  clear: () => void;

  newAgentId: () => string;
}

const SUB_AGENT_LOG_CAP = 200;

export const useStore = create<StoreState>((set) => ({
  output: [],
  subAgentLogs: {},
  status: 'idle',
  inputValue: '',
  scrollOffset: 0,
  error: null,
  view: 'main',
  config: defaultConfig(),
  editingField: null,
  pipelinePhase: { type: 'idle', label: 'idle' },
  models: [],
  subAgents: [],
  pipelines: [],
  contextEntries: [],
  history: [],
  historyIndex: -1,
  showHints: false,
  hintIndex: 0,
  hintItems: [],
  selectedSubAgentId: null,
  viewAgentId: null,
  showSubAgentLogsInMain: false,

  addOutput: (text, type) => {
    const lines = text.split('\n');
    const produced: OutputLine[] = [];

    if (inCodeBlockBuf.length > 0 && type !== inCodeBlockType) {
      produced.push(makeLine(inCodeBlockBuf, inCodeBlockType, true));
      inCodeBlockBuf = '';
      inCodeBlock = false;
    }

    for (const raw of lines) {
      if (inCodeBlockBuf.length > 0) {
        inCodeBlockBuf += '\n' + raw;
        if (/```/.test(raw)) {
          produced.push(makeLine(inCodeBlockBuf, inCodeBlockType, true));
          inCodeBlockBuf = '';
          inCodeBlock = false;
        }
        continue;
      }

      if (/^```/.test(raw)) {
        inCodeBlockBuf = raw;
        inCodeBlockType = type;
        if (/(^```\s*$)|(```.*```)/.test(raw) && raw.trim().endsWith('```') && raw.trim() !== '```') {
          produced.push(makeLine(inCodeBlockBuf, inCodeBlockType, true));
          inCodeBlockBuf = '';
          inCodeBlock = false;
        } else {
          inCodeBlock = true;
        }
        continue;
      }

      produced.push(makeLine(raw, type, false));
    }

    if (produced.length === 0) return;
    set((s) => ({ output: [...s.output, ...produced], scrollOffset: 0 }));
  },

  addOutputRaw: (line) => {
    set((s) => ({ output: [...s.output, line], scrollOffset: 0 }));
  },

  addSubAgentLog: (agentId, text, type = 'tool_output') => {
    const line = makeLine(text, type, false);
    set((s) => {
      const existing = s.subAgentLogs[agentId] ?? [];
      const next = existing.length < SUB_AGENT_LOG_CAP
        ? [...existing, line]
        : [...existing.slice(existing.length - (SUB_AGENT_LOG_CAP - 1)), line];
      const logs = { ...s.subAgentLogs, [agentId]: next };
      // Optionally also push into the main output buffer
      const output = s.showSubAgentLogsInMain ? [...s.output, line] : s.output;
      return { subAgentLogs: logs, output, scrollOffset: 0 };
    });
  },

  setStatus: (status) => set({ status }),
  setInputValue: (inputValue) => set({ inputValue, showHints: inputValue.startsWith('/') }),
  setScrollOffset: (scrollOffset) => set({ scrollOffset: Math.max(0, scrollOffset) }),
  setError: (error) => set({ error }),
  setView: (view) =>
    set((s) => ({
      view,
      editingField: null,
      viewAgentId: view === 'sub-agent-detail' ? s.viewAgentId : null,
      selectedSubAgentId: view === 'sub-agents' ? s.selectedSubAgentId ?? s.subAgents[0]?.id ?? null : null,
    })),
  setConfig: (config) => set({ config }),
  setRoleConfig: (role, cfg) =>
    set((s) => ({ config: { ...s.config, [role]: cfg } })),
  setEditingField: (editingField) => set({ editingField }),
  setShowHints: (showHints) => set({ showHints, hintIndex: 0 }),
  setHintIndex: (hintIndex) => set({ hintIndex: Math.max(0, hintIndex) }),
  setHintItems: (hintItems) => set({ hintItems }),
  setModels: (models) => set({ models }),
  setPipelinePhase: (pipelinePhase) => set({ pipelinePhase, scrollOffset: 0 }),
  setSubAgents: (subAgents) => set({ subAgents }),
  setPipelines: (pipelines) => set({ pipelines }),
  setContextEntries: (contextEntries) => set({ contextEntries }),
  pushHistory: (entry) =>
    set((s) => {
      if (entry.trim().length === 0) return s;
      const filtered = s.history.filter((h) => h !== entry);
      return { history: [...filtered, entry], historyIndex: -1 };
    }),
  setHistoryIndex: (historyIndex) => set({ historyIndex }),
  setSelectedSubAgentId: (selectedSubAgentId) => set({ selectedSubAgentId }),
  setViewAgentId: (viewAgentId) => set({ viewAgentId }),
  setShowSubAgentLogsInMain: (b) => set({ showSubAgentLogsInMain: b }),
  clear: () =>
    set({
      output: [],
      subAgentLogs: {},
      error: null,
      scrollOffset: 0,
      subAgents: [],
      pipelines: [],
      contextEntries: [],
      pipelinePhase: { type: 'idle', label: 'idle' },
      selectedSubAgentId: null,
      viewAgentId: null,
    }),

  newAgentId: () => `agent-${nextAgentId++}-${Date.now().toString(36)}`,
}));

function makeLine(text: string, type: OutputLineType, codeBlock: boolean): OutputLine {
  return { id: nextLineId++, text, timestamp: Date.now(), type, codeBlock };
}

export function _resetStoreForTests(): void {
  nextLineId = 1;
  nextAgentId = 1;
  inCodeBlock = false;
  inCodeBlockBuf = '';
  inCodeBlockType = 'system';
  useStore.setState({
    output: [],
    subAgentLogs: {},
    status: 'idle',
    inputValue: '',
    scrollOffset: 0,
    error: null,
    view: 'main',
    config: defaultConfig(),
    editingField: null,
    pipelinePhase: { type: 'idle', label: 'idle' },
    models: [],
    subAgents: [],
    pipelines: [],
    contextEntries: [],
    history: [],
    historyIndex: -1,
    showHints: false,
    hintIndex: 0,
    hintItems: [],
    selectedSubAgentId: null,
    viewAgentId: null,
    showSubAgentLogsInMain: false,
  });
}
