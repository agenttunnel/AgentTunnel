import React, { useEffect, useMemo, useState } from 'react';
import { Box, Text, useInput, useStdout } from 'ink';
import { useStore } from '../../core/state/store.js';
import { usePipeline } from '../PipelineContext.jsx';
import type { OutputLine, SubAgentInfo } from '../../types/index.js';
import { parseMarkdownInline } from '../../core/utils/markdown.js';

interface SubAgentsViewProps {
  onBack: () => void;
}

export function SubAgentsView({ onBack }: SubAgentsViewProps): React.ReactElement {
  const subAgents = useStore((s) => s.subAgents);
  const selectedSubAgentId = useStore((s) => s.selectedSubAgentId);
  const setSelectedSubAgentId = useStore((s) => s.setSelectedSubAgentId);
  const setViewAgentId = useStore((s) => s.setViewAgentId);
  const setView = useStore((s) => s.setView);
  const showSubAgentLogsInMain = useStore((s) => s.showSubAgentLogsInMain);
  const setShowSubAgentLogsInMain = useStore((s) => s.setShowSubAgentLogsInMain);
  const pipeline = usePipeline();
  const { stdout } = useStdout();
  const cols = stdout?.columns ?? 80;
  const rows = stdout?.rows ?? 24;

  // Initialize / keep selection valid.
  useEffect(() => {
    if (subAgents.length === 0) {
      if (selectedSubAgentId !== null) setSelectedSubAgentId(null);
      return;
    }
    if (!selectedSubAgentId || !subAgents.find((a) => a.id === selectedSubAgentId)) {
      setSelectedSubAgentId(subAgents[0]?.id ?? null);
    }
  }, [subAgents, selectedSubAgentId, setSelectedSubAgentId]);

  useInput((input, key) => {
    if (key.escape || input === 'q') {
      onBack();
      return;
    }
    if (key.upArrow) {
      if (subAgents.length === 0) return;
      const idx = selectedSubAgentId
        ? subAgents.findIndex((a) => a.id === selectedSubAgentId)
        : 0;
      const next = idx <= 0 ? subAgents.length - 1 : idx - 1;
      const a = subAgents[next];
      if (a) setSelectedSubAgentId(a.id);
      return;
    }
    if (key.downArrow) {
      if (subAgents.length === 0) return;
      const idx = selectedSubAgentId
        ? subAgents.findIndex((a) => a.id === selectedSubAgentId)
        : -1;
      const next = idx >= subAgents.length - 1 ? 0 : idx + 1;
      const a = subAgents[next];
      if (a) setSelectedSubAgentId(a.id);
      return;
    }
    if (input === 'x' || input === 'X') {
      if (!selectedSubAgentId) return;
      if (pipeline) pipeline.killSubAgent(selectedSubAgentId);
      return;
    }
    if (input === 'l' || input === 'L') {
      if (!selectedSubAgentId) return;
      setViewAgentId(selectedSubAgentId);
      setView('sub-agent-detail');
      return;
    }
    if (input === ' ') {
      setShowSubAgentLogsInMain(!showSubAgentLogsInMain);
      return;
    }
  });

  if (subAgents.length === 0) {
    return (
      <Box flexDirection="column" paddingX={1}>
        <Text bold color="cyan">
          Sub-agents
        </Text>
        <Text dimColor>No sub-agents have been spawned yet.</Text>
        <Text dimColor>Press Esc or q to return.</Text>
      </Box>
    );
  }

  // Show as many agents as fit in the viewport, with the selected one always visible.
  const maxRows = Math.max(3, rows - 6);
  const selectedIdx = subAgents.findIndex((a) => a.id === selectedSubAgentId);
  let start = 0;
  if (selectedIdx >= maxRows) start = selectedIdx - maxRows + 1;
  const end = Math.min(subAgents.length, start + maxRows);
  const visible = subAgents.slice(start, end);

  return (
    <Box flexDirection="column" paddingX={1}>
      <Box>
        <Text bold color="cyan">
          Sub-agents
        </Text>
        <Text dimColor>{'  '}{subAgents.length} total · showing {start + 1}-{end}</Text>
      </Box>
      <Text dimColor>
        ↑/↓ select · x kill · l view logs · space toggle in-main · Esc/q back
      </Text>
      <Text dimColor>
        sub-agent output in main log: {showSubAgentLogsInMain ? 'on' : 'off'}
      </Text>
      <Box flexDirection="column" marginTop={1}>
        {visible.map((a) => (
          <AgentRow
            key={a.id}
            agent={a}
            selected={a.id === selectedSubAgentId}
            cols={cols}
          />
        ))}
      </Box>
    </Box>
  );
}

