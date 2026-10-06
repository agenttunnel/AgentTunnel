import {
  type CommandCodeSettings,
  type ModelCapabilities,
  type ServerProvider,
  type ServerProviderAuthStatus,
  type ServerProviderModel,
} from "@t3tools/contracts";
import { causeErrorTag } from "@t3tools/shared/observability";
import { createModelCapabilities } from "@t3tools/shared/model";
import { resolveSpawnCommand } from "@t3tools/shared/shell";
import * as Crypto from "effect/Crypto";
import * as DateTime from "effect/DateTime";
import * as Effect from "effect/Effect";
import * as Exit from "effect/Exit";
import * as Option from "effect/Option";
import * as Result from "effect/Result";
import { HttpClient } from "effect/unstable/http";
import { ChildProcess, ChildProcessSpawner } from "effect/unstable/process";
import type * as EffectAcpSchema from "effect-acp/schema";

import {
  buildServerProvider,
  isCommandMissingCause,
  parseGenericCliVersion,
  providerModelsFromSettings,
  spawnAndCollect,
  type ServerProviderDraft,
} from "../providerSnapshot.ts";
import {
  enrichProviderSnapshotWithVersionAdvisory,
  type ProviderMaintenanceCapabilities,
} from "../providerMaintenance.ts";
import {
  COMMAND_CODE_DEFAULT_MODEL,
  makeCommandCodeAcpRuntime,
} from "../acp/CommandCodeAcpSupport.ts";

const COMMAND_CODE_PRESENTATION = {
  displayName: "Command Code",
  showInteractionModeToggle: true,
} as const;
const EMPTY_CAPABILITIES: ModelCapabilities = createModelCapabilities({
  optionDescriptors: [],
});

const VERSION_PROBE_TIMEOUT_MS = 4_000;
const AUTH_PROBE_TIMEOUT_MS = 10_000;
const COMMAND_CODE_ACP_MODEL_DISCOVERY_TIMEOUT_MS = 15_000;

const COMMAND_CODE_BUILT_IN_MODELS: ReadonlyArray<ServerProviderModel> = [
  {
    slug: COMMAND_CODE_DEFAULT_MODEL,
    name: "DeepSeek V4 Flash",
    isCustom: false,
    isDefault: true,
    capabilities: EMPTY_CAPABILITIES,
  },
];

export function buildInitialCommandCodeProviderSnapshot(
  commandCodeSettings: CommandCodeSettings,
): Effect.Effect<ServerProviderDraft> {
  return Effect.gen(function* () {
    const checkedAt = yield* Effect.map(DateTime.now, DateTime.formatIso);
    const models = commandCodeModelsFromSettings(commandCodeSettings.customModels);

    if (!commandCodeSettings.enabled) {
      return buildServerProvider({
        presentation: COMMAND_CODE_PRESENTATION,
        enabled: false,
        checkedAt,
        models,
        probe: {
          installed: false,
          version: null,
          status: "warning",
          auth: { status: "unknown" },
          message: "Command Code is disabled in AgentTunnel settings.",
        },
      });
    }

    return buildServerProvider({
      presentation: COMMAND_CODE_PRESENTATION,
      enabled: true,
      checkedAt,
      models,
      probe: {
        installed: true,
        version: null,
        status: "warning",
        auth: { status: "unknown" },
        message: "Checking Command Code CLI availability...",
      },
    });
  });
}

function commandCodeModelsFromSettings(
  customModels: ReadonlyArray<string> | undefined,
  builtInModels: ReadonlyArray<ServerProviderModel> = COMMAND_CODE_BUILT_IN_MODELS,
): ReadonlyArray<ServerProviderModel> {
  return providerModelsFromSettings(builtInModels, customModels ?? [], EMPTY_CAPABILITIES);
}

export function buildCommandCodeModelsFromSessionModelState(
  modelState: EffectAcpSchema.SessionModelState | null | undefined,
): ReadonlyArray<ServerProviderModel> {
  if (!modelState || modelState.availableModels.length === 0) {
    return [];
  }
  const seen = new Set<string>();
  const models: Array<ServerProviderModel> = [];
  for (const model of modelState.availableModels) {
    const slug = model.modelId.trim();
    if (slug.length === 0 || seen.has(slug)) {
      continue;
    }
    seen.add(slug);
    models.push({
      slug,
      name: model.name.trim() || slug,
      isCustom: false,
      isDefault: model.modelId === modelState.currentModelId,
      capabilities: EMPTY_CAPABILITIES,
    });
  }
  return models;
}

/**
 * Parse the single JSON line emitted by `command-code status --json`. The
 * command exits non-zero when unauthenticated but still writes the JSON, so
 * callers parse stdout independently of the exit code. Returns `undefined` for
 * anything that is not a recognisable status document.
 */
