import React, { useEffect, useMemo, useState } from 'react';
import { Box, Text, useApp, useInput, useStdout } from 'ink';
import TextInput from 'ink-text-input';
import { useStore } from '../core/state/store.js';
import { Pipeline, listModels, attachStoreEvents } from '../core/Pipeline.js';
import type { HintItem, OutputLine, OutputLineType } from '../types/index.js';
import { parseMarkdownInline } from '../core/utils/markdown.js';
import { ConfigPanel } from './components/ConfigPanel.js';

const SPINNER = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'];

const HINTS: HintItem[] = [
  { command: '/config', description: 'Open the config panel' },
  { command: '/clear', description: 'Clear the output buffer' },
  { command: '/help', description: 'Show available commands' },
  { command: '/exit', description: 'Quit AgentTunnel' },
];

const EXAMPLE_TASKS = [
  'Add a vitest test for the AnsiCleaner utility',
  'Refactor the Session class to support a --format json flag',
  'Audit the codebase for any remaining `any` types',
];

interface AppProps {
  workspace: string;
  autoApprove: boolean;
  defaultModel: string | undefined;
}

export function App({ workspace, autoApprove, defaultModel }: AppProps): React.ReactElement {
  const { exit } = useApp();
  const { stdout } = useStdout();
  const [pipeline, setPipeline] = useState<Pipeline | null>(null);
  const [spin, setSpin] = useState(0);

  const status = useStore((s) => s.status);
  const inputValue = useStore((s) => s.inputValue);
  const view = useStore((s) => s.view);
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

  const setInputValue = useStore((s) => s.setInputValue);
  const setStatus = useStore((s) => s.setStatus);
  const setView = useStore((s) => s.setView);
  const setError = useStore((s) => s.setError);
  const setScrollOffset = useStore((s) => s.setScrollOffset);
  const setModels = useStore((s) => s.setModels);
  const setShowHints = useStore((s) => s.setShowHints);
  const setHintIndex = useStore((s) => s.setHintIndex);
  const setHintItems = useStore((s) => s.setHintItems);
  const addOutput = useStore((s) => s.addOutput);
  const clearStore = useStore((s) => s.clear);
  const pushHistory = useStore((s) => s.pushHistory);
  const setHistoryIndex = useStore((s) => s.setHistoryIndex);
  const setRoleConfig = useStore((s) => s.setRoleConfig);

  const rows = stdout?.rows ?? 24;
  const cols = stdout?.columns ?? 80;

  useEffect(() => {
    if (defaultModel) {
      setRoleConfig('orchestrator', { harness: 'opencode', model: defaultModel });
    }
    listModels('opencode', workspace)
      .then((models) => {
        setModels(models);
        addOutput(`[init] workspace: ${workspace}`, 'system');
        addOutput(`[init] discovered ${models.length} model(s)`, 'system');
        addOutput('[init] type a task and press Enter, or / for commands', 'system');
      })
      .catch(() => {
        addOutput('[init] could not enumerate models (is opencode installed?)', 'error');
      });
  }, []);

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
    if (view === 'config') return;

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
        setHintIndex(Math.min(hintItems.length - 1, hintIndex + 1));
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

  const handleSubmit = (value: string): void => {
    const v = value.trim();
    if (v.length === 0) return;
    setInputValue('');
    setShowHints(false);

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
      for (const h of HINTS) addOutput(`  ${h.command.padEnd(10)} ${h.description}`, 'system');
      return;
    }
    if (v === '/config') {
      setView('config');
      return;
    }
    if (v.startsWith('/')) {
      addOutput(`[ui] unknown command: ${v}`, 'error');
      return;
    }
    startPipeline(v);
  };

  if (view === 'config') {
    return <ConfigPanel onClose={() => setView('main')} cols={cols} />;
  }

  const headerHeight = 1;
  const inputHeight = 1;
  const errorHeight = error ? 1 : 0;
  const hintsHeight = showHints ? Math.min(hintItems.length, 5) : 0;
  const visibleRows = Math.max(3, rows - headerHeight - inputHeight - errorHeight - hintsHeight);

  const visible = useMemo(() => visibleLines(output, visibleRows, scrollOffset), [
    output,
    visibleRows,
    scrollOffset,
  ]);

  return (
    <Box flexDirection="column" height={rows}>
      <Header
        cols={cols}
        status={status}
        phase={pipelinePhase}
        spin={SPINNER[spin] ?? ' '}
      />
      {contextEntries.length > 0 ? <ContextBar entries={contextEntries} /> : null}
      {error ? (
        <Box paddingX={1}>
          <Text color="red">! {error}</Text>
        </Box>
      ) : null}
      <Box flexDirection="column" flexGrow={1} overflow="hidden" paddingX={1}>
        {output.length === 0 ? <EmptyState cols={cols} /> : visible}
        {scrollOffset > 0 ? (
          <Text dimColor>  ↑ {scrollOffset} line(s) above — PgDn to scroll down</Text>
        ) : null}
      </Box>
      {showHints ? (
        <Box flexDirection="column" borderStyle="round" borderColor="gray" paddingX={1} marginX={1}>
          {hintItems.slice(0, 5).map((h, i) => (
            <Text key={h.command} inverse={i === hintIndex}>
              {' '}
              {h.command.padEnd(10)} {h.description}
            </Text>
          ))}
        </Box>
      ) : null}
      <Box borderStyle="round" borderColor={status === 'running' ? 'yellow' : 'gray'} paddingX={1} marginX={1}>
        <Text color={status === 'running' ? 'yellow' : 'green'}>{status === 'running' ? '▶' : '❯'} </Text>
        <TextInput
          value={inputValue}
          onChange={setInputValue}
          onSubmit={handleSubmit}
          placeholder={status === 'running' ? 'busy…' : 'type a task or / for commands'}
        />
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
  const color = status === 'running' ? 'yellow' : status === 'failed' ? 'red' : status === 'completed' ? 'green' : 'gray';
  const model = useStore((s) => s.config.orchestrator.model) || 'default';
  const progress = phase.progress ? ` ${phase.progress.current}/${phase.progress.total}` : '';
  const right = `${phase.label}${progress} • model=${model}`;
  const left = ' AgentTunnel ';
  const pad = Math.max(0, cols - left.length - right.length);
  return (
    <Box>
      <Text backgroundColor="gray" color="white" bold>
        {left}
      </Text>
      <Text backgroundColor="gray" color="white">
        {' '.repeat(pad)}
      </Text>
      <Text backgroundColor="gray" color="white">
        {' '}
        {status === 'running' ? `${spin} ` : ''}
        {right}
      </Text>
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

function EmptyState({ cols }: { cols: number }): React.ReactElement {
  return (
    <Box flexDirection="column" paddingY={1}>
      <Text bold color="cyan">
        AgentTunnel is ready
      </Text>
      <Text dimColor>
        A multi-agent orchestrator for opencode. Describe a task below; the orchestrator will plan and
        dispatch executor sub-agents.
      </Text>
      <Box flexDirection="column" marginTop={1}>
        <Text dimColor>Example tasks:</Text>
        {EXAMPLE_TASKS.map((t) => (
          <Text key={t}> • {t}</Text>
        ))}
      </Box>
      <Box marginTop={1} flexDirection="column">
        <Text dimColor>Slash commands (type / to see):</Text>
        {HINTS.map((h) => (
          <Text key={h.command} dimColor>
            {' '}
            {h.command.padEnd(10)} {h.description}
          </Text>
        ))}
      </Box>
    </Box>
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
        <OutputLineView key={line.id} line={line} />
      ))}
    </>
  );
}

