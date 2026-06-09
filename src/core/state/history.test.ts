import { describe, it, expect, beforeEach } from 'vitest';
import { useStore, _resetStoreForTests } from './store.js';

describe('history navigation inputs (store state)', () => {
  beforeEach(() => {
    _resetStoreForTests();
  });

  it('pushHistory keeps last entry at the end', () => {
    useStore.getState().pushHistory('first');
    useStore.getState().pushHistory('second');
    expect(useStore.getState().history).toEqual(['first', 'second']);
  });

  it('pushHistory dedupes', () => {
    useStore.getState().pushHistory('a');
    useStore.getState().pushHistory('b');
    useStore.getState().pushHistory('a');
    expect(useStore.getState().history).toEqual(['b', 'a']);
  });

  it('historyIndex defaults to -1', () => {
    expect(useStore.getState().historyIndex).toBe(-1);
  });

  it('simulated ↑ recall: index goes to last entry, value populates', () => {
    useStore.getState().pushHistory('add tests');
    useStore.getState().pushHistory('refactor session');

    // simulate ↑ at empty input
    const next = useStore.getState().history.length - 1;
    useStore.getState().setHistoryIndex(next);
    const recalled = useStore.getState().history[next];
    expect(recalled).toBe('refactor session');
    if (recalled !== undefined) useStore.getState().setInputValue(recalled);
    expect(useStore.getState().inputValue).toBe('refactor session');
  });

  it('simulated ↑ then ↓: index moves and value updates', () => {
    useStore.getState().pushHistory('a');
    useStore.getState().pushHistory('b');
    useStore.getState().pushHistory('c');

    // First ↑: index = 2 → 'c'
    useStore.getState().setHistoryIndex(2);
    expect(useStore.getState().history[useStore.getState().historyIndex]).toBe('c');
    // ↓: index = 1 → 'b'
    useStore.getState().setHistoryIndex(1);
    expect(useStore.getState().history[useStore.getState().historyIndex]).toBe('b');
    // ↓: index = 0 → 'a'
    useStore.getState().setHistoryIndex(0);
    expect(useStore.getState().history[useStore.getState().historyIndex]).toBe('a');
    // ↓ past oldest: index = -1, value cleared
    useStore.getState().setHistoryIndex(-1);
    expect(useStore.getState().historyIndex).toBe(-1);
  });

  it('history survives across pushHistory calls', () => {
    useStore.getState().pushHistory('a');
    useStore.getState().pushHistory('b');
    expect(useStore.getState().history).toEqual(['a', 'b']);
    useStore.getState().pushHistory('c');
    expect(useStore.getState().history).toEqual(['a', 'b', 'c']);
  });
});
