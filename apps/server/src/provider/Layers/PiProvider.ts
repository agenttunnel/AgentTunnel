import {
  type ModelCapabilities,
  type PiSettings,
  type ServerProviderModel,
} from "@t3tools/contracts";
import { createModelCapabilities } from "@t3tools/shared/model";
import { resolveSpawnCommand } from "@t3tools/shared/shell";
import * as DateTime from "effect/DateTime";
import * as Effect from "effect/Effect";
import * as Option from "effect/Option";
import * as Result from "effect/Result";
import { ChildProcess, ChildProcessSpawner } from "effect/unstable/process";

import {
  buildServerProvider,
  isCommandMissingCause,
  parseGenericCliVersion,
  providerModelsFromSettings,
  spawnAndCollect,
  type ServerProviderDraft,
} from "../providerSnapshot.ts";

const PI_PRESENTATION = {
  displayName: "Pi",
  showInteractionModeToggle: false,
} as const;

const EMPTY_CAPABILITIES: ModelCapabilities = createModelCapabilities({
  optionDescriptors: [],
});

const VERSION_PROBE_TIMEOUT_MS = 4_000;
const MODEL_LIST_PROBE_TIMEOUT_MS = 10_000;

export interface PiListedModel {
  readonly provider: string;
  readonly model: string;
  readonly contextWindowTokens: number | undefined;
  readonly maxOutputTokens: number | undefined;
  readonly thinking: boolean;
  readonly images: boolean;
}

/**
 * Convert pi's abbreviated token counts (`1.0M`, `65.5K`, `384K`, `1M`) into
 * integers. Returns `undefined` for unparseable cells so a malformed table row
 * degrades to "unknown" rather than a bogus zero.
 */
export function parsePiTokenCount(value: string): number | undefined {
  const match = value.trim().match(/^(\d+(?:\.\d+)?)\s*([KMB])?$/i);
  if (match === null) {
    return undefined;
  }
  const amount = Number(match[1]);
  if (!Number.isFinite(amount)) {
    return undefined;
  }
  const suffix = (match[2] ?? "").toUpperCase();
  const multiplier =
    suffix === "K" ? 1_000 : suffix === "M" ? 1_000_000 : suffix === "B" ? 1_000_000_000 : 1;
  return Math.round(amount * multiplier);
}

/**
 * pi added the `agent_settled` event in 0.85.0. It marks the point where all
 * queued continuations have drained, unlike `agent_end`, which can fire
 * between continuation passes. Older pi never emits it, so the adapter must
 * fall back to `agent_end` to settle a turn.
 */
export function piSupportsAgentSettled(version: string | null | undefined): boolean {
  if (version === null || version === undefined) return false;
  const match = version.match(/^(\d+)\.(\d+)\.(\d+)/);
  if (match === null) return false;
  const [major, minor, patch] = [Number(match[1]), Number(match[2]), Number(match[3])];
  if (major !== 0) return major > 0;
  if (minor !== 85) return minor > 85;
  return patch >= 0;
}

function parsePiBoolean(value: string): boolean | undefined {
  const normalized = value.trim().toLowerCase();
  if (normalized === "yes" || normalized === "true" || normalized === "y") {
    return true;
  }
  if (normalized === "no" || normalized === "false" || normalized === "n") {
    return false;
  }
  return undefined;
}

/**
 * Parse the fixed-width table emitted by `pi --list-models`. The header row and
 * separator lines are skipped; every other row must yield the six documented
 * columns (provider, model, context, max-out, thinking, images) with both token
 * counts parseable and both flags recognized booleans. Lines that only happen to
 * split into six whitespace tokens, such as the "No models available..." notice,
 * are dropped rather than interpreted as a model.
 */
export function parsePiModelListTable(output: string): ReadonlyArray<PiListedModel> {
  const rows: Array<PiListedModel> = [];
  for (const rawLine of output.split("\n")) {
    const line = rawLine.trim();
    if (line.length === 0) {
      continue;
    }
    const columns = line.split(/\s+/);
    if (columns.length < 6) {
      continue;
    }
    const [provider, model, context, maxOut, thinking, images] = columns;
    if (provider === "provider" && model === "model") {
      continue;
    }
    if (
      provider === undefined ||
      model === undefined ||
      context === undefined ||
      maxOut === undefined ||
      thinking === undefined ||
      images === undefined
    ) {
      continue;
    }
    const contextWindowTokens = parsePiTokenCount(context);
    const maxOutputTokens = parsePiTokenCount(maxOut);
    const thinkingFlag = parsePiBoolean(thinking);
    const imagesFlag = parsePiBoolean(images);
    if (
      contextWindowTokens === undefined ||
      maxOutputTokens === undefined ||
      thinkingFlag === undefined ||
      imagesFlag === undefined
    ) {
      continue;
    }
    rows.push({
      provider,
      model,
      contextWindowTokens,
      maxOutputTokens,
      thinking: thinkingFlag,
      images: imagesFlag,
    });
  }
  return rows;
}

