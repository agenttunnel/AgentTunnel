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
});
