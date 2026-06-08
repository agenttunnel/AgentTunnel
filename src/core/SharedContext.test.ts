import { describe, it, expect } from 'vitest';
import { SharedContext } from './SharedContext.js';

describe('SharedContext', () => {
  it('sets and gets entries', () => {
    const c = new SharedContext();
    c.set('user_goal', 'build a CLI');
    expect(c.get('user_goal')?.value).toBe('build a CLI');
    expect(c.has('user_goal')).toBe(true);
  });

  it('rejects invalid keys', () => {
    const c = new SharedContext();
    expect(() => c.set('Bad-Key', 'x')).toThrow(/invalid context key/);
    expect(() => c.set('1leading_digit', 'x')).toThrow(/invalid context key/);
  });

  it('appends to existing entries with provenance', () => {
    const c = new SharedContext();
    c.set('design_notes', 'first draft', 'orchestrator');
    c.append('design_notes', 'second thought', 'agent-1');
    const e = c.get('design_notes');
    expect(e?.value).toContain('first draft');
    expect(e?.value).toContain('second thought');
    expect(e?.source).toBe('sub-agent');
    expect(e?.authorId).toBe('agent-1');
  });

  it('appends to a missing key creates it', () => {
    const c = new SharedContext();
    c.append('new_idea', 'hello', 'agent-1');
    expect(c.get('new_idea')?.source).toBe('sub-agent');
  });

  it('renderForPrompt emits a markdown block per key', () => {
    const c = new SharedContext();
    c.set('a', 'apple');
    c.set('b', 'banana');
    const out = c.renderForPrompt(['a', 'b']);
    expect(out).toContain('### a');
    expect(out).toContain('apple');
    expect(out).toContain('### b');
    expect(out).toContain('banana');
  });

  it('renderForPrompt warns on missing keys', () => {
    const c = new SharedContext();
    const out = c.renderForPrompt(['nope']);
    expect(out).toMatch(/⚠/);
    expect(out).toContain('nope');
  });

  it('renderSummary lists entries sorted by key', () => {
    const c = new SharedContext();
    c.set('zeta', 'last');
    c.set('alpha', 'first');
    const out = c.renderSummary();
    const alphaIdx = out.indexOf('alpha');
    const zetaIdx = out.indexOf('zeta');
    expect(alphaIdx).toBeGreaterThan(-1);
    expect(zetaIdx).toBeGreaterThan(alphaIdx);
  });
});
