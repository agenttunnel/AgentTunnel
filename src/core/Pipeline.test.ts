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
    const rendered = p.context.renderForPrompt(['user_goal']);
    expect(rendered).toContain('X');
  });

  it('rejects dispatch_task with bad role', () => {
    p._testHandleOrchestratorCall({
      name: 'dispatch_task',
      arguments: { role: 'janitor', task: 'sweep' },
    });
    expect(p.context.list()).toHaveLength(0);
    expect(p._testPipelines()).toHaveLength(0);
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

describe('Pipeline scheduling (named pipelines, parallel/sequential)', () => {
  let p: Pipeline;
  beforeEach(() => {
    p = makePipeline();
  });

  it('dispatches with same pipeline id group into one pipeline', () => {
    p._testHandleOrchestratorCall({
      name: 'dispatch_task',
      arguments: { role: 'executor', task: 'a', pipeline: 'research' },
    });
    p._testHandleOrchestratorCall({
      name: 'dispatch_task',
      arguments: { role: 'executor', task: 'b', pipeline: 'research' },
    });
    const pipes = p._testPipelines();
    expect(pipes).toHaveLength(1);
    expect(pipes[0]?.id).toBe('research');
    expect(pipes[0]?.taskIds).toHaveLength(2);
  });

  it('different pipeline ids create separate pipelines', () => {
    p._testHandleOrchestratorCall({
      name: 'dispatch_task',
      arguments: { role: 'executor', task: 'a', pipeline: 'a' },
    });
    p._testHandleOrchestratorCall({
      name: 'dispatch_task',
      arguments: { role: 'executor', task: 'b', pipeline: 'b' },
    });
    const pipes = p._testPipelines();
    expect(pipes).toHaveLength(2);
    expect(pipes.map((x) => x.id).sort()).toEqual(['a', 'b']);
  });

  it('captures mode and depends_on from first dispatch', () => {
    p._testHandleOrchestratorCall({
      name: 'dispatch_task',
      arguments: {
        role: 'executor',
        task: 'a',
        pipeline: 'code',
        mode: 'sequential',
        depends_on: ['research'],
      },
    });
    const pipes = p._testPipelines();
    expect(pipes[0]?.mode).toBe('sequential');
    expect(pipes[0]?.dependsOn).toEqual(['research']);
  });

  it('default mode is parallel when omitted', () => {
    p._testHandleOrchestratorCall({
      name: 'dispatch_task',
      arguments: { role: 'executor', task: 'a', pipeline: 'p' },
    });
    expect(p._testPipelines()[0]?.mode).toBe('parallel');
  });

  it('no pipeline id gets an auto-generated one', () => {
    p._testHandleOrchestratorCall({
      name: 'dispatch_task',
      arguments: { role: 'executor', task: 'a' },
    });
    expect(p._testPipelines()[0]?.id).toMatch(/^pipe-/);
  });

  it('re-declaring mode/depends_on on the same pipeline is ignored', () => {
    p._testHandleOrchestratorCall({
      name: 'dispatch_task',
      arguments: { role: 'executor', task: 'a', pipeline: 'p', mode: 'parallel' },
    });
    p._testHandleOrchestratorCall({
      name: 'dispatch_task',
      arguments: { role: 'executor', task: 'b', pipeline: 'p', mode: 'sequential', depends_on: ['x'] },
    });
    const pipe = p._testPipelines()[0];
    expect(pipe?.mode).toBe('parallel');
    expect(pipe?.dependsOn).toEqual([]);
    expect(pipe?.taskIds).toHaveLength(2);
  });
});

describe('Pipeline.killSubAgent', () => {
  it('returns false for an unknown agent id', () => {
    const p = makePipeline();
    expect(p.killSubAgent('does-not-exist')).toBe(false);
  });

  it('returns false when there are no running agents', () => {
    const p = makePipeline();
    expect(p.killSubAgent('agent-1')).toBe(false);
  });
});
