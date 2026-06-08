import { describe, it, expect } from 'vitest';
import { stripAnsi, splitAnsi, hasAnsi } from './AnsiCleaner.js';

describe('AnsiCleaner', () => {
  it('strips SGR color codes', () => {
    expect(stripAnsi('\x1b[31mhello\x1b[0m')).toBe('hello');
  });

  it('strips cursor movement codes', () => {
    expect(stripAnsi('a\x1b[2Kb\x1b[Hc')).toBe('abc');
  });

  it('strips OSC sequences', () => {
    expect(stripAnsi('hi\x1b]0;title\x07bye')).toBe('hibye');
  });

  it('returns plain text unchanged', () => {
    expect(stripAnsi('plain text')).toBe('plain text');
  });

  it('hasAnsi detects sequences', () => {
    expect(hasAnsi('\x1b[31mhi\x1b[0m')).toBe(true);
    expect(hasAnsi('hi')).toBe(false);
  });

  it('splitAnsi returns non-empty pieces only', () => {
    expect(splitAnsi('\x1b[31ma\x1b[0mb')).toEqual(['a', 'b']);
  });
});