function AgentRow({
  agent,
  selected,
  cols,
}: {
  agent: SubAgentInfo;
  selected: boolean;
  cols: number;
}): React.ReactElement {
  const statusColor =
    agent.status === 'running' ? 'yellow' : agent.status === 'completed' ? 'green' : 'red';
  const age = formatAge(agent.startedAt, agent.endedAt);
  const idShort = agent.id.length > 14 ? agent.id.slice(0, 13) + '…' : agent.id;
  const taskPreview = agent.task.length > 60 ? agent.task.slice(0, 59) + '…' : agent.task;
  return (
    <Box>
      <Text inverse={selected} color={selected ? 'cyan' : undefined}>
        {' '}
        {selected ? '▶' : ' '} {idShort.padEnd(14)} {agent.role.padEnd(10)}{' '}
      </Text>
      <Text color={statusColor} inverse={selected}>
        {' '}[{agent.status}] {age.padStart(6)} · {taskPreview}
      </Text>
    </Box>
  );
}

function formatAge(start: number, end?: number): string {
  const ms = (end ?? Date.now()) - start;
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.floor((ms % 60_000) / 1000);
  return `${m}m${s.toString().padStart(2, '0')}s`;
}

interface SubAgentDetailViewProps {
  onBack: () => void;
}

export function SubAgentDetailView({ onBack }: SubAgentDetailViewProps): React.ReactElement {
  const viewAgentId = useStore((s) => s.viewAgentId);
  const subAgents = useStore((s) => s.subAgents);
  const subAgentLogs = useStore((s) => s.subAgentLogs);
  const { stdout } = useStdout();
  const rows = stdout?.rows ?? 24;

  const agent = subAgents.find((a) => a.id === viewAgentId);
  const log = viewAgentId ? subAgentLogs[viewAgentId] ?? [] : [];

  useInput((input, key) => {
    if (key.escape || input === 'q') {
      onBack();
    }
  });

  if (!agent) {
    return (
      <Box flexDirection="column" paddingX={1}>
        <Text bold color="red">
          Sub-agent not found
        </Text>
        <Text dimColor>Press Esc or q to go back.</Text>
      </Box>
    );
  }

  const headerH = 4;
  const visibleRows = Math.max(3, rows - headerH);
  const start = Math.max(0, log.length - visibleRows);
  const slice = log.slice(start);

  return (
    <Box flexDirection="column" paddingX={1}>
      <Box>
        <Text bold color="cyan">
          {agent.id}
        </Text>
        <Text>
          {'  '}
          role=<Text color="magenta">{agent.role}</Text> status=<Text color={agent.status === 'running' ? 'yellow' : 'green'}>{agent.status}</Text>
        </Text>
      </Box>
      <Text dimColor>task: {agent.task}</Text>
      <Text dimColor>
        {log.length} log line(s) · showing {start + 1}-{start + slice.length} · Esc/q back
      </Text>
      <Box flexDirection="column" marginTop={1}>
        {slice.map((line) => (
          <LogLineView key={line.id} line={line} />
        ))}
      </Box>
    </Box>
  );
}

function LogLineView({ line }: { line: OutputLine }): React.ReactElement {
  if (line.codeBlock) {
    return (
      <Text backgroundColor="blackBright" color="white">
        {'  '}
        {line.text}
      </Text>
    );
  }
  const segments = parseMarkdownInline(line.text);
  return (
    <Text dimColor>
      {' '}
      {segments.map((s, i) => (
        <Text key={i} bold={s.bold} dimColor={s.dim} color={s.color}>
          {s.text}
        </Text>
      ))}
    </Text>
  );
}