/**
 * Drop duplicate provider/model rows. `pi --list-models` is written to stderr
 * when spawned with piped stdio, but a future version could echo it to stdout
 * as well; parsing the combined text must not double-count models.
 */
export function dedupePiListedModels(
  rows: ReadonlyArray<PiListedModel>,
): ReadonlyArray<PiListedModel> {
  const seen = new Set<string>();
  const deduped: Array<PiListedModel> = [];
  for (const row of rows) {
    const key = `${row.provider}/${row.model}`;
    if (seen.has(key)) {
      continue;
    }
    seen.add(key);
    deduped.push(row);
  }
  return deduped;
}

export function piListedModelsToServerModels(
  rows: ReadonlyArray<PiListedModel>,
): ReadonlyArray<ServerProviderModel> {
  return rows.map((row) => ({
    slug: `${row.provider}/${row.model}`,
    name: row.model,
    subProvider: row.provider,
    isCustom: false,
    capabilities: EMPTY_CAPABILITIES,
  }));
}

function piModelsFromSettings(
  customModels: ReadonlyArray<string> | undefined,
  builtInModels: ReadonlyArray<ServerProviderModel> = [],
): ReadonlyArray<ServerProviderModel> {
  return providerModelsFromSettings(builtInModels, customModels ?? [], EMPTY_CAPABILITIES);
}

export const makePendingPiProvider = (piSettings: PiSettings): Effect.Effect<ServerProviderDraft> =>
  Effect.gen(function* () {
    const checkedAt = DateTime.formatIso(yield* DateTime.now);
    const models = piModelsFromSettings(piSettings.customModels);

    return buildServerProvider({
      presentation: PI_PRESENTATION,
      enabled: piSettings.enabled,
      checkedAt,
      models,
      probe: {
        installed: false,
        version: null,
        status: "warning",
        auth: { status: "unknown" },
        message: piSettings.enabled
          ? "Pi provider status has not been checked in this session yet."
          : "Pi is disabled in AgentTunnel settings.",
      },
    });
  });

const runPiCommand = (
  piSettings: PiSettings,
  args: ReadonlyArray<string>,
  environment: NodeJS.ProcessEnv,
) =>
  Effect.gen(function* () {
    const command = piSettings.binaryPath || "pi";
    const spawnCommand = yield* resolveSpawnCommand(command, args, { env: environment });
    return yield* spawnAndCollect(
      command,
      ChildProcess.make(spawnCommand.command, spawnCommand.args, {
        env: environment,
        shell: spawnCommand.shell,
      }),
    );
  });

/**
 * Best-effort `pi --version` probe for callers that only need the version
 * string (e.g. feature gating). Never fails: a missing binary or a timeout
 * yields `null`, which callers treat as "version unknown".
 */
export const probePiVersion = (
  piSettings: PiSettings,
  environment: NodeJS.ProcessEnv = process.env,
): Effect.Effect<string | null, never, ChildProcessSpawner.ChildProcessSpawner> =>
  runPiCommand(piSettings, ["--version"], environment).pipe(
    Effect.timeoutOption(VERSION_PROBE_TIMEOUT_MS),
    Effect.result,
    Effect.map((result) =>
      Result.isFailure(result) || Option.isNone(result.success)
        ? null
        : parseGenericCliVersion(`${result.success.value.stdout}\n${result.success.value.stderr}`),
    ),
    Effect.catchCause(() => Effect.succeed(null)),
  );

