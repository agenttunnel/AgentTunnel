import { describe, it, expect } from 'vitest';
import { parseToolCallLine, validateToolCall, ToolRegistry } from './ToolRegistry.js';
import { DISPATCH_TASK_TOOL, DONE_TOOL } from './prompts.js';
import type { ToolCall } from './tools.js';

describe('parseToolCallLine', () => {
  it('parses a simple dispatch_task line', () => {
    const line = '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"do x"}}';
    const call = parseToolCallLine(line);
    expect(call).toEqual({ name: 'dispatch_task', arguments: { role: 'executor', task: 'do x' } });
  });

  it('parses a done line with reason', () => {
    const call = parseToolCallLine('__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"all good"}}');
    expect(call).toEqual({ name: 'done', arguments: { reason: 'all good' } });
  });

  it('returns null for non-tool lines', () => {
    expect(parseToolCallLine('hello world')).toBeNull();
    expect(parseToolCallLine('__AGENTTUNNEL_TOOL__ not json')).toBeNull();
    expect(parseToolCallLine('__AGENTTUNNEL_TOOL__ {"name":42}')).toBeNull();
  });

  it('tolerates leading whitespace', () => {
    const call = parseToolCallLine('   __AGENTTUNNEL_TOOL__ {"name":"done","arguments":{}}');
    expect(call?.name).toBe('done');
  });
});

describe('validateToolCall', () => {
  it('rejects unknown tool names', () => {
    const call: ToolCall = { name: 'wrong', arguments: {} };
    const err = validateToolCall(call, DISPATCH_TASK_TOOL);
    expect(err).toMatch(/unknown tool/);
  });

  it('rejects missing required arguments', () => {
    const call: ToolCall = { name: 'dispatch_task', arguments: { role: 'executor' } };
    const err = validateToolCall(call, DISPATCH_TASK_TOOL);
    expect(err).toMatch(/missing required argument: task/);
  });

  it('rejects wrong enum values', () => {
    const call: ToolCall = {
      name: 'dispatch_task',
      arguments: { role: 'janitor', task: 'x' },
    };
    const err = validateToolCall(call, DISPATCH_TASK_TOOL);
    expect(err).toMatch(/must be one of/);
  });

  it('rejects unknown arguments', () => {
    const call: ToolCall = {
      name: 'dispatch_task',
      arguments: { role: 'executor', task: 'x', weird: true },
    };
    const err = validateToolCall(call, DISPATCH_TASK_TOOL);
    expect(err).toMatch(/unknown argument: weird/);
  });

  it('accepts a valid call', () => {
    const call: ToolCall = {
      name: 'dispatch_task',
      arguments: { role: 'executor', task: 'do something' },
    };
    expect(validateToolCall(call, DISPATCH_TASK_TOOL)).toBeNull();
  });

  it('accepts done with empty arguments', () => {
    const call: ToolCall = { name: 'done', arguments: {} };
    expect(validateToolCall(call, DONE_TOOL)).toBeNull();
  });
});

describe('ToolRegistry', () => {
  it('invokes a registered handler with validation', async () => {
    const reg = new ToolRegistry();
    reg.register(DISPATCH_TASK_TOOL, () => ({ ok: true, output: 'queued' }));
    const result = await reg.invoke({
      name: 'dispatch_task',
      arguments: { role: 'executor', task: 'fix bug' },
    });
    expect(result).toEqual({ ok: true, output: 'queued' });
  });

  it('returns an error for an unknown tool', async () => {
    const reg = new ToolRegistry();
    const result = await reg.invoke({ name: 'nope', arguments: {} });
    expect(result.ok).toBe(false);
    expect(result.error).toMatch(/unknown tool/);
  });

  it('returns an error when the handler throws', async () => {
    const reg = new ToolRegistry();
    reg.register(DONE_TOOL, () => {
      throw new Error('boom');
    });
    const result = await reg.invoke({ name: 'done', arguments: {} });
    expect(result.ok).toBe(false);
    expect(result.error).toBe('boom');
  });
});
