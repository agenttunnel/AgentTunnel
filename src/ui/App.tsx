import React, { useEffect, useMemo, useState } from 'react';
import { Box, Text, useApp, useInput, useStdout } from 'ink';
import { useStore } from '../core/state/store.js';
import { Pipeline, listModels, attachStoreEvents } from '../core/Pipeline.js';
import type { HintItem, OutputLine } from '../types/index.js';
import { ConfigPanel } from './components/ConfigPanel.js';
import { SubAgentsView, SubAgentDetailView } from './components/SubAgentsView.jsx';
import { PipelineProvider } from './PipelineContext.jsx';
import { TextInputBox } from './TextInputBox.jsx';
import { MarkdownLine } from './MarkdownLine.jsx';
import {
  colors,
  glyphs,
  borderForStatus,
  colorForStatus,
  renderHeaderLine,
} from './theme.js';

const SPINNER = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'] as const;

const HINTS: HintItem[] = [
  { command: '/config', description: 'Open the config panel' },
  { command: '/sub-agents', description: 'List running sub-agents (x kill, l logs)' },
  { command: '/log', description: 'Toggle showing sub-agent output in main log' },
  { command: '/clear', description: 'Clear the output buffer' },
  { command: '/help', description: 'Show available commands' },
  { command: '/exit', description: 'Quit AgentTunnel' },
];

interface AppProps {
  workspace: string;
  autoApprove: boolean;
  defaultModel: string | undefined;
}

export interface InitStatus {
  workspace: string;
  modelCount: number | null;
  error: string | null;
}

