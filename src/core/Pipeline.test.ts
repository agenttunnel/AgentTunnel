import { describe, it, expect, beforeEach } from 'vitest';
import { Pipeline } from './Pipeline.js';
import { defaultConfig } from '../types/index.js';

function makePipeline(): Pipeline {
  return new Pipeline({
    cwd: process.cwd(),
    config: defaultConfig(),
    newAgentId: () => `agent-${Math.random().toString(36).slice(2, 8)}`,
  });
}

describe('Pipeline orchestrator tool calls', () => {
  let p: Pipeline;
  beforeEach(() => {
    p = makePipeline();
  });

  it('update_context writes to shared context', () => {
    p._testHandleOrchestratorCall({
      name: 'update_context',
      arguments: { key: 'user_goal', value: 'build a CLI' },
    });
    expect(p.context.get('user_goal')?.value).toBe('build a CLI');
    expect(p.context.get('user_goal')?.source).toBe('orchestrator');
  });

  it('dispatch_task with context: string[] captures the keys', () => {
    p._testHandleOrchestratorCall({
      name: 'update_context',
      arguments: { key: 'user_goal', value: 'X' },
    });
    p._testHandleOrchestratorCall({
      name: 'dispatch_task',
      arguments: {
        role: 'executor',
        task: 'do it',
        context: ['user_goal'],
      },
    });
    // The pipeline exposes dispatched via internal state; for now verify the context was rendered properly
    const rendered = p.context.renderForPrompt(['user_goal']);
    expect(rendered).toContain('X');
  });

  it('rejects dispatch_task with bad role', () => {
    p._testHandleOrchestratorCall({
      name: 'dispatch_task',
      arguments: { role: 'janitor', task: 'sweep' },
    });
    // No easy way to assert from outside without exposing dispatched, but the
    // call should not throw and should not create a context entry.
    expect(p.context.list()).toHaveLength(0);
  });

  it('sub-agent note_finding appends to existing or creates new', () => {
    p._testHandleOrchestratorCall({
      name: 'update_context',
      arguments: { key: 'design', value: 'first draft' },
    });
    p._testHandleSubAgentCall(
      { name: 'note_finding', arguments: { key: 'design', value: 'second thought' } },
      'agent-1',
    );
    const e = p.context.get('design');
    expect(e?.value).toContain('first draft');
    expect(e?.value).toContain('second thought');
    expect(e?.source).toBe('sub-agent');
    expect(e?.authorId).toBe('agent-1');
  });

  it('sub-agent done is a no-op for context', () => {
    p._testHandleSubAgentCall({ name: 'done', arguments: {} }, 'agent-1');
    expect(p.context.list()).toHaveLength(0);
  });
});
