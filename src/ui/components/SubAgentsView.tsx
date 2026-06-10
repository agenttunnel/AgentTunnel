import React, { useEffect } from 'react';
import { Box, Text, useInput, useStdout } from 'ink';
import { useStore } from '../../core/state/store.js';
import { usePipeline } from '../PipelineContext.jsx';
import type { OutputLine, PipelineInfo, SubAgentInfo } from '../../types/index.js';
import { colors, glyphs, colorForStatus } from '../theme.js';
import { MarkdownLine } from '../MarkdownLine.jsx';

interface SubAgentsViewProps {
  onBack: () => void;
}

export function SubAgentsView({ onBack }: SubAgentsViewProps): React.ReactElement {
  const subAgents = useStore((s) => s.subAgents);
  const pipelines = useStore((s) => s.pipelines);
  const selectedSubAgentId = useStore((s) => s.selectedSubAgentId);
  const setSelectedSubAgentId = useStore((s) => s.setSelectedSubAgentId);
  const setViewAgentId = useStore((s) => s.setViewAgentId);
  const setView = useStore((s) => s.setView);
  const showSubAgentLogsInMain = useStore((s) => s.showSubAgentLogsInMain);
  const setShowSubAgentLogsInMain = useStore((s) => s.setShowSubAgentLogsInMain);
  const pipeline = usePipeline();
  const { stdout } = useStdout();
  const rows = stdout?.rows ?? 24;
  const cols = stdout?.columns ?? 80;

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
        <Text bold color={colors.primary}>
          {`${glyphs.blockFull} SUB-AGENTS`}
        </Text>
        <Box borderStyle="single" borderColor={colors.dim} paddingX={1}>
          <Text color={colors.grid}>No sub-agents have been spawned yet.</Text>
        </Box>
        <Text color={colors.dim}>{`${glyphs.chevron} Press Esc or q to return.`}</Text>
      </Box>
    );
  }

  const maxRows = Math.max(3, rows - 10);
  // Group agents by pipeline id, preserving order within each group.
  const groups = groupByPipeline(subAgents, pipelines);
  const totalAgents = subAgents.length;
  let consumed = 0;
  const visibleBlocks: React.ReactElement[] = [];
  let selectedRendered = false;

  for (const group of groups) {
    const pipeline: PipelineInfo | undefined = group.pipeline;
    const headerLines = pipeline ? 1 : 0;
    const remaining = maxRows - consumed - headerLines;
    if (remaining <= 0) {
      if (!selectedRendered) {
        const selectedInGroup = group.agents.find((a) => a.id === selectedSubAgentId);
        if (selectedInGroup) {
          if (pipeline) {
            const doneCount = group.agents.filter((a) => a.status === 'completed').length;
            const failedCount = group.agents.filter((a) => a.status === 'failed').length;
            const okColor = failedCount > 0 ? colors.danger : colorForStatus(pipeline.status);
            const modeLabel = pipeline.mode === 'sequential' ? 'seq' : 'par';
            visibleBlocks.push(
              <Box key={`h-${pipeline.id}`}>
                <Text color={colors.accent} bold>
                  {` ${glyphs.chevron} `}
                </Text>
                <Text color={colors.primary} bold>
                  {pipeline.name}
                </Text>
                <Text color={colors.dim}>
                  {`  [${modeLabel}] ${doneCount}/${group.agents.length} done`}
                </Text>
                <Text color={okColor}>{`  ${pipeline.status}`}</Text>
                {pipeline.dependsOn.length > 0 ? (
                  <Text color={colors.grid}>
                    {`  after [${pipeline.dependsOn.join(', ')}]`}
                  </Text>
                ) : null}
              </Box>,
            );
            consumed += 1;
          }
          visibleBlocks.push(
            <AgentRow
              key={selectedInGroup.id}
              agent={selectedInGroup}
              selected={true}
            />,
          );
          consumed += 1;
          selectedRendered = true;
        }
      }
      continue;
    }
    if (pipeline) {
      const doneCount = group.agents.filter((a) => a.status === 'completed').length;
      const failedCount = group.agents.filter((a) => a.status === 'failed').length;
      const okColor = failedCount > 0 ? colors.danger : colorForStatus(pipeline.status);
      const modeLabel = pipeline.mode === 'sequential' ? 'seq' : 'par';
      visibleBlocks.push(
        <Box key={`h-${pipeline.id}`}>
          <Text color={colors.accent} bold>
            {` ${glyphs.chevron} `}
          </Text>
          <Text color={colors.primary} bold>
            {pipeline.name}
          </Text>
          <Text color={colors.dim}>
            {`  [${modeLabel}] ${doneCount}/${group.agents.length} done`}
          </Text>
          <Text color={okColor}>{`  ${pipeline.status}`}</Text>
          {pipeline.dependsOn.length > 0 ? (
            <Text color={colors.grid}>
              {`  after [${pipeline.dependsOn.join(', ')}]`}
            </Text>
          ) : null}
        </Box>,
      );
      consumed += 1;
    }
    const agentsToShow = group.agents.slice(0, Math.max(0, maxRows - consumed));
    for (const a of agentsToShow) {
      visibleBlocks.push(
        <AgentRow
          key={a.id}
          agent={a}
          selected={a.id === selectedSubAgentId}
        />,
      );
      consumed += 1;
      if (a.id === selectedSubAgentId) selectedRendered = true;
    }
    if (group.agents.length > agentsToShow.length) {
      visibleBlocks.push(
        <Text key={`more-${group.pipeline?.id ?? '?'}`} color={colors.dim}>
          {`  ${glyphs.blockHalf} +${group.agents.length - agentsToShow.length} more in this pipeline`}
        </Text>,
      );
    }
  }

  return (
    <Box flexDirection="column" paddingX={1}>
      <Box>
        <Text bold color={colors.primary}>
          {`${glyphs.blockFull} SUB-AGENTS`}
        </Text>
        <Text color={colors.dim}>
          {`  ${groups.length} pipeline(s) · ${totalAgents} agent(s)`}
        </Text>
      </Box>
      <Text color={colors.dim}>
        {glyphs.scanStart}
        {glyphs.scanMid.repeat(Math.max(0, cols - 2))}
        {glyphs.scanEnd}
      </Text>
      <Text color={colors.primary}>
        {`${glyphs.chevron} ↑/↓ select · x kill · l view logs · space toggle in-main · Esc/q back`}
      </Text>
      <Text color={showSubAgentLogsInMain ? colors.accent : colors.dim}>
        {`${glyphs.blockHalf} sub-agent output in main log: ${showSubAgentLogsInMain ? 'ON' : 'off'}`}
      </Text>
      <Box flexDirection="column" marginTop={1}>
        {visibleBlocks}
      </Box>
    </Box>
  );
}

