import { describe, it, expect } from 'vitest';
import { defaultConfig } from './index.js';

describe('defaultConfig', () => {
  it('returns a fresh config with all three roles', () => {
    const config = defaultConfig();
    expect(config).toEqual({
      orchestrator: { harness: 'opencode', model: '' },
      executor: { harness: 'opencode', model: '' },
      reviewer: { harness: 'opencode', model: '' },
    });
  });

  it('returns independent objects on each call', () => {
    const a = defaultConfig();
    const b = defaultConfig();
    expect(a).not.toBe(b);
    a.executor.model = 'changed';
    expect(b.executor.model).toBe('');
  });
});
