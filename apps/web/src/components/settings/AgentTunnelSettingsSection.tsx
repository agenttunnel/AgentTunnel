"use client";

import type { ServerProviderModel } from "@t3tools/contracts";
import { Bot, ShieldCheck, Workflow } from "lucide-react";
import { Input } from "../ui/input";
import { Select, SelectItem, SelectPopup, SelectTrigger, SelectValue } from "../ui/select";
import { Switch } from "../ui/switch";
import {
  AGENT_TUNNEL_HARNESS_OPTIONS as SUPPORTED_HARNESSES,
  AGENT_TUNNEL_REASONING_OPTIONS,
} from "../agentTunnelRoles.logic";

const ROLES = [
  {
    key: "orchestrator",
    label: "Orchestrator",
    description: "Plans the workflow, dispatches tasks, and reviews team results.",
  },
  {
    key: "executor",
    label: "Executor",
    description: "Implements individual tasks assigned by the orchestrator.",
  },
  {
    key: "reviewer",
    label: "Reviewer",
    description: "Inspects completed work for correctness, consistency, and tests.",
  },
] as const;

interface AgentTunnelSettingsSectionProps {
  readonly config: unknown;
  readonly models: ReadonlyArray<ServerProviderModel>;
  readonly onChange: (nextConfig: Record<string, unknown> | undefined) => void;
}

function readConfigString(config: unknown, key: string): string {
  if (config === null || typeof config !== "object") return "";
  const value = (config as Record<string, unknown>)[key];
  return typeof value === "string" ? value : "";
}

function readConfigBoolean(config: unknown, key: string, defaultValue: boolean): boolean {
  if (config === null || typeof config !== "object") return defaultValue;
  const value = (config as Record<string, unknown>)[key];
  return typeof value === "boolean" ? value : defaultValue;
}

function writeConfigValue(
  config: unknown,
  onChange: (nextConfig: Record<string, unknown> | undefined) => void,
  key: string,
  value: string | boolean,
): void {
  const base: Record<string, unknown> =
    config !== null && typeof config === "object" ? { ...(config as Record<string, unknown>) } : {};
  if (typeof value === "string" && value.length === 0) {
    delete base[key];
  } else {
    base[key] = value;
  }
  onChange(Object.keys(base).length > 0 ? base : undefined);
}

function RoleSettings({
  config,
  models,
  role,
  onChange,
}: {
  readonly config: unknown;
  readonly models: ReadonlyArray<ServerProviderModel>;
  readonly role: (typeof ROLES)[number];
  readonly onChange: (nextConfig: Record<string, unknown> | undefined) => void;
}) {
  const harnessKey = `${role.key}Harness`;
  const modelKey = `${role.key}Model`;
  const variantKey = `${role.key}Variant`;
  const harness = readConfigString(config, harnessKey) || "opencode";
  const model = readConfigString(config, modelKey);
  const variant = readConfigString(config, variantKey);
  const harnessOptions =
    SUPPORTED_HARNESSES.find((entry) => entry.value === harness) === undefined
      ? [...SUPPORTED_HARNESSES, { value: harness, label: `${harness} (custom)` }]
      : SUPPORTED_HARNESSES;

  return (
    <div className="rounded-lg border border-border/60 bg-muted/20 p-3">
      <div className="mb-2 flex items-center gap-2">
        <Bot className="size-4 text-muted-foreground" />
        <span className="text-sm font-medium text-foreground">{role.label}</span>
      </div>
      <p className="mb-3 text-xs leading-snug text-muted-foreground">{role.description}</p>
      <div className="space-y-3">
        <label className="block">
          <span className="text-xs font-medium text-foreground">Harness</span>
          <Select
            value={harness}
            onValueChange={(next) => writeConfigValue(config, onChange, harnessKey, next ?? "")}
          >
            <SelectTrigger className="mt-1.5">
              <SelectValue placeholder="Select a harness" />
            </SelectTrigger>
            <SelectPopup className="max-h-72 w-[min(24rem,calc(100vw-3rem))]">
              {harnessOptions.map((entry) => (
                <SelectItem key={entry.value} value={entry.value}>
                  {entry.label}
                </SelectItem>
              ))}
            </SelectPopup>
          </Select>
          <span className="mt-1 block text-xs text-muted-foreground">
            CLI used to run this role. Must be on the server PATH.
          </span>
        </label>
        <label className="block">
          <span className="text-xs font-medium text-foreground">Model</span>
          <Select
            value={model || ""}
            onValueChange={(next) => writeConfigValue(config, onChange, modelKey, next ?? "")}
          >
            <SelectTrigger className="mt-1.5">
              <SelectValue placeholder="Use the workflow default model" />
            </SelectTrigger>
            <SelectPopup className="max-h-72 w-[min(24rem,calc(100vw-3rem))]">
              <SelectItem value="">Use the workflow default model</SelectItem>
              {models.map((entry) => (
                <SelectItem key={entry.slug} value={entry.slug}>
                  {entry.name}
                </SelectItem>
              ))}
            </SelectPopup>
          </Select>
          <span className="mt-1 block text-xs text-muted-foreground">
            Optional per-role override of the workflow model selected in the composer.
          </span>
        </label>
        <label className="block">
          <span className="text-xs font-medium text-foreground">Reasoning</span>
          <Select
            value={variant}
            onValueChange={(next) => writeConfigValue(config, onChange, variantKey, next ?? "")}
          >
            <SelectTrigger className="mt-1.5">
              <SelectValue placeholder="Default" />
            </SelectTrigger>
            <SelectPopup className="max-h-72 w-[min(24rem,calc(100vw-3rem))]">
              {AGENT_TUNNEL_REASONING_OPTIONS.map((entry) => (
                <SelectItem key={entry.value || "default"} value={entry.value}>
                  {entry.label}
                </SelectItem>
              ))}
            </SelectPopup>
          </Select>
          <span className="mt-1 block text-xs text-muted-foreground">
            Reasoning effort passed to the harness (opencode `--variant`).
          </span>
        </label>
      </div>
    </div>
  );
}

