import { AgentTunnelSettings, ProviderDriverKind, type ServerProvider } from "@t3tools/contracts";
import * as Cause from "effect/Cause";
import * as Crypto from "effect/Crypto";
import * as DateTime from "effect/DateTime";
import * as Effect from "effect/Effect";
import * as Schema from "effect/Schema";
import * as Stream from "effect/Stream";
import { ChildProcessSpawner } from "effect/unstable/process";

import { ServerConfig } from "../../config.ts";
import { makeAgentTunnelTextGeneration } from "../../textGeneration/AgentTunnelTextGeneration.ts";
import { ProviderDriverError } from "../Errors.ts";
import { makeAgentTunnelAdapter } from "../Layers/AgentTunnelAdapter.ts";
import { ProviderEventLoggers } from "../Layers/ProviderEventLoggers.ts";
import { OpenCodeRuntime } from "../opencodeRuntime.ts";
import * as OpenCodeServerOwner from "../OpenCodeServerOwner.ts";
import {
  defaultProviderContinuationIdentity,
  type ProviderDriver,
  type ProviderInstance,
} from "../ProviderDriver.ts";
import { mergeProviderInstanceEnvironment } from "../ProviderInstanceEnvironment.ts";
import {
  makeManualOnlyProviderMaintenanceCapabilities,
  type ProviderMaintenanceCapabilities,
} from "../providerMaintenance.ts";
import type { ServerProviderShape } from "../Services/ServerProvider.ts";

const DRIVER_KIND = ProviderDriverKind.make("agenttunnel");

export type AgentTunnelDriverEnv =
  | ChildProcessSpawner.ChildProcessSpawner
  | Crypto.Crypto
  | OpenCodeRuntime
  | ProviderEventLoggers
  | ServerConfig
  | OpenCodeServerOwner.OpenCodeServerOwner;

const decodeSettings = Schema.decodeSync(AgentTunnelSettings);

const maintenanceCapabilities: ProviderMaintenanceCapabilities =
  makeManualOnlyProviderMaintenanceCapabilities({
    provider: DRIVER_KIND,
    packageName: null,
  });

export const AgentTunnelDriver: ProviderDriver<AgentTunnelSettings, AgentTunnelDriverEnv> = {
  driverKind: DRIVER_KIND,
  metadata: {
    displayName: "AgentTunnel",
    supportsMultipleInstances: true,
  },
  configSchema: AgentTunnelSettings,
  defaultConfig: () => decodeSettings({}),
  create: ({ instanceId, displayName, accentColor, environment, enabled, config }) =>
    Effect.gen(function* () {
      const openCodeRuntime = yield* OpenCodeRuntime;
      const eventLoggers = yield* ProviderEventLoggers;
      const serverConfig = yield* ServerConfig;
      const processEnvironment = mergeProviderInstanceEnvironment(environment);
      const checkedAt = yield* Effect.map(DateTime.now, DateTime.formatIso);
      const inventoryExit = enabled
        ? yield* Effect.exit(
            openCodeRuntime
              .loadInventoryFromCli({
                binaryPath: config.orchestratorHarness,
                cwd: serverConfig.cwd,
                environment: processEnvironment,
              })
              // A wedged or interactive harness CLI must never block the
              // registry build; degrade to a warning snapshot instead.
              .pipe(Effect.timeout("4 seconds")),
          )
        : undefined;
      const inventory = inventoryExit?._tag === "Success" ? inventoryExit.value : undefined;
      const probeMessage =
        inventoryExit?._tag === "Failure" ? Cause.pretty(inventoryExit.cause) : undefined;
      const models = modelsFromInventory(inventory, config.customModels);
      const snapshot: ServerProvider = {
        instanceId,
        driver: DRIVER_KIND,
        ...(displayName ? { displayName } : { displayName: "AgentTunnel" }),
        ...(accentColor ? { accentColor } : {}),
        badgeLabel: "Workflow",
        continuation: {
          groupKey: defaultProviderContinuationIdentity({
            driverKind: DRIVER_KIND,
            instanceId,
          }).continuationKey,
        },
        showInteractionModeToggle: false,
        enabled,
        installed: enabled && inventory !== undefined,
        version: null,
        status: enabled ? (inventory ? "ready" : "warning") : "disabled",
        auth: {
          status: "unknown",
          type: "AgentTunnel",
          ...(probeMessage ? { label: probeMessage } : {}),
        },
        checkedAt,
        ...(probeMessage ? { message: probeMessage } : {}),
        availability: "available",
        models,
        slashCommands: [],
        skills: [],
      };
      const serverProvider: ServerProviderShape = {
        maintenanceCapabilities,
        getSnapshot: Effect.succeed(snapshot),
        refresh: Effect.succeed(snapshot),
        streamChanges: Stream.empty,
      };
      const adapter = yield* makeAgentTunnelAdapter(config, {
        instanceId,
        ...(environment ? { environment } : {}),
        ...(eventLoggers.native ? { nativeEventLogger: eventLoggers.native } : {}),
      });
      const serverOwner = yield* OpenCodeServerOwner.make({
        binaryPath: config.orchestratorHarness,
        directory: serverConfig.cwd,
        environment: processEnvironment,
      });
      const textGeneration = yield* makeAgentTunnelTextGeneration(config, undefined, {
        availableModels: models.map((model) => model.slug),
      }).pipe(Effect.provideService(OpenCodeServerOwner.OpenCodeServerOwner, serverOwner));
      const continuationIdentity = defaultProviderContinuationIdentity({
        driverKind: DRIVER_KIND,
        instanceId,
      });

      return {
        instanceId,
        driverKind: DRIVER_KIND,
        continuationIdentity,
        displayName,
        accentColor,
        enabled,
        snapshot: serverProvider,
        adapter,
        textGeneration,
      } satisfies ProviderInstance;
    }).pipe(
      Effect.mapError(
        (cause) =>
          new ProviderDriverError({
            driver: DRIVER_KIND,
            instanceId,
            detail: String(cause),
            cause,
          }),
      ),
    ),
};

function modelsFromInventory(
  inventory: OpenCodeInventoryLike | undefined,
  customModels: ReadonlyArray<string>,
): ReadonlyArray<ServerProvider["models"][number]> {
  const models: Array<ServerProvider["models"][number]> = [];
  const seen = new Set<string>();
  const connected = new Set(inventory?.providerList.connected ?? []);

  for (const provider of inventory?.providerList.all ?? []) {
    if (!connected.has(provider.id)) continue;
    for (const [modelId, model] of Object.entries(provider.models)) {
      const slug = `${provider.id}/${modelId}`;
      if (seen.has(slug)) continue;
      seen.add(slug);
      const subProvider =
        typeof provider.name === "string" && provider.name.trim().length > 0
          ? provider.name.trim()
          : provider.id;
      models.push({
        slug,
        name: typeof model.name === "string" && model.name.trim().length > 0 ? model.name : slug,
        subProvider,
        isCustom: false,
        capabilities: null,
      });
    }
  }

  for (const candidate of customModels) {
    const slug = candidate.trim();
    if (slug.length === 0 || seen.has(slug)) continue;
    seen.add(slug);
    models.push({ slug, name: slug, isCustom: true, capabilities: null });
  }

  return models;
}

interface OpenCodeInventoryLike {
  readonly providerList: {
    readonly connected: ReadonlyArray<string>;
    readonly all: ReadonlyArray<{
      readonly id: string;
      readonly name?: string;
      readonly models: Record<string, { readonly name?: string }>;
    }>;
  };
}
