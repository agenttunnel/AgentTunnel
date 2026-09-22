import {
  AGENT_TUNNEL_HARNESS_OPTIONS,
  AGENT_TUNNEL_REASONING_OPTIONS,
  agentTunnelRoleHarnessKey,
  agentTunnelRoleModelKey,
  agentTunnelRoleVariantKey,
  readAgentTunnelRoles,
} from "@t3tools/client-runtime/state/agentTunnelRoles";

import {
  DEFAULT_SERVER_SETTINGS,
  ProviderDriverKind,
  ProviderInstanceId,
  type ProviderInstanceConfig,
  type ServerSettings,
  type ServerSettingsPatch,
} from "@t3tools/contracts";

import type { TuiAgentTunnelCatalog } from "./format.ts";

/**
 * The AgentTunnel screen: the workflow provider is a team of roles, so the
 * thing worth configuring is which harness, model, and reasoning effort each
 * role runs on. The role vocabulary and its defaults come from the shared
 * client logic the web app uses; this module only turns that into the rows a
 * terminal can draw and the pickers behind them.
 *
 * Every picker row carries `field::value`, so choosing a row is a patch to one
 * settings field and the reducer needs no per-row bookkeeping.
 */

export type TuiAgentTunnelRowKind = "toggle" | "model" | "harness" | "variant" | "number";

export interface TuiAgentTunnelRow {
  /** The `AgentTunnelSettings` field this row edits. */
  readonly key: string;
  readonly label: string;
  readonly value: string;
  readonly kind: TuiAgentTunnelRowKind;
  /** The role this row belongs to, when it belongs to one. */
  readonly role: string | null;
}

const ORCHESTRATOR_FALLBACK = "use the selected model";
const ROLE_FALLBACK = "use the orchestrator model";

/** How many passes and how long each may take; the server accepts these ranges. */
const NUMBER_OPTIONS: Readonly<Record<string, ReadonlyArray<number>>> = {
  maxOrchestratorPasses: [1, 2, 3, 4, 5, 6, 8, 10, 12],
  orchestratorPassTimeoutMinutes: [5, 10, 15, 20, 30, 45, 60, 90, 120],
  orchestratorRecapIntervalSeconds: [0, 10, 15, 30, 60, 120, 300, 600, 900],
};

const NUMBER_LABELS: Readonly<Record<string, string>> = {
  maxOrchestratorPasses: "max orchestrator passes",
  orchestratorPassTimeoutMinutes: "pass timeout",
  orchestratorRecapIntervalSeconds: "recap interval",
};

const NUMBER_SUFFIX: Readonly<Record<string, string>> = {
  maxOrchestratorPasses: "",
  orchestratorPassTimeoutMinutes: "m",
  orchestratorRecapIntervalSeconds: "s",
};

const readString = (config: Record<string, unknown> | null, key: string): string => {
  const value = config?.[key];
  return typeof value === "string" ? value.trim() : "";
};

const readNumber = (
  config: Record<string, unknown> | null,
  key: string,
  fallback: number,
): number => {
  const value = config?.[key];
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
};

/** The rows the AgentTunnel screen lists, in the order a workflow reads. */
export function agentTunnelRows(
  config: Record<string, unknown> | null,
): ReadonlyArray<TuiAgentTunnelRow> {
  const rows: Array<TuiAgentTunnelRow> = [
    {
      key: "autoMode",
      label: "auto mode",
      value: config?.autoMode === true ? "on" : "off",
      kind: "toggle",
      role: null,
    },
    {
      key: "dangerouslySkipPermissions",
      label: "skip harness permissions",
      value: config?.dangerouslySkipPermissions === false ? "off" : "on",
      kind: "toggle",
      role: null,
    },
  ];

  for (const role of readAgentTunnelRoles(config ?? {})) {
    const harness = role.harness;
    const modelKey = agentTunnelRoleModelKey(role.id) ?? `${role.id}Model`;
    const harnessKey = agentTunnelRoleHarnessKey(role.id) ?? `${role.id}Harness`;
    const variantKey = agentTunnelRoleVariantKey(role.id) ?? `${role.id}Variant`;
    rows.push(
      {
        key: modelKey,
        label: `${role.label.toLowerCase()} model`,
        value: role.model ?? (role.id === "orchestrator" ? ORCHESTRATOR_FALLBACK : ROLE_FALLBACK),
        kind: "model",
        role: role.id,
      },
      {
        key: harnessKey,
        label: `${role.label.toLowerCase()} harness`,
        value: harness,
        kind: "harness",
        role: role.id,
      },
      {
        key: variantKey,
        label: `${role.label.toLowerCase()} reasoning`,
        value: role.variant ?? "default",
        kind: "variant",
        role: role.id,
      },
    );
  }

  for (const key of Object.keys(NUMBER_OPTIONS)) {
    const fallback =
      key === "maxOrchestratorPasses" ? 4 : key === "orchestratorPassTimeoutMinutes" ? 15 : 30;
    rows.push({
      key,
      label: NUMBER_LABELS[key] ?? key,
      value: `${readNumber(config, key, fallback)}${NUMBER_SUFFIX[key] ?? ""}`,
      kind: "number",
      role: null,
    });
  }

  return rows;
}