export function parseCommandCodeAuthStatusJson(
  raw: string,
): { readonly authenticated: boolean; readonly userName?: string } | undefined {
  const line = raw
    .split("\n")
    .map((entry) => entry.trim())
    .find((entry) => entry.startsWith("{"));
  if (line === undefined) {
    return undefined;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(line);
  } catch {
    return undefined;
  }
  if (typeof parsed !== "object" || parsed === null) {
    return undefined;
  }
  const record = parsed as Record<string, unknown>;
  if (typeof record.authenticated !== "boolean") {
    return undefined;
  }
  const userName = typeof record.userName === "string" ? record.userName.trim() : "";
  return {
    authenticated: record.authenticated,
    ...(userName.length > 0 ? { userName } : {}),
  };
}

const discoverCommandCodeModelsViaAcp = (
  commandCodeSettings: CommandCodeSettings,
  environment: NodeJS.ProcessEnv = process.env,
) =>
  Effect.gen(function* () {
    const childProcessSpawner = yield* ChildProcessSpawner.ChildProcessSpawner;
    const acp = yield* makeCommandCodeAcpRuntime({
      commandCodeSettings,
      environment,
      childProcessSpawner,
      cwd: process.cwd(),
      clientInfo: { name: "t3-code-provider-probe", version: "0.0.0" },
    });
    const started = yield* acp.start();
    return buildCommandCodeModelsFromSessionModelState(started.sessionSetupResult.models);
  }).pipe(Effect.scoped);

const runCommandCodeCommand = (
  commandCodeSettings: CommandCodeSettings,
  args: ReadonlyArray<string>,
  environment: NodeJS.ProcessEnv = process.env,
) =>
  Effect.gen(function* () {
    const command = commandCodeSettings.binaryPath || "command-code";
    const spawnCommand = yield* resolveSpawnCommand(command, args, { env: environment });
    return yield* spawnAndCollect(
      command,
      ChildProcess.make(spawnCommand.command, spawnCommand.args, {
        env: environment,
        shell: spawnCommand.shell,
      }),
    );
  });

type CommandCodeAuthProbe = {
  readonly status: ServerProviderAuthStatus;
  readonly label?: string;
};

const checkCommandCodeAuth = (
  commandCodeSettings: CommandCodeSettings,
  environment: NodeJS.ProcessEnv,
): Effect.Effect<CommandCodeAuthProbe, never, ChildProcessSpawner.ChildProcessSpawner> =>
  runCommandCodeCommand(commandCodeSettings, ["status", "--json"], environment).pipe(
    Effect.timeoutOption(AUTH_PROBE_TIMEOUT_MS),
    Effect.result,
    Effect.map((result) => {
      if (Result.isFailure(result) || Option.isNone(result.success)) {
        return { status: "unknown" } as const;
      }
      const parsed = parseCommandCodeAuthStatusJson(result.success.value.stdout);
      if (parsed === undefined) {
        return { status: "unknown" } as const;
      }
      return parsed.authenticated
        ? ({
            status: "authenticated",
            ...(parsed.userName ? { label: parsed.userName } : {}),
          } as const)
        : ({ status: "unauthenticated" } as const);
    }),
    Effect.catchCause(() => Effect.succeed({ status: "unknown" } as const)),
  );

