import { ProviderDriverKind, type ThreadModelConfig } from "@t3tools/contracts";

export const AGENT_TUNNEL_ROLES = [
  {
    id: "orchestrator",
    label: "Orchestrator",
    key: "orchestratorModel",
    harnessKey: "orchestratorHarness",
    variantKey: "orchestratorVariant",
  },
  {
    id: "planner",
    label: "Planner",
    key: "plannerModel",
    harnessKey: "plannerHarness",
    variantKey: "plannerVariant",
  },
  {
    id: "executor",
    label: "Executor",
    key: "executorModel",
    harnessKey: "executorHarness",
    variantKey: "executorVariant",
  },
  {
    id: "tester",
    label: "Tester",
    key: "testerModel",
    harnessKey: "testerHarness",
    variantKey: "testerVariant",
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
  { value: "pi", label: "Pi" },
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
  pi: "pi",
};

/** Driver kind backing a harness preset, for enablement checks. */
export function agentTunnelHarnessProviderKind(harness: string): string | null {
  return HARNESS_PROVIDER_KINDS[harness] ?? null;
}

/** Harness CLI basename, so a configured path maps to the same preset. */
function agentTunnelHarnessBasename(harness: string): string {
  const base = harness.split(/[\\/]/).pop() ?? harness;
  return base.replace(/\.exe$/iu, "");
}

/**
 * Driver kind whose models a harness runs, resolving CLI paths by basename —
 * `/opt/homebrew/bin/pi` and `pi` both run the Pi catalog.
 */
export function agentTunnelHarnessDriverKind(harness: string): ProviderDriverKind | null {
  const kind =
    HARNESS_PROVIDER_KINDS[harness] ?? HARNESS_PROVIDER_KINDS[agentTunnelHarnessBasename(harness)];
  return kind === undefined ? null : ProviderDriverKind.make(kind);
}

/**
 * Harness options for the provider instances actually present on the
 * environment. The preset catalog says which CLIs the workflow driver knows how
 * to run; this narrows it to the ones whose provider kind is installed, so a
 * role never offers a harness the environment cannot run. A harness a role is
 * already configured with stays listed even when its provider is not installed,
 * so a saved selection never silently disappears.
 */
export function agentTunnelHarnessOptions(input: {
  readonly providerKinds: ReadonlyArray<string>;
  readonly selectedHarnesses?: ReadonlyArray<string> | undefined;
}): ReadonlyArray<{ readonly value: string; readonly label: string }> {
  const installedKinds = new Set(input.providerKinds);
  const options: Array<{ value: string; label: string }> = [];
  const seen = new Set<string>();
  for (const option of AGENT_TUNNEL_HARNESS_OPTIONS) {
    const kind = agentTunnelHarnessProviderKind(option.value);
    if (kind === null || !installedKinds.has(kind)) {
      continue;
    }
    seen.add(option.value);
    options.push(option);
  }
  for (const selected of input.selectedHarnesses ?? []) {
    const value = selected.trim();
    if (value.length === 0 || seen.has(value)) {
      continue;
    }
    seen.add(value);
    const preset = AGENT_TUNNEL_HARNESS_OPTIONS.find((option) => option.value === value);
    options.push(preset ?? { value, label: `${value} (custom)` });
  }
  return options;
}

export interface AgentTunnelRoleOption {
  readonly id: string;
  readonly label: string;
  readonly model: string | null;
  readonly harness: string;
  readonly variant: string | null;
  /** Auto mode lets the orchestrator pick this role's per-task model. */
  readonly auto: boolean;
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
  const autoMode = source.autoMode === true;
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
      // Auto mode is per dispatched task, so every role except the orchestrator
      // itself has its model chosen by the orchestrator.
      auto: autoMode && role.id !== "orchestrator",
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

/** Reads the workflow auto mode flag: the orchestrator picks per-task models. */
export function readAgentTunnelAutoMode(config: Record<string, unknown> | undefined): boolean {
  return config?.autoMode === true;
}

/**
 * Snapshot of the role config a thread runs with: the same keys as the
 * AgentTunnel provider settings, with the settings' defaults filled in.
 * Threads store this snapshot so later default changes never rewrite how an
 * existing thread runs; unknown keys (pass limits, permission flags, …) are
 * deliberately dropped — they stay environment settings, not thread state.
 */
export function readAgentTunnelModelConfig(
  config: Record<string, unknown> | undefined,
): ThreadModelConfig {
  const source = config ?? {};
  const read = (key: string, fallback: string): string => {
    const value = source[key];
    return typeof value === "string" && value.trim().length > 0 ? value.trim() : fallback;
  };
  const approved = readAgentTunnelApprovedModels(source);
  return {
    autoMode: source.autoMode === true,
    orchestratorHarness: read("orchestratorHarness", "opencode"),
    plannerHarness: read("plannerHarness", "opencode"),
    executorHarness: read("executorHarness", "opencode"),
    testerHarness: read("testerHarness", "opencode"),
    reviewerHarness: read("reviewerHarness", "opencode"),
    orchestratorModel: read("orchestratorModel", ""),
    plannerModel: read("plannerModel", ""),
    executorModel: read("executorModel", ""),
    testerModel: read("testerModel", ""),
    reviewerModel: read("reviewerModel", ""),
    orchestratorVariant: read("orchestratorVariant", ""),
    plannerVariant: read("plannerVariant", ""),
    executorVariant: read("executorVariant", ""),
    testerVariant: read("testerVariant", ""),
    reviewerVariant: read("reviewerVariant", ""),
    ...(approved === null ? {} : { approvedModels: approved }),
  };
}

/** Enables or disables workflow auto mode. */
export function applyAgentTunnelAutoMode(
  config: Record<string, unknown>,
  autoMode: boolean,
): Record<string, unknown> {
  const next = { ...config };
  if (autoMode) {
    next.autoMode = true;
  } else {
    delete next.autoMode;
  }
  return next;
}

/** Provider config key holding the slugs auto mode may dispatch. */
const APPROVED_MODELS_KEY = "approvedModels";

/**
 * Reads the auto-mode approved model slugs. `null` means the key is absent,
 * which is not the same as an empty list: absent approves every model (the
 * pre-existing behavior), while `[]` approves none.
 */
export function readAgentTunnelApprovedModels(
  config: Record<string, unknown> | undefined,
): ReadonlyArray<string> | null {
  const value = config?.[APPROVED_MODELS_KEY];
  if (!Array.isArray(value)) {
    return null;
  }
  return value.filter((entry): entry is string => typeof entry === "string");
}

/** Whether auto mode may dispatch the slug; absent list approves everything. */
export function isAgentTunnelModelApproved(
  config: Record<string, unknown> | undefined,
  slug: string,
): boolean {
  const approved = readAgentTunnelApprovedModels(config);
  return approved === null || approved.includes(slug);
}

/**
 * Toggles one model's approval. An absent list already approves everything, so
 * enabling is a no-op; disabling materializes the explicit list of every other
 * model. Once explicit, the slug is added or removed without collapsing an
 * empty list back to absent — `[]` legitimately means "none approved".
 */
export function applyAgentTunnelApprovedModelToggle(
  config: Record<string, unknown>,
  slug: string,
  enabled: boolean,
  allSlugs: ReadonlyArray<string>,
): Record<string, unknown> {
  const next = { ...config };
  const current = readAgentTunnelApprovedModels(config);
  if (current === null) {
    if (!enabled) {
      next[APPROVED_MODELS_KEY] = allSlugs.filter((entry) => entry !== slug);
    }
    return next;
  }
  next[APPROVED_MODELS_KEY] = enabled
    ? current.includes(slug)
      ? [...current]
      : [...current, slug]
    : current.filter((entry) => entry !== slug);
  return next;
}

/** Approves every model by deleting the key (absent means all). */
export function applyAgentTunnelApprovedModelsEnableAll(
  config: Record<string, unknown>,
): Record<string, unknown> {
  const next = { ...config };
  delete next[APPROVED_MODELS_KEY];
  return next;
}

/** Approves no models with an explicit empty list. */
export function applyAgentTunnelApprovedModelsDisableAll(
  config: Record<string, unknown>,
): Record<string, unknown> {
  return { ...config, [APPROVED_MODELS_KEY]: [] };
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