function OutputLineView({ line }: { line: OutputLine }): React.ReactElement {
  if (line.codeBlock) {
    return (
      <Text backgroundColor="blackBright" color="white">
        {'  '}
        {line.text}
      </Text>
    );
  }
  const color = colorForType(line.type);
  const prefix = prefixForType(line.type);
  const segments = parseMarkdownInline(line.text);
  return (
    <Text color={color}>
      {prefix}
      {segments.map((s, i) => (
        <Text
          key={i}
          bold={s.bold}
          dimColor={s.dim}
          color={s.color ?? color}
        >
          {s.text}
        </Text>
      ))}
    </Text>
  );
}

function colorForType(t: OutputLineType): string | undefined {
  switch (t) {
    case 'user':
      return 'green';
    case 'assistant':
      return 'cyan';
    case 'system':
      return 'gray';
    case 'error':
      return 'red';
    case 'tool':
      return 'magenta';
    case 'tool_output':
      return 'white';
  }
}

function prefixForType(t: OutputLineType): string {
  switch (t) {
    case 'user':
      return ' › ';
    case 'assistant':
      return ' ● ';
    case 'system':
      return ' · ';
    case 'error':
      return ' ! ';
    case 'tool':
      return ' ⚙ ';
    case 'tool_output':
      return '   ';
  }
}
