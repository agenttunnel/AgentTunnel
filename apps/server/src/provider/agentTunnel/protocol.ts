export interface AgentTunnelToolCall {
  readonly name: string;
  readonly arguments: Record<string, unknown>;
}

const TOOL_LINE = /^[ \t]*__AGENTTUNNEL_TOOL__[ \t]+(\{.*\})\s*$/;

/** Parse one line of AgentTunnel's deliberately small out-of-band protocol. */
export function parseAgentTunnelToolCall(line: string): AgentTunnelToolCall | null {
  const match = TOOL_LINE.exec(line);
  if (!match?.[1]) return null;

  let value: unknown;
  try {
    value = JSON.parse(match[1]);
  } catch {
    return null;
  }

  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  if (
    typeof record.name !== "string" ||
    !record.arguments ||
    typeof record.arguments !== "object" ||
    Array.isArray(record.arguments)
  ) {
    return null;
  }

  return {
    name: record.name,
    arguments: record.arguments as Record<string, unknown>,
  };
}
