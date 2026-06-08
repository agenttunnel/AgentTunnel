import { create } from 'zustand';
import type {
  AppConfig,
  HintItem,
  OutputLine,
  OutputLineType,
  PipelinePhase,
  RoleConfig,
  RoleName,
  SessionStatus,
  SubAgentInfo,
  ViewMode,
} from '../../types/index.js';
import { defaultConfig } from '../../types/index.js';

let nextLineId = 1;
let nextAgentId = 1;
let inCodeBlock = false;
let inCodeBlockBuf = '';

interface EditingField {
  section: RoleName;
  field: 'harness' | 'model';
  value: string;
}

export interface StoreState {
  output: OutputLine[];
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
  history: string[];
  historyIndex: number;
  showHints: boolean;
  hintIndex: number;
  hintItems: HintItem[];

  addOutput: (text: string, type: OutputLineType) => void;
  addOutputRaw: (line: OutputLine) => void;
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
  pushHistory: (entry: string) => void;
  setHistoryIndex: (n: number) => void;
  clear: () => void;

  newAgentId: () => string;
}

export const useStore = create<StoreState>((set) => ({
  output: [],
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
  history: [],
  historyIndex: -1,
  showHints: false,
  hintIndex: 0,
  hintItems: [],

  addOutput: (text, type) => {
    const lines = text.split('\n');
    const produced: OutputLine[] = [];
    for (const raw of lines) {
      if (inCodeBlockBuf.length > 0) {
        inCodeBlockBuf += '\n' + raw;
        if (/```/.test(raw)) {
          produced.push(makeLine(inCodeBlockBuf, type, true));
          inCodeBlockBuf = '';
          inCodeBlock = false;
        }
        continue;
      }

      if (/^```/.test(raw)) {
        inCodeBlockBuf = raw;
        if (/(^```\s*$)|(```.*```)/.test(raw) && raw.trim().endsWith('```') && raw.trim() !== '```') {
          produced.push(makeLine(inCodeBlockBuf, type, true));
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

  setStatus: (status) => set({ status }),
  setInputValue: (inputValue) => set({ inputValue, showHints: inputValue.startsWith('/') }),
  setScrollOffset: (scrollOffset) => set({ scrollOffset: Math.max(0, scrollOffset) }),
  setError: (error) => set({ error }),
  setView: (view) => set({ view, editingField: null }),
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
  pushHistory: (entry) =>
    set((s) => {
      if (entry.trim().length === 0) return s;
      const filtered = s.history.filter((h) => h !== entry);
      return { history: [...filtered, entry], historyIndex: -1 };
    }),
  setHistoryIndex: (historyIndex) => set({ historyIndex }),
  clear: () =>
    set({
      output: [],
      error: null,
      scrollOffset: 0,
      subAgents: [],
      pipelinePhase: { type: 'idle', label: 'idle' },
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
  useStore.setState({
    output: [],
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
    history: [],
    historyIndex: -1,
    showHints: false,
    hintIndex: 0,
    hintItems: [],
  });
}
