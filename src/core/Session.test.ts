import { describe, it, expect } from 'vitest';
import { Session } from './Session.js';

describe('Session', () => {
  it('constructs with default options', () => {
    const s = new Session();
    expect(s.isRunning()).toBe(false);
    expect(s.pid()).toBeUndefined();
  });

  it('exposes unsubscribe functions for all subscribers', () => {
    const s = new Session();
    const a = s.onEvent(() => {});
    const b = s.onOutput(() => {});
    const c = s.onExit(() => {});
    expect(typeof a).toBe('function');
    expect(typeof b).toBe('function');
    expect(typeof c).toBe('function');
    a();
    b();
    c();
  });

  it('cancel on an idle session is a no-op', () => {
    const s = new Session();
    expect(() => s.cancel()).not.toThrow();
  });

  it('rejects a second run while one is in flight', async () => {
    const s = new Session({ harness: 'node', dangerouslySkipPermissions: false });
    const pending = s.run({ prompt: '"hi"' });
    await expect(s.run({ prompt: '"again"' })).rejects.toThrow(/already running/);
    s.cancel();
    await pending.catch(() => undefined);
  });
});
