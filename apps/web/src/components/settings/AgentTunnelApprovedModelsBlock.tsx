"use client";

import { useMemo, useState } from "react";
import { SearchIcon } from "lucide-react";
import { cn } from "~/lib/utils";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Switch } from "../ui/switch";
import {
  applyAgentTunnelApprovedModelToggle,
  applyAgentTunnelApprovedModelsDisableAll,
  applyAgentTunnelApprovedModelsEnableAll,
  readAgentTunnelApprovedModels,
} from "@t3tools/client-runtime/state/agentTunnelRoles";

interface HarnessTaggedServerModel {
  readonly slug: string;
  readonly name: string;
  readonly harnesses?: ReadonlyArray<string> | undefined;
}

/**
 * Curates the models auto mode may dispatch. Eligible models come from the
 * orchestrator harness only — the same rule the driver applies — because a
 * model another harness reported is one auto mode can never run. The list is
 * bounded, scrollable, and searchable, so a 200+ model catalog stays cheap
 * to render and quick to narrow.
 */
export function AgentTunnelApprovedModelsBlock({
  config,
  models,
  orchestratorHarness,
  onChange,
  className,
}: {
  readonly config: Record<string, unknown> | undefined;
  readonly models: ReadonlyArray<HarnessTaggedServerModel>;
  readonly orchestratorHarness: string;
  readonly onChange: (nextConfig: Record<string, unknown> | undefined) => void;
  readonly className?: string;
}) {
  const [search, setSearch] = useState("");
  const eligibleModels = useMemo(
    () =>
      models.filter(
        (entry) => entry.harnesses === undefined || entry.harnesses.includes(orchestratorHarness),
      ),
    [models, orchestratorHarness],
  );
  const allSlugs = useMemo(() => eligibleModels.map((model) => model.slug), [eligibleModels]);
  const approvedSet = useMemo(() => {
    const approved = readAgentTunnelApprovedModels(config);
    return approved === null ? null : new Set(approved);
  }, [config]);
  const isApproved = (slug: string) => approvedSet === null || approvedSet.has(slug);
  const approvedCount = eligibleModels.reduce(
    (count, model) => count + (isApproved(model.slug) ? 1 : 0),
    0,
  );
  // Mirror `writeConfigValue`: an emptied config is handed back as undefined.
  const commit = (next: Record<string, unknown>) => {
    onChange(Object.keys(next).length > 0 ? next : undefined);
  };

  const normalizedSearch = search.trim().toLowerCase();
  const visibleModels = useMemo(
    () =>
      normalizedSearch.length === 0
        ? eligibleModels
        : eligibleModels.filter(
            (model) =>
              model.name.toLowerCase().includes(normalizedSearch) ||
              model.slug.toLowerCase().includes(normalizedSearch),
          ),
    [eligibleModels, normalizedSearch],
  );

  return (
    <div
      className={cn(
        "divide-y divide-border/50 overflow-hidden rounded-lg border border-border/60",
        className,
      )}
    >
      <div className="flex items-center justify-between gap-3 px-3 py-2.5">
        <span className="min-w-0">
          <span className="block text-[.8125rem] font-medium leading-5 text-foreground">
            Approved models
          </span>
          <span className="block text-[.8125rem] leading-5 text-muted-foreground">
            Auto mode may only dispatch these models ({approvedCount} of {eligibleModels.length}).
          </span>
        </span>
        <span className="flex shrink-0 items-center gap-1.5">
          <Button
            size="compact"
            variant="outline"
            onClick={() => commit(applyAgentTunnelApprovedModelsEnableAll(config ?? {}))}
          >
            Enable all
          </Button>
          <Button
            size="compact"
            variant="outline"
            onClick={() => commit(applyAgentTunnelApprovedModelsDisableAll(config ?? {}))}
          >
            Disable all
          </Button>
        </span>
      </div>
      <div className="px-3 py-2">
        <div className="relative">
          <SearchIcon className="pointer-events-none absolute start-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground opacity-70" />
          <Input
            className="h-7 ps-7 text-xs"
            placeholder={search ? undefined : "Search models…"}
            type="search"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            aria-label="Search approved models"
          />
        </div>
      </div>
      <div className="max-h-52 overflow-y-auto px-3 py-1">
        {eligibleModels.length === 0 ? (
          <p className="py-2 text-xs text-muted-foreground">No models reported for this harness.</p>
        ) : visibleModels.length === 0 ? (
          <p className="py-2 text-xs text-muted-foreground">No models match this search.</p>
        ) : (
          visibleModels.map((model) => (
            <label
              key={model.slug}
              className="flex min-h-7 items-center justify-between gap-3 py-1"
            >
              <span className="min-w-0">
                <span className="block truncate text-xs text-foreground/90">{model.name}</span>
                <span className="block truncate text-[.625rem] text-muted-foreground">
                  {model.slug}
                </span>
              </span>
              <Switch
                checked={isApproved(model.slug)}
                onCheckedChange={(checked) =>
                  commit(
                    applyAgentTunnelApprovedModelToggle(
                      config ?? {},
                      model.slug,
                      checked,
                      allSlugs,
                    ),
                  )
                }
                aria-label={`Approve ${model.name}`}
              />
            </label>
          ))
        )}
      </div>
    </div>
  );
}
