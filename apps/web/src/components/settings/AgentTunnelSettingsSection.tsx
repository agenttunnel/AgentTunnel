"use client";

import type { ServerProviderModel } from "@t3tools/contracts";
import { ChevronDown } from "lucide-react";
import { useState } from "react";
import { cn } from "~/lib/utils";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "../ui/collapsible";
import { Input } from "../ui/input";
import { Select, SelectItem, SelectPopup, SelectTrigger, SelectValue } from "../ui/select";
import { Switch } from "../ui/switch";
import {
  AGENT_TUNNEL_HARNESS_OPTIONS as SUPPORTED_HARNESSES,
  AGENT_TUNNEL_REASONING_OPTIONS,
} from "../agentTunnelRoles.logic";

const ROLES = [
  { key: "orchestrator", label: "Orchestrator" },
  { key: "executor", label: "Executor" },
  { key: "reviewer", label: "Reviewer" },
] as const;

const ROLE_GRID_COLUMNS = "sm:grid-cols-[7rem_minmax(0,1fr)_minmax(0,1fr)_10rem]";

interface AgentTunnelSettingsSectionProps {
  readonly config: unknown;
  readonly models: ReadonlyArray<ServerProviderModel>;
  readonly onChange: (nextConfig: Record<string, unknown> | undefined) => void;
}

function readConfigNumber(config: unknown, key: string, fallback: number): number {
  if (config === null || typeof config !== "object") return fallback;
  const value = (config as Record<string, unknown>)[key];
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && value.trim().length > 0) {
    const parsed = Number(value);
    if (Number.isFinite(parsed)) return parsed;
  }
  return fallback;
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
  value: string | boolean | number,
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

const selectPopupClassName = "max-h-72 w-[min(24rem,calc(100vw-3rem))]";

function RoleRow({
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
    <div className={cn("grid gap-2 px-3 py-2.5 sm:items-center", ROLE_GRID_COLUMNS)}>
      <span className="text-[13px] font-medium leading-5 text-foreground">{role.label}</span>
      <Select
        value={harness}
        onValueChange={(next) => writeConfigValue(config, onChange, harnessKey, next ?? "")}
      >
        <SelectTrigger aria-label={`${role.label} harness`}>
          <SelectValue placeholder="Harness" />
        </SelectTrigger>
        <SelectPopup className={selectPopupClassName}>
          {harnessOptions.map((entry) => (
            <SelectItem key={entry.value} value={entry.value}>
              {entry.label}
            </SelectItem>
          ))}
        </SelectPopup>
      </Select>
      <Select
        value={model || ""}
        onValueChange={(next) => writeConfigValue(config, onChange, modelKey, next ?? "")}
      >
        <SelectTrigger aria-label={`${role.label} model`}>
          <SelectValue placeholder="Workflow default" />
        </SelectTrigger>
        <SelectPopup className={selectPopupClassName}>
          <SelectItem value="">Workflow default</SelectItem>
          {models.map((entry) => (
            <SelectItem key={entry.slug} value={entry.slug}>
              {entry.name}
            </SelectItem>
          ))}
        </SelectPopup>
      </Select>
      <Select
        value={variant}
        onValueChange={(next) => writeConfigValue(config, onChange, variantKey, next ?? "")}
      >
        <SelectTrigger aria-label={`${role.label} reasoning`}>
          <SelectValue placeholder="Default" />
        </SelectTrigger>
        <SelectPopup className={selectPopupClassName}>
          {AGENT_TUNNEL_REASONING_OPTIONS.map((entry) => (
            <SelectItem key={entry.value || "default"} value={entry.value}>
              {entry.label}
            </SelectItem>
          ))}
        </SelectPopup>
      </Select>
    </div>
  );
}

function ToggleRow({
  label,
  description,
  checked,
  onCheckedChange,
}: {
  readonly label: string;
  readonly description: string;
  readonly checked: boolean;
  readonly onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <label className="flex items-center justify-between gap-4 px-3 py-2.5">
      <span className="min-w-0">
        <span className="block text-[13px] font-medium leading-5 text-foreground">{label}</span>
        <span className="block text-[13px] leading-5 text-muted-foreground">{description}</span>
      </span>
      <Switch checked={checked} onCheckedChange={onCheckedChange} aria-label={label} />
    </label>
  );
}