/** One picker row: the field it patches and the value it writes. */
export function agentTunnelItem(
  field: string,
  value: string | number,
  label: string,
): {
  readonly id: string;
  readonly label: string;
  readonly detail: string | null;
} {
  return { id: `${field}::${value}`, label, detail: null };
}

/** Reads a picker row back into the patch it represents. */
export function parseAgentTunnelItem(
  id: string,
): { readonly field: string; readonly value: string | number } | null {
  const separator = id.indexOf("::");
  if (separator <= 0) return null;
  const field = id.slice(0, separator);
  const raw = id.slice(separator + 2);
  const numeric = Number(raw);
  return {
    field,
    value: raw.length > 0 && Number.isFinite(numeric) && /^\d+$/u.test(raw) ? numeric : raw,
  };
}

/** Models the role can run: the ones its harness reported, plus clearing back to the fallback. */
export function agentTunnelModelItems(
  config: Record<string, unknown> | null,
  catalog: TuiAgentTunnelCatalog,
  roleId: string,
): ReadonlyArray<{ readonly id: string; readonly label: string; readonly detail: string | null }> {
  const role = readAgentTunnelRoles(config ?? {}).find((entry) => entry.id === roleId);
  const field = `${roleId}Model`;
  const items = [
    agentTunnelItem(field, "", roleId === "orchestrator" ? ORCHESTRATOR_FALLBACK : ROLE_FALLBACK),
  ];
  const harness = role?.harness ?? "opencode";
  for (const model of catalog.models) {
    if (model.harnesses !== null && !model.harnesses.includes(harness)) continue;
    items.push({
      id: `${field}::${model.slug}`,
      label: model.name,
      detail: model.harnesses === null ? null : model.harnesses.join(", "),
    });
  }
  return items;
}

/** The harness CLIs a role can run on. */
export function agentTunnelHarnessItems(
  roleId: string,
): ReadonlyArray<{ readonly id: string; readonly label: string; readonly detail: string | null }> {
  return AGENT_TUNNEL_HARNESS_OPTIONS.map((option) =>
    agentTunnelItem(`${roleId}Harness`, option.value, option.label),
  );
}

/** Reasoning effort passed to the role's harness. */
export function agentTunnelVariantItems(
  roleId: string,
): ReadonlyArray<{ readonly id: string; readonly label: string; readonly detail: string | null }> {
  return AGENT_TUNNEL_REASONING_OPTIONS.map((option) =>
    agentTunnelItem(`${roleId}Variant`, option.value, option.label),
  );
}

/** The allowed values for a numeric workflow setting. */
export function agentTunnelNumberItems(
  key: string,
): ReadonlyArray<{ readonly id: string; readonly label: string; readonly detail: string | null }> {
  return (NUMBER_OPTIONS[key] ?? []).map((value) =>
    agentTunnelItem(key, value, `${value}${NUMBER_SUFFIX[key] ?? ""}`),
  );
}

/** The workflow instance's settings, wherever they live: instance map first. */
export function resolveAgentTunnelConfig(
  settings: Pick<ServerSettings, "providers" | "providerInstances">,
): Record<string, unknown> {
  const instance = Object.values(settings.providerInstances).find(
    (entry) => entry.driver === "agenttunnel",
  );
  const config = instance?.config;
  if (typeof config === "object" && config !== null) return config as Record<string, unknown>;
  return settings.providers.agenttunnel as Record<string, unknown>;
}

/**
 * One field of the workflow settings, as the patch the server expects.
 *
 * The instance map is the source of truth and is replaced whole, never patched
 * per entry; for the built-in instance the legacy `providers.agenttunnel` blob
 * is reset to its defaults at the same time, exactly as the web client does, so
 * the two copies cannot drift apart.
 */
export function buildAgentTunnelPatch(
  settings: Pick<ServerSettings, "providers" | "providerInstances">,
  field: string,
  value: string | number | boolean,
): ServerSettingsPatch {
  const found = Object.entries(settings.providerInstances).find(
    ([, entry]) => entry.driver === "agenttunnel",
  );
  const instanceId = ProviderInstanceId.make(found?.[0] ?? "agenttunnel");
  const existing = found?.[1];
  const base =
    typeof existing?.config === "object" && existing.config !== null
      ? (existing.config as Record<string, unknown>)
      : (settings.providers.agenttunnel as Record<string, unknown>);
  const instance: ProviderInstanceConfig = {
    ...(existing ?? { driver: ProviderDriverKind.make("agenttunnel") }),
    driver: ProviderDriverKind.make("agenttunnel"),
    config: { ...base, [field]: value },
  };
  return {
    ...(instanceId === "agenttunnel"
      ? {
          providers: {
            ...settings.providers,
            agenttunnel: DEFAULT_SERVER_SETTINGS.providers.agenttunnel,
          },
        }
      : {}),
    providerInstances: { ...settings.providerInstances, [instanceId]: instance },
  };
}