export const checkPiProviderStatus = Effect.fn("checkPiProviderStatus")(function* (
  piSettings: PiSettings,
  cwd: string,
  environment: NodeJS.ProcessEnv = process.env,
): Effect.fn.Return<ServerProviderDraft, never, ChildProcessSpawner.ChildProcessSpawner> {
  const checkedAt = DateTime.formatIso(yield* DateTime.now);
  const customModels = piSettings.customModels;
  const fallbackModels = piModelsFromSettings(customModels);

  if (!piSettings.enabled) {
    return buildServerProvider({
      presentation: PI_PRESENTATION,
      enabled: false,
      checkedAt,
      models: fallbackModels,
      probe: {
        installed: false,
        version: null,
        status: "warning",
        auth: { status: "unknown" },
        message: "Pi is disabled in AgentTunnel settings.",
      },
    });
  }

  const versionResult = yield* runPiCommand(piSettings, ["--version"], environment).pipe(
    Effect.timeoutOption(VERSION_PROBE_TIMEOUT_MS),
    Effect.result,
  );

  if (Result.isFailure(versionResult)) {
    const error = versionResult.failure;
    yield* Effect.logWarning("Pi CLI health check failed.", { errorTag: error._tag });
    return buildServerProvider({
      presentation: PI_PRESENTATION,
      enabled: true,
      checkedAt,
      models: fallbackModels,
      probe: {
        installed: !isCommandMissingCause(error),
        version: null,
        status: "error",
        auth: { status: "unknown" },
        message: isCommandMissingCause(error)
          ? "Pi CLI (`pi`) is not installed or not on PATH."
          : "Failed to execute Pi CLI health check.",
      },
    });
  }

  if (Option.isNone(versionResult.success)) {
    return buildServerProvider({
      presentation: PI_PRESENTATION,
      enabled: true,
      checkedAt,
      models: fallbackModels,
      probe: {
        installed: true,
        version: null,
        status: "error",
        auth: { status: "unknown" },
        message: "Pi CLI is installed but timed out while running `pi --version`.",
      },
    });
  }

  const versionOutput = versionResult.success.value;
  const version = parseGenericCliVersion(`${versionOutput.stdout}\n${versionOutput.stderr}`);
  if (versionOutput.code !== 0) {
    yield* Effect.logWarning("Pi CLI version probe exited with a non-zero status.", {
      exitCode: versionOutput.code,
    });
    return buildServerProvider({
      presentation: PI_PRESENTATION,
      enabled: true,
      checkedAt,
      models: fallbackModels,
      probe: {
        installed: true,
        version,
        status: "error",
        auth: { status: "unknown" },
        message: "Pi CLI is installed but failed to run.",
      },
    });
  }

  const modelsResult = yield* runPiCommand(piSettings, ["--list-models"], environment).pipe(
    Effect.timeoutOption(MODEL_LIST_PROBE_TIMEOUT_MS),
    Effect.result,
  );

  if (Result.isFailure(modelsResult)) {
    const error = modelsResult.failure;
    yield* Effect.logWarning("Pi model list probe failed.", { errorTag: error._tag });
    return buildServerProvider({
      presentation: PI_PRESENTATION,
      enabled: true,
      checkedAt,
      models: fallbackModels,
      probe: {
        installed: true,
        version,
        status: "error",
        auth: { status: "unknown" },
        message: "Pi CLI is installed but failed to list models.",
      },
    });
  }

  if (Option.isNone(modelsResult.success)) {
    return buildServerProvider({
      presentation: PI_PRESENTATION,
      enabled: true,
      checkedAt,
      models: fallbackModels,
      probe: {
        installed: true,
        version,
        status: "error",
        auth: { status: "unknown" },
        message: "Pi CLI is installed but timed out while running `pi --list-models`.",
      },
    });
  }

  const modelsOutput = modelsResult.success.value;
  if (modelsOutput.code !== 0) {
    yield* Effect.logWarning("Pi model list probe exited with a non-zero status.", {
      exitCode: modelsOutput.code,
    });
    return buildServerProvider({
      presentation: PI_PRESENTATION,
      enabled: true,
      checkedAt,
      models: fallbackModels,
      probe: {
        installed: true,
        version,
        status: "error",
        auth: { status: "unknown" },
        message: "Pi CLI is installed but failed to list models.",
      },
    });
  }
  // pi v0.78.0 writes the table to stderr when spawned with piped stdio, so
  // parse both streams (mirroring the `--version` probe) and drop any row a
  // version that also prints to stdout would duplicate.
  const listedModels = dedupePiListedModels(
    parsePiModelListTable(`${modelsOutput.stdout}\n${modelsOutput.stderr}`),
  );
  const models = piModelsFromSettings(customModels, piListedModelsToServerModels(listedModels));
  const hasModels = listedModels.length > 0;

  yield* Effect.annotateCurrentSpan({ cwd, modelCount: listedModels.length });

  return buildServerProvider({
    presentation: PI_PRESENTATION,
    enabled: true,
    checkedAt,
    models,
    probe: {
      installed: true,
      version,
      status: hasModels ? "ready" : "warning",
      auth: { status: "unknown" },
      message: hasModels
        ? `${listedModels.length} model${listedModels.length === 1 ? "" : "s"} available from Pi.`
        : "Pi is installed but reported no models. Run `pi --list-models` to verify your configuration.",
    },
  });
});
