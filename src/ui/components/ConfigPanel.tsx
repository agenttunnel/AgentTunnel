import React, { useState } from 'react';
import { Box, Text, useInput, useStdout } from 'ink';
import TextInput from 'ink-text-input';
import { useStore } from '../../core/state/store.js';
import type { RoleName } from '../../types/index.js';

const SECTIONS: { key: RoleName; label: string }[] = [
  { key: 'orchestrator', label: 'Orchestrator' },
  { key: 'executor', label: 'Executor' },
  { key: 'reviewer', label: 'Reviewer' },
];

const FIELDS = [
  { key: 'harness', label: 'Harness' },
  { key: 'model', label: 'Model' },
] as const;

type FieldKey = (typeof FIELDS)[number]['key'];

interface ConfigPanelProps {
  onClose: () => void;
}

export function ConfigPanel({ onClose }: ConfigPanelProps): React.ReactElement {
  const config = useStore((s) => s.config);
  const models = useStore((s) => s.models);
  const setRoleConfig = useStore((s) => s.setRoleConfig);
  const { stdout } = useStdout();
  const cols = stdout?.columns ?? 80;

  const [sectionIdx, setSectionIdx] = useState(0);
  const [fieldIdx, setFieldIdx] = useState(0);
  const [editing, setEditing] = useState(false);
  const [editValue, setEditValue] = useState('');
  const [modelPickerOpen, setModelPickerOpen] = useState(false);
  const [modelPickIdx, setModelPickIdx] = useState(0);

  useInput((input, key) => {
    if (modelPickerOpen) {
      if (key.upArrow) {
        setModelPickIdx(Math.max(0, modelPickIdx - 1));
        return;
      }
      if (key.downArrow) {
        setModelPickIdx(Math.min(Math.max(0, models.length - 1), modelPickIdx + 1));
        return;
      }
      if (key.return) {
        const sec = SECTIONS[sectionIdx];
        const m = models[modelPickIdx];
        if (sec && m !== undefined) {
          setRoleConfig(sec.key, { ...config[sec.key], model: m });
        }
        setModelPickerOpen(false);
        return;
      }
      if (key.escape || input === 'q') {
        setModelPickerOpen(false);
        return;
      }
      return;
    }

    if (editing) return;

    if (key.upArrow) {
      if (fieldIdx > 0) setFieldIdx(fieldIdx - 1);
      else if (sectionIdx > 0) {
        setSectionIdx(sectionIdx - 1);
        setFieldIdx(FIELDS.length - 1);
      }
      return;
    }
    if (key.downArrow) {
      if (fieldIdx < FIELDS.length - 1) setFieldIdx(fieldIdx + 1);
      else if (sectionIdx < SECTIONS.length - 1) {
        setSectionIdx(sectionIdx + 1);
        setFieldIdx(0);
      }
      return;
    }
    if (key.return) {
      const sec = SECTIONS[sectionIdx];
      const fld = FIELDS[fieldIdx];
      if (!sec || !fld) return;
      if (fld.key === 'model') {
        setModelPickIdx(Math.max(0, models.indexOf(config[sec.key].model)));
        setModelPickerOpen(true);
      } else {
        setEditValue(config[sec.key][fld.key]);
        setEditing(true);
      }
      return;
    }
    if (key.escape || input === 'q') {
      onClose();
    }
  });

  return (
    <Box flexDirection="column" paddingX={1}>
      <Text bold color="cyan">
        Config
      </Text>
      <Text dimColor>↑/↓ navigate • Enter edit/pick • Esc back</Text>
      <Box flexDirection="column" marginTop={1}>
        {SECTIONS.map((sec, si) => {
          const role = config[sec.key];
          return (
            <Box key={sec.key} flexDirection="column" marginBottom={1}>
              <Text bold color={si === sectionIdx ? 'yellow' : undefined}>
                {sec.label}
              </Text>
              {FIELDS.map((fld, fi) => {
                const isCurrent = si === sectionIdx && fi === fieldIdx;
                return (
                  <Box key={fld.key} marginLeft={2}>
                    <Text color={isCurrent ? 'green' : 'gray'}>
                      {isCurrent ? '▶' : ' '} {fld.label.padEnd(8)}
                    </Text>
                    {editing && isCurrent ? (
                      <TextInput
                        value={editValue}
                        onChange={setEditValue}
                        onSubmit={(v) => {
                          setRoleConfig(sec.key, { ...role, [fld.key]: v });
                          setEditing(false);
                        }}
                      />
                    ) : (
                      <Text color={role[fld.key] ? undefined : 'gray'}>
                        {role[fld.key] || `(no ${fld.key})`}
                      </Text>
                    )}
                  </Box>
                );
              })}
            </Box>
          );
        })}
      </Box>
      {modelPickerOpen ? (
        <ModelPicker
          models={models}
          selected={modelPickIdx}
          cols={cols}
        />
      ) : null}
    </Box>
  );
}

function ModelPicker({
  models,
  selected,
  cols,
}: {
  models: string[];
  selected: number;
  cols: number;
}): React.ReactElement {
  if (models.length === 0) {
    return (
      <Box flexDirection="column" borderStyle="round" borderColor="yellow" paddingX={1}>
        <Text color="yellow">No models discovered. Run `opencode models` to verify.</Text>
        <Text dimColor>Esc to close</Text>
      </Box>
    );
  }
  const max = Math.max(3, Math.floor((cols - 8) / 24));
  const start = Math.max(0, Math.min(selected - Math.floor(max / 2), models.length - max));
  const end = Math.min(models.length, start + max);
  const visible = models.slice(start, end);
  return (
    <Box flexDirection="column" borderStyle="round" borderColor="cyan" paddingX={1}>
      <Text bold color="cyan">
        Select model
      </Text>
      {visible.map((m, i) => {
        const realIdx = start + i;
        return (
          <Text key={m} inverse={realIdx === selected} color={realIdx === selected ? 'cyan' : undefined}>
            {' '}
            {m}
          </Text>
        );
      })}
      {models.length > max ? (
        <Text dimColor>
          showing {start + 1}-{end} of {models.length}
        </Text>
      ) : null}
      <Text dimColor>↑/↓ move • Enter select • Esc close</Text>
    </Box>
  );
}
