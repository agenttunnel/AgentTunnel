export interface ToolDefinition {
  name: string;
  description: string;
  parameters: ToolParameters;
}

export type ToolParameterType = 'string' | 'number' | 'boolean' | 'enum' | 'array' | 'object';

export interface ToolParameter {
  type: ToolParameterType;
  description: string;
  required?: boolean;
  enum?: string[];
  items?: ToolParameter;
  properties?: Record<string, ToolParameter>;
}

export interface ToolParameters {
  type: 'object';
  properties: Record<string, ToolParameter>;
  required?: string[];
}

export interface ToolCall {
  name: string;
  arguments: Record<string, unknown>;
}

export interface ToolResult {
  ok: boolean;
  output?: string;
  error?: string;
}
