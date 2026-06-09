import { describe, it, expect } from 'vitest';
import { applyKey } from './TextInputBox.jsx';

const k = (opts: Partial<{ upArrow: boolean; downArrow: boolean; leftArrow: boolean; rightArrow: boolean; return: boolean; backspace: boolean; delete: boolean; tab: boolean; escape: boolean; ctrl: boolean; meta: boolean; shift: boolean }> = {}) => opts as never;

describe('TextInputBox.applyKey', () => {
  it('appends printable characters', () => {
    expect(applyKey('ab', 'c', k({}))).toEqual({ next: 'abc' });
  });

  it('handles backspace by removing the last char', () => {
    expect(applyKey('abc', '', k({ backspace: true }))).toEqual({ next: 'ab' });
  });

  it('handles delete (treated like backspace in this minimal input)', () => {
    expect(applyKey('abc', '', k({ delete: true }))).toEqual({ next: 'ab' });
  });

  it('backspace on empty returns empty (no-op)', () => {
    expect(applyKey('', '', k({ backspace: true }))).toEqual({ next: '' });
  });

  it('Enter returns submit flag', () => {
    expect(applyKey('abc', '', k({ return: true }))).toEqual({ next: 'abc', submit: true });
  });

  it('passes through on up arrow (returns null)', () => {
    expect(applyKey('abc', '', k({ upArrow: true }))).toBeNull();
  });

  it('passes through on down arrow (returns null)', () => {
    expect(applyKey('abc', '', k({ downArrow: true }))).toBeNull();
  });

  it('passes through on left/right arrow (returns null)', () => {
    expect(applyKey('abc', '', k({ leftArrow: true }))).toBeNull();
    expect(applyKey('abc', '', k({ rightArrow: true }))).toBeNull();
  });

  it('passes through on tab (returns null)', () => {
    expect(applyKey('abc', '', k({ tab: true }))).toBeNull();
  });

  it('passes through on escape (returns null)', () => {
    expect(applyKey('abc', '', k({ escape: true }))).toBeNull();
  });

  it('passes through on ctrl modifier (returns null)', () => {
    expect(applyKey('abc', 'c', k({ ctrl: true }))).toBeNull();
  });

  it('ignores empty input with no special key', () => {
    expect(applyKey('abc', '', k({}))).toBeNull();
  });
});