interface PipelineGroup {
  pipeline?: PipelineInfo;
  agents: SubAgentInfo[];
}

function groupByPipeline(
  agents: SubAgentInfo[],
  pipelines: PipelineInfo[],
): PipelineGroup[] {
  const byPipe = new Map<string, SubAgentInfo[]>();
  const orphan: SubAgentInfo[] = [];
  for (const a of agents) {
    const id = a.pipelineId ?? '';
    if (id) {
      const arr = byPipe.get(id) ?? [];
      arr.push(a);
      byPipe.set(id, arr);
    } else {
      orphan.push(a);
    }
  }
  const result: PipelineGroup[] = [];
  for (const p of pipelines) {
    const arr = byPipe.get(p.id);
    if (arr && arr.length > 0) result.push({ pipeline: p, agents: arr });
    byPipe.delete(p.id);
  }
  for (const arr of byPipe.values()) {
    orphan.push(...arr);
  }
  if (orphan.length > 0) result.push({ pipeline: undefined, agents: orphan });
  return result;
}

function AgentRow({ agent, selected }: { agent: SubAgentInfo; selected: boolean }): React.ReactElement {
  const age = formatAge(agent.startedAt, agent.endedAt);
  const idShort = agent.id.length > 14 ? agent.id.slice(0, 13) + '…' : agent.id;
  const taskPreview = agent.task.length > 60 ? agent.task.slice(0, 59) + '…' : agent.task;
  const statusColor = colorForStatus(agent.status);
  const caret = selected ? glyphs.chevron : ' ';
  return (
    <Box>
      <Text
        backgroundColor={selected ? colors.primary : undefined}
        color={selected ? colors.onPrimary : colors.dim}
        bold={selected}
      >
        {` ${caret} ${idShort.padEnd(14)} ${agent.role.padEnd(10)} `}
      </Text>
      <Text
        backgroundColor={selected ? colors.primary : undefined}
        color={selected ? colors.onPrimary : statusColor}
      >
        {` [${agent.status.toUpperCase()}] ${age.padStart(6)} · ${taskPreview}`}
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
        <Text bold color={colors.danger}>
          {`${glyphs.blockFull} SUB-AGENT NOT FOUND`}
        </Text>
        <Text color={colors.dim}>{`${glyphs.chevron} Press Esc or q to go back.`}</Text>
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
        <Text bold color={colors.primary}>
          {`${glyphs.blockFull} ${agent.id}`}
        </Text>
        <Text>
          {'  '}role=<Text color={colors.magenta}>{agent.role}</Text> status=
          <Text color={colorForStatus(agent.status)}>{agent.status.toUpperCase()}</Text>
        </Text>
      </Box>
      <Text color={colors.dim}>task: {agent.task}</Text>
      <Text color={colors.primary}>
        {`${glyphs.chevron} ${log.length} log line(s) · showing ${start + 1}-${start + slice.length} · Esc/q back`}
      </Text>
      <Box flexDirection="column" marginTop={1} borderStyle="single" borderColor={colors.dim} paddingX={1}>
        {slice.map((line) => (
          <LogLineView key={line.id} line={line} />
        ))}
      </Box>
    </Box>
  );
}

function LogLineView({ line }: { line: OutputLine }): React.ReactElement {
  return <MarkdownLine line={line} dim />;
}
