import {
  AgentTunnelSettings,
  PiSettings,
  ProviderDriverKind,
  type ServerProvider,
} from "@t3tools/contracts";
import * as Cause from "effect/Cause";
import * as Crypto from "effect/Crypto";
import * as DateTime from "effect/DateTime";
import * as Effect from "effect/Effect";
import * as FileSystem from "effect/FileSystem";
import * as Option from "effect/Option";
import * as Path from "effect/Path";
import * as Schema from "effect/Schema";
import * as Stream from "effect/Stream";
import { ChildProcessSpawner } from "effect/unstable/process";

import { ServerConfig } from "../../config.ts";
import { makeAgentTunnelTextGeneration } from "../../textGeneration/AgentTunnelTextGeneration.ts";
import { makePiTextGeneration } from "../../textGeneration/PiTextGeneration.ts";
import { agentTunnelHarnessKind } from "../agentTunnel/opencodeTransport.ts";
import { ProviderDriverError } from "../Errors.ts";
import { makeAgentTunnelAdapter } from "../Layers/AgentTunnelAdapter.ts";
import {
  dedupePiListedModels,
  parsePiModelListTable,
  piListedModelsToServerModels,
} from "../Layers/PiProvider.ts";
import { ProviderEventLoggers } from "../Layers/ProviderEventLoggers.ts";
import { OpenCodeRuntime, OpenCodeRuntimeError } from "../opencodeRuntime.ts";
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
  | FileSystem.FileSystem
  | OpenCodeRuntime
  | Path.Path
  | ProviderEventLoggers
  | ServerConfig;

const decodeSettings = Schema.decodeSync(AgentTunnelSettings);
const decodePiSettings = Schema.decodeSync(PiSettings);

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
      const harnessKind = agentTunnelHarnessKind(config.orchestratorHarness);
      const loadOpenCodeModels = (binaryPath: string) =>
        openCodeRuntime
          .loadInventoryFromCli({
            binaryPath,
            cwd: serverConfig.cwd,
            environment: processEnvironment,
          })
          .pipe(Effect.map((inventory) => modelsFromInventory(inventory, config.customModels)));
      const loadPiModels = (binaryPath: string) =>
        Effect.gen(function* () {
          const result = yield* openCodeRuntime.runOpenCodeCommand({
            binaryPath,
            args: ["--list-models"],
            cwd: serverConfig.cwd,
            environment: processEnvironment,
          });
          const rows = dedupePiListedModels(
            parsePiModelListTable(`${result.stdout}\n${result.stderr}`),
          );
          if (rows.length === 0 && result.code !== 0) {
            return yield* new OpenCodeRuntimeError({
              operation: "loadPiModels",
              detail: `\`${binaryPath} --list-models\` exited with code ${result.code}.`,
            });
          }
          return mergeProviderModels(piListedModelsToServerModels(rows), config.customModels);
        });
      // A wedged or interactive harness CLI must never block the registry
      // build; the probe degrades to a warning snapshot with a readable reason.
      const probeTimeout = harnessKind === "pi" ? "8 seconds" : "4 seconds";
      let models = customModelsToServerModels(config.customModels);
      let probeFailed = false;
      let probeMessage: string | undefined;
      if (enabled) {
        const attempt = yield* Effect.exit(
          (harnessKind === "pi"
            ? loadPiModels(config.orchestratorHarness)
            : loadOpenCodeModels(config.orchestratorHarness)
          ).pipe(Effect.timeoutOption(probeTimeout)),
        );
        if (attempt._tag === "Failure") {
          probeFailed = true;
          probeMessage = Cause.pretty(attempt.cause);
        } else if (Option.isNone(attempt.value)) {
          probeFailed = true;
          probeMessage = `Timed out after ${probeTimeout} reading models from the "${config.orchestratorHarness}" harness. Check that the harness CLI is installed and does not wait for input.`;
        } else {
          models = attempt.value.value;
        }
      }
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
        installed: enabled && !probeFailed,
        version: null,
        status: enabled ? (probeFailed ? "warning" : "ready") : "disabled",
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
        availableModels: models.map((model) => model.slug),
        ...(environment ? { environment } : {}),
        ...(eventLoggers.native ? { nativeEventLogger: eventLoggers.native } : {}),
      });
      const textGeneration =
        harnessKind === "pi"
          ? yield* makePiTextGeneration(
              decodePiSettings({
                enabled,
                binaryPath: config.orchestratorHarness,
                customModels: [...config.customModels],
              }),
              processEnvironment,
            )
          : yield* Effect.gen(function* () {
              const serverOwner = yield* OpenCodeServerOwner.make({
                binaryPath: config.orchestratorHarness,
                directory: serverConfig.cwd,
                environment: processEnvironment,
              });
              return yield* makeAgentTunnelTextGeneration(config, undefined, {
                availableModels: models.map((model) => model.slug),
              }).pipe(Effect.provideService(OpenCodeServerOwner.OpenCodeServerOwner, serverOwner));
            });
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

function customModelsToServerModels(
  customModels: ReadonlyArray<string>,
): ReadonlyArray<ServerProvider["models"][number]> {
  return mergeProviderModels([], customModels);
}

function mergeProviderModels(
  listed: ReadonlyArray<ServerProvider["models"][number]>,
  customModels: ReadonlyArray<string>,
): ReadonlyArray<ServerProvider["models"][number]> {
  const models = [...listed];
  const seen = new Set(models.map((model) => model.slug));
  for (const candidate of customModels) {
    const slug = candidate.trim();
    if (slug.length === 0 || seen.has(slug)) continue;
    seen.add(slug);
    models.push({ slug, name: slug, isCustom: true, capabilities: null });
  }
  return models;
}

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