/**
 * Interactive role configuration for the AgentTunnel workflow provider.
 *
 * Replaces the generic schema form so each role (orchestrator, executor,
 * reviewer) gets a focused harness + model editor with a live model picker
 * populated from the provider snapshot.
 */
export function AgentTunnelSettingsSection({
  config,
  models,
  onChange,
}: AgentTunnelSettingsSectionProps) {
  const skipPermissions = readConfigBoolean(config, "dangerouslySkipPermissions", true);
  const maxPasses = Number(readConfigString(config, "maxOrchestratorPasses") || 4);

  return (
    <div className="space-y-4">
      <div>
        <div className="mb-2 flex items-center gap-2">
          <Workflow className="size-4 text-muted-foreground" />
          <span className="text-sm font-medium text-foreground">Roles</span>
        </div>
        <div className="grid gap-3">
          {ROLES.map((role) => (
            <RoleSettings
              key={role.key}
              config={config}
              models={models}
              role={role}
              onChange={onChange}
            />
          ))}
        </div>
      </div>

      <div className="rounded-lg border border-border/60 bg-muted/20 p-3">
        <div className="flex items-center justify-between gap-4">
          <div className="flex items-center gap-2">
            <ShieldCheck className="size-4 text-muted-foreground" />
            <div>
              <span className="block text-sm font-medium text-foreground">
                Skip harness permissions
              </span>
              <span className="block text-xs text-muted-foreground">
                Pass the harness permission-bypass flag to child sessions.
              </span>
            </div>
          </div>
          <Switch
            checked={skipPermissions}
            onCheckedChange={(checked) =>
              writeConfigValue(config, onChange, "dangerouslySkipPermissions", checked)
            }
            aria-label="Skip harness permissions"
          />
        </div>
      </div>

      <label className="block rounded-lg border border-border/60 bg-muted/20 p-3">
        <span className="block text-sm font-medium text-foreground">Max orchestrator passes</span>
        <span className="mb-2 block text-xs text-muted-foreground">
          How many summary passes the orchestrator may run before the workflow must finish.
        </span>
        <Input
          className="mt-1.5 max-w-28"
          type="number"
          min={1}
          max={12}
          value={Number.isFinite(maxPasses) ? maxPasses : 4}
          onChange={(event) => {
            const parsed = Number.parseInt(event.target.value, 10);
            if (!Number.isNaN(parsed)) {
              writeConfigValue(config, onChange, "maxOrchestratorPasses", String(parsed));
            }
          }}
        />
      </label>
    </div>
  );
}
