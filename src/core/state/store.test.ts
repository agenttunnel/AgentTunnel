import { describe, it, expect, beforeEach } from 'vitest';
import { useStore, _resetStoreForTests } from './store.js';
import { defaultConfig } from '../../types/index.js';

describe('store', () => {
  beforeEach(() => {
    _resetStoreForTests();
  });

  it('addOutput appends lines and resets scroll', () => {
    useStore.getState().addOutput('hello', 'assistant');
    useStore.getState().addOutput('world', 'assistant');
    const { output } = useStore.getState();
    expect(output).toHaveLength(2);
    expect(output[0]?.text).toBe('hello');
    expect(output[1]?.text).toBe('world');
  });

  it('addOutput tags code-block lines', () => {
    const s = useStore.getState();
    s.addOutput('```ts', 'assistant');
    s.addOutput('const x = 1;', 'assistant');
    s.addOutput('```', 'assistant');
    const { output } = useStore.getState();
    expect(output.find((l) => l.codeBlock)?.text).toContain('const x = 1;');
  });

  it('clear empties output, error, subAgents, and resets phase', () => {
    const s = useStore.getState();
    s.addOutput('foo', 'assistant');
    s.setError('boom');
    s.clear();
    const after = useStore.getState();
    expect(after.output).toEqual([]);
    expect(after.error).toBeNull();
    expect(after.subAgents).toEqual([]);
    expect(after.pipelinePhase.type).toBe('idle');
  });

  it('setRoleConfig updates a single role', () => {
    useStore.getState().setRoleConfig('executor', { harness: 'opencode', model: 'gpt-x' });
    expect(useStore.getState().config.executor.model).toBe('gpt-x');
    expect(useStore.getState().config.orchestrator.model).toBe('');
  });

  it('pushHistory dedupes and appends', () => {
    const s = useStore.getState();
    s.pushHistory('a');
    s.pushHistory('b');
    s.pushHistory('a');
    const { history } = useStore.getState();
    expect(history).toEqual(['b', 'a']);
  });

  it('pushHistory ignores whitespace-only entries', () => {
    useStore.getState().pushHistory('   ');
    expect(useStore.getState().history).toEqual([]);
  });

  it('setInputValue toggles showHints based on /', () => {
    useStore.getState().setInputValue('/');
    expect(useStore.getState().showHints).toBe(true);
    useStore.getState().setInputValue('hello');
    expect(useStore.getState().showHints).toBe(false);
  });

  it('defaultConfig is independent of store state', () => {
    useStore.getState().setRoleConfig('orchestrator', { harness: 'x', model: 'y' });
    expect(defaultConfig().orchestrator.model).toBe('');
  });

  it('addSubAgentLog appends to per-agent log buffer', () => {
    useStore.getState().addSubAgentLog('a1', 'hello', 'tool_output');
    useStore.getState().addSubAgentLog('a1', 'world', 'tool_output');
    const logs = useStore.getState().subAgentLogs;
    expect(logs['a1']).toHaveLength(2);
    expect(logs['a1']?.[0]?.text).toBe('hello');
  });

  it('addSubAgentLog does NOT add to main output by default', () => {
    useStore.getState().addSubAgentLog('a1', 'hidden', 'tool_output');
    expect(useStore.getState().output).toHaveLength(0);
  });

  it('addSubAgentLog mirrors to main output when toggle is on', () => {
    useStore.getState().setShowSubAgentLogsInMain(true);
    useStore.getState().addSubAgentLog('a1', 'visible', 'tool_output');
    expect(useStore.getState().output.length).toBeGreaterThan(0);
    expect(useStore.getState().output[0]?.text).toBe('visible');
  });

  it('addSubAgentLog caps each agent buffer at SUB_AGENT_LOG_CAP', () => {
    const cap = 200;
    for (let i = 0; i < cap + 50; i++) {
      useStore.getState().addSubAgentLog('a1', `line-${i}`, 'tool_output');
    }
    const buf = useStore.getState().subAgentLogs['a1'] ?? [];
    expect(buf).toHaveLength(cap);
    expect(buf[buf.length - 1]?.text).toBe(`line-${cap + 49}`);
  });

  it('clear() resets sub-agent state but preserves current view', () => {
    useStore.getState().addSubAgentLog('a1', 'x', 'tool_output');
    useStore.getState().setView('sub-agents');
    useStore.getState().clear();
    expect(useStore.getState().subAgentLogs).toEqual({});
    expect(useStore.getState().view).toBe('sub-agents');
  });
});