export function App({ workspace, autoApprove, defaultModel }: AppProps): React.ReactElement {
  const [pipeline, setPipeline] = useState<Pipeline | null>(null);
  const [initStatus, setInitStatus] = useState<InitStatus>({
    workspace,
    modelCount: null,
    error: null,
  });

  const view = useStore((s) => s.view);
  const setView = useStore((s) => s.setView);

  const setRoleConfig = useStore((s) => s.setRoleConfig);
  const setModels = useStore((s) => s.setModels);

  useEffect(() => {
    if (defaultModel) {
      setRoleConfig('orchestrator', { harness: 'opencode', model: defaultModel });
    }
    let cancelled = false;
    listModels('opencode', workspace)
      .then((models) => {
        if (cancelled) return;
        setModels(models);
        setInitStatus({ workspace, modelCount: models.length, error: null });
      })
      .catch((err: Error) => {
        if (cancelled) return;
        setInitStatus({ workspace, modelCount: null, error: err.message });
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <PipelineProvider value={{ pipeline }}>
      {view === 'config' ? (
        <ConfigPanel onClose={() => setView('main')} />
      ) : view === 'sub-agents' ? (
        <SubAgentsView onBack={() => setView('main')} />
      ) : view === 'sub-agent-detail' ? (
        <SubAgentDetailView onBack={() => setView('sub-agents')} />
      ) : (
        <MainView
          workspace={workspace}
          autoApprove={autoApprove}
          pipeline={pipeline}
          setPipeline={setPipeline}
          initStatus={initStatus}
        />
      )}
    </PipelineProvider>
  );
}

interface MainViewProps {
  workspace: string;
  autoApprove: boolean;
  pipeline: Pipeline | null;
  setPipeline: (p: Pipeline | null) => void;
  initStatus: InitStatus;
}

function MainView({ workspace, autoApprove, pipeline, setPipeline, initStatus }: MainViewProps): React.ReactElement {
  const { exit } = useApp();
  const { stdout } = useStdout();
  const [spin, setSpin] = useState(0);

  const status = useStore((s) => s.status);
  const inputValue = useStore((s) => s.inputValue);
  const output = useStore((s) => s.output);
  const error = useStore((s) => s.error);
  const scrollOffset = useStore((s) => s.scrollOffset);
  const pipelinePhase = useStore((s) => s.pipelinePhase);
  const showHints = useStore((s) => s.showHints);
  const hintIndex = useStore((s) => s.hintIndex);
  const hintItems = useStore((s) => s.hintItems);
  const history = useStore((s) => s.history);
  const historyIndex = useStore((s) => s.historyIndex);
  const contextEntries = useStore((s) => s.contextEntries);
  const showSubAgentLogsInMain = useStore((s) => s.showSubAgentLogsInMain);

  const setInputValue = useStore((s) => s.setInputValue);
  const setStatus = useStore((s) => s.setStatus);
  const setView = useStore((s) => s.setView);
  const setError = useStore((s) => s.setError);
  const setScrollOffset = useStore((s) => s.setScrollOffset);
  const setShowHints = useStore((s) => s.setShowHints);
  const setHintIndex = useStore((s) => s.setHintIndex);
  const setHintItems = useStore((s) => s.setHintItems);
  const setShowSubAgentLogsInMain = useStore((s) => s.setShowSubAgentLogsInMain);
  const addOutput = useStore((s) => s.addOutput);
  const clearStore = useStore((s) => s.clear);
  const pushHistory = useStore((s) => s.pushHistory);
  const setHistoryIndex = useStore((s) => s.setHistoryIndex);

  const rows = stdout?.rows ?? 24;
  const cols = stdout?.columns ?? 80;

  useEffect(() => {
    if (status !== 'running') return;
    const t = setInterval(() => setSpin((i) => (i + 1) % SPINNER.length), 80);
    return () => clearInterval(t);
  }, [status]);

  useEffect(() => {
    if (!inputValue.startsWith('/')) {
      if (showHints) setShowHints(false);
      return;
    }
    const q = inputValue.slice(1).toLowerCase();
    const matches = HINTS.filter((h) => h.command.slice(1).toLowerCase().startsWith(q));
    setHintItems(matches);
    if (matches.length > 0) {
      if (!showHints) setShowHints(true);
      if (hintIndex >= matches.length) setHintIndex(0);
    } else {
      setShowHints(false);
    }
  }, [inputValue, showHints, hintIndex, setShowHints, setHintItems, setHintIndex]);

  useInput((input, key) => {
    if (key.ctrl && input === 'c') {
      if (status === 'running' && pipeline) {
        pipeline.cancel();
        setStatus('idle');
        addOutput('[ui] cancelled by user', 'system');
      } else {
        pipeline?.cancel();
        exit();
      }
      return;
    }

    if (showHints) {
      if (key.upArrow) {
        setHintIndex(Math.max(0, hintIndex - 1));
        return;
      }
      if (key.downArrow) {
        setHintIndex(Math.min(Math.max(0, hintItems.length - 1), hintIndex + 1));
        return;
      }
      if (key.tab) {
        const h = hintItems[hintIndex];
        if (h) {
          setInputValue(h.command + ' ');
          setShowHints(false);
        }
        return;
      }
    }

    if (status !== 'running' && !showHints) {
      if (key.upArrow) {
        if (history.length === 0) return;
        const next = historyIndex < 0 ? history.length - 1 : Math.max(0, historyIndex - 1);
        setHistoryIndex(next);
        const v = history[next];
        if (v !== undefined) setInputValue(v);
        return;
      }
      if (key.downArrow) {
        if (historyIndex < 0) return;
        const next = historyIndex + 1;
        if (next >= history.length) {
          setHistoryIndex(-1);
          setInputValue('');
        } else {
          setHistoryIndex(next);
          const v = history[next];
          if (v !== undefined) setInputValue(v);
        }
        return;
      }
    }

    if (key.pageUp) {
      setScrollOffset(scrollOffset + Math.floor(rows / 2));
      return;
    }
    if (key.pageDown) {
      setScrollOffset(Math.max(0, scrollOffset - Math.floor(rows / 2)));
      return;
    }
  });

  const startPipeline = (task: string): void => {
    if (!task.trim()) return;
    if (status === 'running') {
      addOutput('[ui] pipeline already running, ignoring new task', 'system');
      return;
    }
    pushHistory(task);
    setError(null);
    setStatus('running');
    addOutput(`> ${task}`, 'user');

    const p = new Pipeline({
      cwd: workspace,
      config: useStore.getState().config,
      newAgentId: useStore.getState().newAgentId,
      dangerouslySkipPermissions: autoApprove,
    });
    attachStoreEvents(p);
    setPipeline(p);
    void p.run(task);
  };

  const executeCommand = (raw: string): void => {
    const v = raw.trim();
    if (v.length === 0) return;

    if (v === '/exit') {
      pipeline?.cancel();
      exit();
      return;
    }
    if (v === '/clear') {
      clearStore();
      return;
    }
    if (v === '/help') {
      for (const h of HINTS) addOutput(`  ${h.command.padEnd(14)} ${h.description}`, 'system');
      return;
    }
    if (v === '/config') {
      setView('config');
      return;
    }
    if (v === '/sub-agents') {
      setView('sub-agents');
      return;
    }
    if (v === '/log') {
      const next = !showSubAgentLogsInMain;
      setShowSubAgentLogsInMain(next);
      addOutput(`[ui] sub-agent logs in main: ${next ? 'on' : 'off'}`, 'system');
      return;
    }
    if (v.startsWith('/')) {
      addOutput(`[ui] unknown command: ${v}`, 'error');
      return;
    }
    startPipeline(v);
  };

  const handleSubmit = (value: string): void => {
    // If hints are showing and one is highlighted, treat Enter as "select that hint".
    if (showHints) {
      const h = hintItems[hintIndex];
      if (h) {
        setInputValue('');
        setShowHints(false);
        executeCommand(h.command);
        return;
      }
    }
    setInputValue('');
    setShowHints(false);
    executeCommand(value);
  };

  // Layout dimensions are computed here, BEFORE the JSX, so the useMemo below
  // can use them without re-creating a new object on every render.
  const headerHeight = 2; // logo line + dim grid line
  const footerHeight = 1; // scanline phase strip
  const inputHeight = 1;
  const contextBarHeight = contextEntries.length > 0 ? 1 : 0;
  const errorHeight = error ? 1 : 0;
  const hintsHeight = showHints ? Math.min(hintItems.length, 5) : 0;
  const visibleRows = Math.max(
    3,
    rows - headerHeight - inputHeight - errorHeight - hintsHeight - contextBarHeight - footerHeight,
  );

  const visible = useMemo(
    () => visibleLines(output, visibleRows, scrollOffset),
    [output, visibleRows, scrollOffset],
  );

  return (
    <Box flexDirection="column" height={rows}>
      <Box flexShrink={0}>
        <Header
          cols={cols}
          status={status}
          phase={pipelinePhase}
          spin={SPINNER[spin] ?? ' '}
        />
      </Box>
      <Box
        flexDirection="column"
        flexGrow={1}
        flexShrink={1}
        borderStyle="single"
        borderColor={colors.dim}
        paddingX={1}
        marginX={0}
        overflow="hidden"
      >
        {contextEntries.length > 0 ? <ContextBar entries={contextEntries} /> : null}
        {error ? (
          <Box>
            <Text color={colors.danger} bold>
              {`${glyphs.blockFull} ERROR `}
            </Text>
            <Text color={colors.danger}>{error}</Text>
          </Box>
        ) : null}
        {showSubAgentLogsInMain ? (
          <Text color={colors.accent}>
            {`${glyphs.chevron} sub-agent output is mirrored into the main log`}
          </Text>
        ) : null}
        <Box flexDirection="column" flexGrow={1} flexShrink={1} overflow="hidden">
          {output.length === 0 ? <EmptyState initStatus={initStatus} /> : visible}
        </Box>
        {scrollOffset > 0 ? (
          <Text color={colors.dim}>
            {`  ${glyphs.chevron} ${scrollOffset} line(s) above — PgDn to scroll down`}
          </Text>
        ) : null}
      </Box>
      {showHints ? (
        <Box
          flexDirection="column"
          flexShrink={0}
          borderStyle="round"
          borderColor={colors.primary}
          paddingX={1}
          marginX={1}
        >
          {hintItems.slice(0, 5).map((h, i) => (
            <Text
              key={h.command}
              backgroundColor={i === hintIndex ? colors.primary : undefined}
              color={i === hintIndex ? colors.onPrimary : colors.grid}
            >
              {' '}
              {h.command.padEnd(14)} {h.description}
            </Text>
          ))}
        </Box>
      ) : null}
      <Box flexShrink={0}>
        <Footer cols={cols} status={status} phase={pipelinePhase.label} />
      </Box>
      <Box flexShrink={0}>
        <Box
          borderStyle="round"
          borderColor={borderForStatus(status)}
          paddingX={1}
          marginX={1}
        >
          <Text color={colorForStatus(status)}>
            {status === 'running' ? `${SPINNER[spin] ?? ' '} ` : `${glyphs.chevron} `}
          </Text>
          <TextInputBox
            value={inputValue}
            onChange={setInputValue}
            onSubmit={handleSubmit}
            placeholder={status === 'running' ? 'busy…' : 'type a task or / for commands'}
          />
        </Box>
      </Box>
    </Box>
  );
}

function Header({
  cols,
  status,
  phase,
  spin,
}: {
  cols: number;
  status: 'idle' | 'running' | 'completed' | 'failed';
  phase: { type: string; label: string; progress?: { current: number; total: number } };
  spin: string;
}): React.ReactElement {
  const model = useStore((s) => s.config.orchestrator.model) || 'default';
  const progress = phase.progress ? ` ${phase.progress.current}/${phase.progress.total}` : '';
  const statusLabel = `${phase.label}${progress}`;
  const right = `${statusLabel} • model=${model || 'default'}`;
  const running = status === 'running';

  const spinIndex = Math.abs(typeof spin === 'number' ? spin : 0) % SPINNER.length;
  const spinFrame = SPINNER[spinIndex] ?? ' ';

  return (
    <Box flexDirection="column">
      <Box>
        <Text color={colors.primary} bold>
          {running ? `${spinFrame} ` : `${glyphs.blockFull} `}
        </Text>
        <Text color={colors.primary} bold>
          {`▰▰▰ AGENTTUNNEL ▰▰▰`}
        </Text>
        <Text color={colors.dim}>
          {` ${glyphs.hLine.repeat(Math.max(0, cols - right.length - 28))} `}
        </Text>
        <Text color={colorForStatus(status)}>{right}</Text>
      </Box>
      <Text color={colors.dim}>{glyphs.hLine.repeat(Math.max(0, cols))}</Text>
    </Box>
  );
}

function Footer({ cols, status, phase }: { cols: number; status: 'idle' | 'running' | 'completed' | 'failed'; phase: string }): React.ReactElement {
  const statusColor = colorForStatus(status);
  return (
    <Box paddingX={1}>
      <Text color={colors.dim}>{glyphs.scanStart}</Text>
      <Text color={statusColor}>{glyphs.scanMid.repeat(Math.max(2, cols - phase.length - 8))}</Text>
      <Text color={colors.dim}>{glyphs.scanEnd}</Text>
      <Text color={statusColor}>{` ${phase} `}</Text>
    </Box>
  );
}

function ContextBar({ entries }: { entries: { key: string; source: string; value: string }[] }): React.ReactElement {
  const summary = entries
    .slice(0, 6)
    .map((e) => {
      const firstLine = e.value.trim().split('\n')[0] ?? '';
      const preview = firstLine.length > 40 ? firstLine.slice(0, 39) + '…' : firstLine;
      const sourceMark = e.source === 'sub-agent' ? '↳' : e.source === 'orchestrator' ? '◆' : '·';
      return `${sourceMark} ${e.key}: ${preview}`;
    })
    .join('  ');
  const more = entries.length > 6 ? `  +${entries.length - 6} more` : '';
  return (
    <Box paddingX={1}>
      <Text dimColor>
        📓 {summary}
        {more}
      </Text>
    </Box>
  );
}

function EmptyState({ initStatus }: { initStatus: InitStatus }): React.ReactElement {
  return (
    <Box flexDirection="column" flexGrow={1} paddingY={1}>
      <Text bold color={colors.primary}>
        {`${glyphs.blockFull} AGENTTUNNEL ONLINE`}
      </Text>
      <Text color={colors.grid}>
        {`${glyphs.chevron} type a task below and press Enter — the orchestrator will plan, dispatch sub-agents, and report back`}
      </Text>
      <Text color={colors.grid}>
        {`${glyphs.chevron} type / to discover commands (Tab to autocomplete, Enter to pick)`}
      </Text>
      <Box marginTop={1} flexDirection="column">
        <Text color={colors.primary}>{`${glyphs.chevron} Quick start`}</Text>
        {HINTS.slice(0, 4).map((h) => (
          <Text key={h.command} color={colors.grid}>
            {`  ${h.command.padEnd(14)} ${h.description}`}
          </Text>
        ))}
      </Box>
      <Box marginTop={1} flexDirection="column">
        <InitStatusLine status={initStatus} />
      </Box>
    </Box>
  );
}

function InitStatusLine({ status }: { status: InitStatus }): React.ReactElement {
  if (status.error) {
    return (
      <Text color={colors.danger}>
        {`  ${glyphs.blockHalf} opencode unavailable — ${status.error}`}
      </Text>
    );
  }
  if (status.modelCount === null) {
    return (
      <Text color={colors.dim}>
        {`  ${glyphs.blockHalf} loading models…`}
      </Text>
    );
  }
  return (
    <Text color={colors.dim}>
      {`  ${glyphs.blockHalf} workspace ${status.workspace}  ·  ${status.modelCount} model(s) available`}
    </Text>
  );
}

function visibleLines(output: OutputLine[], rows: number, offset: number): React.ReactElement {
  if (output.length === 0) return <Text dimColor>(empty)</Text>;
  const start = Math.max(0, output.length - rows - offset);
  const end = Math.min(output.length, start + rows);
  const slice = output.slice(start, end);
  return (
    <>
      {slice.map((line) => (
        <MarkdownLine key={line.id} line={line} />
      ))}
    </>
  );
}
