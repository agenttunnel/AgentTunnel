import type { ToolCall, ToolDefinition, ToolParameters, ToolResult } from './tools.js';

const TOOL_LINE = /^[ \t]*__AGENTTUNNEL_TOOL__[ \t]+(\{.*\})\s*$/;

export function parseToolCallLine(line: string): ToolCall | null {
  const m = TOOL_LINE.exec(line);
  if (!m || !m[1]) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(m[1]);
  } catch {
    return null;
  }
  if (!parsed || typeof parsed !== 'object') return null;
  const obj = parsed as Record<string, unknown>;
  const name = obj.name;
  const args = obj.arguments;
  if (typeof name !== 'string') return null;
  if (!args || typeof args !== 'object') return null;
  return { name, arguments: args as Record<string, unknown> };
}

export function validateToolCall(call: ToolCall, def: ToolDefinition): string | null {
  if (call.name !== def.name) return `unknown tool: ${call.name}`;
  const params = def.parameters;
  if (params.required) {
    for (const key of params.required) {
      if (!(key in call.arguments)) return `missing required argument: ${key}`;
    }
  }
  for (const [key, value] of Object.entries(call.arguments)) {
    const paramDef = params.properties[key];
    if (!paramDef) return `unknown argument: ${key}`;
    const err = validateValue(value, paramDef, def.name, key);
    if (err) return err;
  }
  return null;
}

function validateValue(value: unknown, def: ToolParameters['properties'][string], tool: string, key: string): string | null {
  switch (def.type) {
    case 'string':
      if (typeof value !== 'string') return `${tool}.${key} must be a string`;
      return null;
    case 'number':
      if (typeof value !== 'number') return `${tool}.${key} must be a number`;
      return null;
    case 'boolean':
      if (typeof value !== 'boolean') return `${tool}.${key} must be a boolean`;
      return null;
    case 'enum':
      if (typeof value !== 'string') return `${tool}.${key} must be a string`;
      if (def.enum && !def.enum.includes(value)) {
        return `${tool}.${key} must be one of: ${def.enum.join(', ')}`;
      }
      return null;
    case 'array':
      if (!Array.isArray(value)) return `${tool}.${key} must be an array`;
      if (def.items) {
        for (let i = 0; i < value.length; i++) {
          const err = validateValue(value[i], def.items, tool, `${key}[${i}]`);
          if (err) return err;
        }
      }
      return null;
    case 'object':
      if (typeof value !== 'object' || value === null) return `${tool}.${key} must be an object`;
      return null;
  }
}

export type ToolHandler = (call: ToolCall) => Promise<ToolResult> | ToolResult;

export class ToolRegistry {
  private readonly tools = new Map<string, { def: ToolDefinition; handler: ToolHandler }>();

  register(def: ToolDefinition, handler: ToolHandler): void {
    this.tools.set(def.name, { def, handler });
  }

  get(name: string): { def: ToolDefinition; handler: ToolHandler } | undefined {
    return this.tools.get(name);
  }

  list(): ToolDefinition[] {
    return [...this.tools.values()].map((t) => t.def);
  }

  async invoke(call: ToolCall): Promise<ToolResult> {
    const entry = this.tools.get(call.name);
    if (!entry) return { ok: false, error: `unknown tool: ${call.name}` };
    const err = validateToolCall(call, entry.def);
    if (err) return { ok: false, error: err };
    try {
      return await entry.handler(call);
    } catch (e) {
      return { ok: false, error: (e as Error).message };
    }
  }
}
