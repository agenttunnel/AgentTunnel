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
      const loadOpenCodeModels = (binaryPath: string) =>
        openCodeRuntime
          .loadInventoryFromCli({
            binaryPath,
            cwd: serverConfig.cwd,
            environment: processEnvironment,
          })
          .pipe(Effect.map((inventory) => modelsFromInventory(inventory)));
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
          return piListedModelsToServerModels(rows);
        });
      const loadHarnessCatalog = (harness: string) =>
        (agentTunnelHarnessKind(harness) === "pi"
          ? loadPiModels(harness)
          : loadOpenCodeModels(harness)
        ).pipe(Effect.map((models) => ({ harness, models })));
      // A wedged or interactive harness CLI must never block the registry
      // build; the probe degrades to a readable reason instead of hanging.
      const probeTimeoutFor = (harness: string) =>
        agentTunnelHarnessKind(harness) === "pi" ? "8 seconds" : "4 seconds";
      const orchestratorHarness = config.orchestratorHarness;
      const plan = catalogHarnesses(config);
      const catalogs: HarnessCatalog[] = [];
      let probeFailed = false;
      let probeMessage: string | undefined;
      if (enabled) {
        const attempts = yield* Effect.forEach(
          plan,
          (harness) =>
            loadHarnessCatalog(harness).pipe(
              Effect.timeoutOption(probeTimeoutFor(harness)),
              Effect.exit,
            ),
          { concurrency: "unbounded" },
        );
        for (const [index, attempt] of attempts.entries()) {
          const harness = plan[index] ?? "";
          // Only the coordinator's catalog drives the snapshot state: a role
          // harness that cannot answer is a missing catalog, not a broken
          // provider.
          const fatal = harness === orchestratorHarness;
          if (attempt._tag === "Failure") {
            if (fatal) {
              probeFailed = true;
              probeMessage = Cause.pretty(attempt.cause);
            } else {
              yield* Effect.logDebug(`agenttunnel: no model catalog from ${harness}`, {
                cause: Cause.pretty(attempt.cause),
              });
            }
            continue;
          }
          if (Option.isNone(attempt.value)) {
            if (fatal) {
              probeFailed = true;
              probeMessage = `Timed out after ${probeTimeoutFor(harness)} reading models from the "${harness}" harness. Check that the harness CLI is installed and does not wait for input.`;
            }
            continue;
          }
          catalogs.push(attempt.value.value);
        }
      }
      const models = mergeHarnessCatalogs(catalogs, config.customModels);
      // Auto mode and text generation only receive the coordinator's models:
      // what another role's harness reports is not a model this one can run.
      const orchestratorModels = models
        .filter(
          (model) => model.harnesses === undefined || model.harnesses.includes(orchestratorHarness),
        )
        .map((model) => model.slug);
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
        availableModels: orchestratorModels,
        ...(environment ? { environment } : {}),
        ...(eventLoggers.native ? { nativeEventLogger: eventLoggers.native } : {}),
      });
      const textGeneration =
        agentTunnelHarnessKind(orchestratorHarness) === "pi"
          ? yield* makePiTextGeneration(
              decodePiSettings({
                enabled,
                binaryPath: orchestratorHarness,
                customModels: [...config.customModels],
              }),
              processEnvironment,
            )
          : yield* Effect.gen(function* () {
              const serverOwner = yield* OpenCodeServerOwner.make({
                binaryPath: orchestratorHarness,
                directory: serverConfig.cwd,
                environment: processEnvironment,
              });
              return yield* makeAgentTunnelTextGeneration(config, undefined, {
                availableModels: orchestratorModels,
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

/** One harness CLI's model catalog, as reported by its own listing probe. */
export interface HarnessCatalog {
  readonly harness: string;
  readonly models: ReadonlyArray<ServerProvider["models"][number]>;
}

/**
 * The harness presets this driver can list models for. Other CLIs keep the
 * generic `run` invocation and expose no catalog, so the model config shows
 * them through their own provider instance instead.
 */
const ENUMERABLE_HARNESS_PRESETS: ReadonlyArray<string> = ["opencode", "pi"];

/**
 * Harness CLIs whose catalogs the snapshot should carry, coordinator first.
 *
 * Configured role harnesses come first so the coordinator's models stay at the
 * top of the list; the enumerable presets follow, so switching a role (or a
 * thread's own model config) to `pi`/`opencode` still finds a catalog even when
 * the environment's role harnesses all point at one CLI. Harnesses with no
 * listing command are left to their own provider instance — except the
 * coordinator, whose probe failure is what marks the provider as limited.
 */
export function catalogHarnesses(config: AgentTunnelSettings): ReadonlyArray<string> {
  const harnesses: string[] = [];
  const add = (value: string) => {
    const harness = value.trim();
    if (harness.length === 0 || harnesses.includes(harness)) return;
    harnesses.push(harness);
  };
  add(config.orchestratorHarness);
  add(config.plannerHarness);
  add(config.executorHarness);
  add(config.testerHarness);
  add(config.reviewerHarness);
  for (const preset of ENUMERABLE_HARNESS_PRESETS) add(preset);
  return harnesses.filter(
    (harness) =>
      agentTunnelHarnessKind(harness) !== "other" || harness === config.orchestratorHarness,
  );
}

/**
 * Flatten per-harness catalogs into one model list. A slug two harnesses both
 * report stays a single row tagged with both, so the picker can filter by
 * harness without rendering duplicates. Custom models carry no harness: the
 * user typed them for the workflow, not for one CLI.
 */
export function mergeHarnessCatalogs(
  catalogs: ReadonlyArray<HarnessCatalog>,
  customModels: ReadonlyArray<string>,
): ReadonlyArray<ServerProvider["models"][number]> {
  const models: Array<ServerProvider["models"][number]> = [];
  const indexBySlug = new Map<string, number>();
  for (const catalog of catalogs) {
    for (const model of catalog.models) {
      const index = indexBySlug.get(model.slug);
      if (index === undefined) {
        indexBySlug.set(model.slug, models.length);
        models.push({ ...model, harnesses: [catalog.harness] });
        continue;
      }
      const current = models[index];
      if (!current || current.harnesses?.includes(catalog.harness)) continue;
      models[index] = { ...current, harnesses: [...(current.harnesses ?? []), catalog.harness] };
    }
  }
  for (const candidate of customModels) {
    const slug = candidate.trim();
    if (slug.length === 0 || indexBySlug.has(slug)) continue;
    indexBySlug.set(slug, models.length);
    models.push({ slug, name: slug, isCustom: true, capabilities: null });
  }
  return models;
}

function modelsFromInventory(
  inventory: OpenCodeInventoryLike | undefined,
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
