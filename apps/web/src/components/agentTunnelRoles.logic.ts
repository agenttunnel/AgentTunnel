export const AGENT_TUNNEL_ROLES = [
  {
    id: "orchestrator",
    label: "Orchestrator",
    key: "orchestratorModel",
    harnessKey: "orchestratorHarness",
    variantKey: "orchestratorVariant",
  },
  {
    id: "executor",
    label: "Executor",
    key: "executorModel",
    harnessKey: "executorHarness",
    variantKey: "executorVariant",
  },
  {
    id: "reviewer",
    label: "Reviewer",
    key: "reviewerModel",
    harnessKey: "reviewerHarness",
    variantKey: "reviewerVariant",
  },
] as const;

export type AgentTunnelRole = (typeof AGENT_TUNNEL_ROLES)[number];

export const AGENT_TUNNEL_HARNESS_OPTIONS: ReadonlyArray<{
  readonly value: string;
  readonly label: string;
}> = [
  { value: "opencode", label: "OpenCode" },
  { value: "codex", label: "Codex" },
  { value: "claude", label: "Claude Code" },
  { value: "cursor-agent", label: "Cursor" },
  { value: "grok", label: "Grok Build" },
];

export const AGENT_TUNNEL_REASONING_OPTIONS: ReadonlyArray<{
  readonly value: string;
  readonly label: string;
}> = [
  { value: "", label: "Default" },
  { value: "minimal", label: "Minimal" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "max", label: "Max" },
];

const HARNESS_PROVIDER_KINDS: Record<string, string> = {
  opencode: "opencode",
  codex: "codex",
  claude: "claudeAgent",
  "cursor-agent": "cursor",
  grok: "grok",
};

/** Driver kind backing a harness preset, for enablement checks. */
export function agentTunnelHarnessProviderKind(harness: string): string | null {
  return HARNESS_PROVIDER_KINDS[harness] ?? null;
}

export interface AgentTunnelRoleOption {
  readonly id: string;
  readonly label: string;
  readonly model: string | null;
  readonly harness: string;
  readonly variant: string | null;
}

export function agentTunnelRoleModelKey(roleId: string): string | null {
  return AGENT_TUNNEL_ROLES.find((role) => role.id === roleId)?.key ?? null;
}

export function agentTunnelRoleHarnessKey(roleId: string): string | null {
  return AGENT_TUNNEL_ROLES.find((role) => role.id === roleId)?.harnessKey ?? null;
}

export function agentTunnelRoleVariantKey(roleId: string): string | null {
  return AGENT_TUNNEL_ROLES.find((role) => role.id === roleId)?.variantKey ?? null;
}

export function readAgentTunnelRoles(
  config: Record<string, unknown> | undefined,
): ReadonlyArray<AgentTunnelRoleOption> {
  const source = config ?? {};
  return AGENT_TUNNEL_ROLES.map((role) => {
    const model = source[role.key];
    const harness = source[role.harnessKey];
    const variant = source[role.variantKey];
    return {
      id: role.id,
      label: role.label,
      model: typeof model === "string" && model.length > 0 ? model : null,
      harness:
        typeof harness === "string" && harness.trim().length > 0 ? harness.trim() : "opencode",
      variant: typeof variant === "string" && variant.trim().length > 0 ? variant.trim() : null,
    };
  });
}

/** Sets the role's model, or clears the override when the model is empty. */
export function applyAgentTunnelRoleModel(
  config: Record<string, unknown>,
  roleId: string,
  model: string,
): Record<string, unknown> {
  const key = agentTunnelRoleModelKey(roleId);
  if (key === null) {
    return config;
  }
  const next = { ...config };
  if (model.trim().length > 0) {
    next[key] = model;
  } else {
    delete next[key];
  }
  return next;
}

/** Sets the role's harness, or clears it back to the opencode default. */
export function applyAgentTunnelRoleHarness(
  config: Record<string, unknown>,
  roleId: string,
  harness: string,
): Record<string, unknown> {
  const key = agentTunnelRoleHarnessKey(roleId);
  if (key === null) {
    return config;
  }
  const next = { ...config };
  if (harness.trim().length > 0) {
    next[key] = harness.trim();
  } else {
    delete next[key];
  }
  return next;
}

/** Sets the role's reasoning variant, or clears it back to the harness default. */
export function applyAgentTunnelRoleVariant(
  config: Record<string, unknown>,
  roleId: string,
  variant: string,
): Record<string, unknown> {
  const key = agentTunnelRoleVariantKey(roleId);
  if (key === null) {
    return config;
  }
  const next = { ...config };
  if (variant.trim().length > 0) {
    next[key] = variant.trim();
  } else {
    delete next[key];
  }
  return next;
}