function NumberRow({
  label,
  description,
  value,
  min,
  max,
  onCommit,
}: {
  readonly label: string;
  readonly description: string;
  readonly value: number;
  readonly min: number;
  readonly max: number;
  readonly onCommit: (value: number) => void;
}) {
  return (
    <label className="flex items-center justify-between gap-4 px-3 py-2.5">
      <span className="min-w-0">
        <span className="block text-[13px] font-medium leading-5 text-foreground">{label}</span>
        <span className="block text-[13px] leading-5 text-muted-foreground">{description}</span>
      </span>
      <Input
        className="w-20 shrink-0"
        type="number"
        min={min}
        max={max}
        value={value}
        onChange={(event) => {
          const parsed = Number.parseInt(event.target.value, 10);
          if (!Number.isNaN(parsed)) {
            onCommit(Math.min(max, Math.max(min, parsed)));
          }
        }}
      />
    </label>
  );
}

/**
 * Interactive role configuration for the AgentTunnel workflow provider.
 *
 * Compact role grid plus a single Advanced disclosure, so the per-role harness,
 * model, and reasoning choices stay scannable without a wall of cards.
 */
export function AgentTunnelSettingsSection({
  config,
  models,
  onChange,
}: AgentTunnelSettingsSectionProps) {
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const skipPermissions = readConfigBoolean(config, "dangerouslySkipPermissions", true);
  const autoMode = readConfigBoolean(config, "autoMode", false);
  const maxPasses = readConfigNumber(config, "maxOrchestratorPasses", 4);
  const passTimeoutMinutes = readConfigNumber(config, "orchestratorPassTimeoutMinutes", 15);
  const recapIntervalSeconds = readConfigNumber(config, "orchestratorRecapIntervalSeconds", 30);

  return (
    <div className="space-y-3">
      <div className="overflow-hidden rounded-lg border border-border/60">
        <div
          className={cn(
            "hidden gap-2 border-b border-border/50 bg-muted/15 px-3 py-1.5 text-[11px] font-medium text-muted-foreground sm:grid sm:items-center",
            ROLE_GRID_COLUMNS,
          )}
        >
          <span>Role</span>
          <span>Harness</span>
          <span>Model</span>
          <span>Reasoning</span>
        </div>
        <div className="divide-y divide-border/50">
          {ROLES.map((role) => (
            <RoleRow
              key={role.key}
              config={config}
              models={models}
              role={role}
              onChange={onChange}
            />
          ))}
        </div>
      </div>

      <div className="divide-y divide-border/50 overflow-hidden rounded-lg border border-border/60">
        <ToggleRow
          label="Auto mode"
          description="Orchestrator picks each task's model; per-role models act as fallbacks."
          checked={autoMode}
          onCheckedChange={(checked) => writeConfigValue(config, onChange, "autoMode", checked)}
        />
        <ToggleRow
          label="Skip harness permissions"
          description="Pass the permission-bypass flag to child sessions."
          checked={skipPermissions}
          onCheckedChange={(checked) =>
            writeConfigValue(config, onChange, "dangerouslySkipPermissions", checked)
          }
        />
      </div>

      <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
        <CollapsibleTrigger className="flex items-center gap-1.5 rounded-sm px-1 py-1 text-xs text-muted-foreground hover:text-foreground">
          <ChevronDown
            aria-hidden
            className={cn("size-3 transition-transform", advancedOpen && "rotate-180")}
          />
          Advanced
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div className="mt-2 divide-y divide-border/50 overflow-hidden rounded-lg border border-border/60">
            <NumberRow
              label="Max orchestrator passes"
              description="Summary passes before the workflow must finish."
              value={Number.isFinite(maxPasses) ? maxPasses : 4}
              min={1}
              max={12}
              onCommit={(value) =>
                writeConfigValue(config, onChange, "maxOrchestratorPasses", value)
              }
            />
            <NumberRow
              label="Orchestrator pass timeout"
              description="Minutes a planning or summary pass may run."
              value={Number.isFinite(passTimeoutMinutes) ? passTimeoutMinutes : 15}
              min={1}
              max={120}
              onCommit={(value) =>
                writeConfigValue(config, onChange, "orchestratorPassTimeoutMinutes", value)
              }
            />
            <NumberRow
              label="Progress recap interval"
              description="Seconds between live progress recaps. 0 turns the heartbeat off."
              value={Number.isFinite(recapIntervalSeconds) ? recapIntervalSeconds : 30}
              min={0}
              max={900}
              onCommit={(value) =>
                writeConfigValue(config, onChange, "orchestratorRecapIntervalSeconds", value)
              }
            />
          </div>
        </CollapsibleContent>
      </Collapsible>
    </div>
  );
}