export const checkCommandCodeProviderStatus = Effect.fn("checkCommandCodeProviderStatus")(
  function* (
    commandCodeSettings: CommandCodeSettings,
    environment: NodeJS.ProcessEnv = process.env,
  ): Effect.fn.Return<
    ServerProviderDraft,
    never,
    ChildProcessSpawner.ChildProcessSpawner | Crypto.Crypto
  > {
    const checkedAt = DateTime.formatIso(yield* DateTime.now);
    const fallbackModels = commandCodeModelsFromSettings(commandCodeSettings.customModels);

    if (!commandCodeSettings.enabled) {
      return buildServerProvider({
        presentation: COMMAND_CODE_PRESENTATION,
        enabled: false,
        checkedAt,
        models: fallbackModels,
        probe: {
          installed: false,
          version: null,
          status: "warning",
          auth: { status: "unknown" },
          message: "Command Code is disabled in AgentTunnel settings.",
        },
      });
    }

    const versionResult = yield* runCommandCodeCommand(
      commandCodeSettings,
      ["--version"],
      environment,
    ).pipe(Effect.timeoutOption(VERSION_PROBE_TIMEOUT_MS), Effect.result);

    if (Result.isFailure(versionResult)) {
      const error = versionResult.failure;
      yield* Effect.logWarning("Command Code CLI health check failed.", {
        errorTag: error._tag,
      });
      return buildServerProvider({
        presentation: COMMAND_CODE_PRESENTATION,
        enabled: commandCodeSettings.enabled,
        checkedAt,
        models: fallbackModels,
        probe: {
          installed: !isCommandMissingCause(error),
          version: null,
          status: "error",
          auth: { status: "unknown" },
          message: isCommandMissingCause(error)
            ? "Command Code CLI (`command-code`) is not installed or not on PATH."
            : "Failed to execute Command Code CLI health check.",
        },
      });
    }

    if (Option.isNone(versionResult.success)) {
      return buildServerProvider({
        presentation: COMMAND_CODE_PRESENTATION,
        enabled: commandCodeSettings.enabled,
        checkedAt,
        models: fallbackModels,
        probe: {
          installed: true,
          version: null,
          status: "error",
          auth: { status: "unknown" },
          message:
            "Command Code CLI is installed but timed out while running `command-code --version`.",
        },
      });
    }

    const versionOutput = versionResult.success.value;
    const version = parseGenericCliVersion(`${versionOutput.stdout}\n${versionOutput.stderr}`);
    if (versionOutput.code !== 0) {
      yield* Effect.logWarning("Command Code CLI version probe exited with a non-zero status.", {
        exitCode: versionOutput.code,
        stdoutLength: versionOutput.stdout.length,
        stderrLength: versionOutput.stderr.length,
      });
      return buildServerProvider({
        presentation: COMMAND_CODE_PRESENTATION,
        enabled: commandCodeSettings.enabled,
        checkedAt,
        models: fallbackModels,
        probe: {
          installed: true,
          version,
          status: "error",
          auth: { status: "unknown" },
          message: "Command Code CLI is installed but failed to run.",
        },
      });
    }

    const authProbe = yield* checkCommandCodeAuth(commandCodeSettings, environment);

    const discoveryExit = yield* discoverCommandCodeModelsViaAcp(
      commandCodeSettings,
      environment,
    ).pipe(Effect.timeoutOption(COMMAND_CODE_ACP_MODEL_DISCOVERY_TIMEOUT_MS), Effect.exit);
    if (Exit.isFailure(discoveryExit)) {
      yield* Effect.logWarning("Command Code ACP model discovery failed", {
        errorTag: causeErrorTag(discoveryExit.cause),
      });
      return buildServerProvider({
        presentation: COMMAND_CODE_PRESENTATION,
        enabled: commandCodeSettings.enabled,
        checkedAt,
        models: fallbackModels,
        probe: {
          installed: true,
          version,
          status: authProbe.status === "unauthenticated" ? "warning" : "error",
          auth: authProbe.label
            ? { status: authProbe.status, label: authProbe.label }
            : { status: authProbe.status },
          message:
            authProbe.status === "unauthenticated"
              ? "Command Code is not signed in. Run `command-code login` in a terminal."
              : "Command Code CLI is installed but ACP startup failed. Check server logs for details.",
        },
      });
    }
    if (Option.isNone(discoveryExit.value)) {
      yield* Effect.logWarning(
        `Command Code ACP model discovery timed out after ${COMMAND_CODE_ACP_MODEL_DISCOVERY_TIMEOUT_MS}ms.`,
      );
      return buildServerProvider({
        presentation: COMMAND_CODE_PRESENTATION,
        enabled: commandCodeSettings.enabled,
        checkedAt,
        models: fallbackModels,
        probe: {
          installed: true,
          version,
          status: "error",
          auth: { status: authProbe.status },
          message: `Command Code CLI is installed but ACP startup timed out after ${COMMAND_CODE_ACP_MODEL_DISCOVERY_TIMEOUT_MS}ms.`,
        },
      });
    }

    const discoveredModels = discoveryExit.value.value;
    const models =
      discoveredModels.length > 0
        ? commandCodeModelsFromSettings(commandCodeSettings.customModels, discoveredModels)
        : fallbackModels;

    return buildServerProvider({
      presentation: COMMAND_CODE_PRESENTATION,
      enabled: commandCodeSettings.enabled,
      checkedAt,
      models,
      probe: {
        installed: true,
        version,
        status: "ready",
        auth: authProbe.label
          ? { status: authProbe.status, label: authProbe.label }
          : { status: authProbe.status },
      },
    });
  },
);

export const enrichCommandCodeSnapshot = (input: {
  readonly snapshot: ServerProvider;
  readonly maintenanceCapabilities: ProviderMaintenanceCapabilities;
  readonly enableProviderUpdateChecks?: boolean;
  readonly publishSnapshot: (snapshot: ServerProvider) => Effect.Effect<void>;
  readonly httpClient: HttpClient.HttpClient;
}): Effect.Effect<void> => {
  const { snapshot, publishSnapshot } = input;

  return enrichProviderSnapshotWithVersionAdvisory(snapshot, input.maintenanceCapabilities, {
    enableProviderUpdateChecks: input.enableProviderUpdateChecks,
  }).pipe(
    Effect.provideService(HttpClient.HttpClient, input.httpClient),
    Effect.flatMap((enrichedSnapshot) => publishSnapshot(enrichedSnapshot)),
    Effect.catchCause((cause) =>
      Effect.logWarning("Command Code version advisory enrichment failed", {
        errorTag: causeErrorTag(cause),
      }),
    ),
    Effect.asVoid,
  );
};
