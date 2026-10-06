/**
 * CommandCodeAcpSupport — spawn/model helpers for the Command Code CLI's
 * built-in ACP agent (`command-code acp`).
 *
 * Command Code ships a stdio ACP server, so the provider reuses the shared
 * {@link ./AcpSessionRuntime} transport exactly like Cursor and Grok. The CLI
 * advertises one non-terminal auth method (`command-code-cli`); the ACP
 * `authenticate` call reuses the account created by `command-code login`, so
 * an unauthenticated machine fails session start with a readable AUTH_REQUIRED
 * error instead of hanging.
 *
 * Model ids are Command Code catalog slugs (for example
 * `deepseek/deepseek-v4-flash`); no alias normalization beyond the shared
 * per-provider map is applied.
 *
 * @module provider/acp/CommandCodeAcpSupport
 */
import { type CommandCodeSettings, ProviderDriverKind } from "@t3tools/contracts";
import { normalizeModelSlug } from "@t3tools/shared/model";
import * as Crypto from "effect/Crypto";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Scope from "effect/Scope";
import * as ChildProcessSpawner from "effect/unstable/process/ChildProcessSpawner";
import type * as EffectAcpErrors from "effect-acp/errors";
import type * as EffectAcpSchema from "effect-acp/schema";

import * as AcpSessionRuntime from "./AcpSessionRuntime.ts";

export const COMMAND_CODE_DRIVER_KIND = ProviderDriverKind.make("commandcode");

/**
 * The non-terminal method Command Code's ACP server advertises. The terminal
 * method (`command-code-login`) is filtered out unless the client advertises
 * terminal auth support, which AgentTunnel does not.
 */
const COMMAND_CODE_AUTH_METHOD_ID = "command-code-cli";

/** Command Code's own default catalog model, used when a thread names none. */
export const COMMAND_CODE_DEFAULT_MODEL = "deepseek/deepseek-v4-flash";

type CommandCodeAcpRuntimeSettings = Pick<CommandCodeSettings, "binaryPath">;

export interface CommandCodeAcpRuntimeInput extends Omit<
  AcpSessionRuntime.AcpSessionRuntimeOptions,
  "authMethodId" | "spawn"
> {
  readonly childProcessSpawner: ChildProcessSpawner.ChildProcessSpawner["Service"];
  readonly commandCodeSettings: CommandCodeAcpRuntimeSettings | null | undefined;
  readonly environment?: NodeJS.ProcessEnv;
}

export function buildCommandCodeAcpSpawnInput(
  commandCodeSettings: CommandCodeAcpRuntimeSettings | null | undefined,
  cwd: string,
  environment?: NodeJS.ProcessEnv,
): AcpSessionRuntime.AcpSpawnInput {
  return {
    command: commandCodeSettings?.binaryPath || "command-code",
    args: ["acp"],
    cwd,
    ...(environment ? { env: environment } : {}),
  };
}

export const makeCommandCodeAcpRuntime = (
  input: CommandCodeAcpRuntimeInput,
): Effect.Effect<
  AcpSessionRuntime.AcpSessionRuntime["Service"],
  EffectAcpErrors.AcpError,
  Crypto.Crypto | Scope.Scope
> =>
  Effect.gen(function* () {
    const acpContext = yield* Layer.build(
      AcpSessionRuntime.layer({
        ...input,
        spawn: buildCommandCodeAcpSpawnInput(
          input.commandCodeSettings,
          input.cwd,
          input.environment,
        ),
        authMethodId: COMMAND_CODE_AUTH_METHOD_ID,
      }).pipe(
        Layer.provide(
          Layer.succeed(ChildProcessSpawner.ChildProcessSpawner, input.childProcessSpawner),
        ),
      ),
    );
    return yield* Effect.service(AcpSessionRuntime.AcpSessionRuntime).pipe(
      Effect.provide(acpContext),
    );
  });

export function resolveCommandCodeAcpBaseModelId(model: string | null | undefined): string {
  const trimmed = model?.trim();
  const base = trimmed && trimmed.length > 0 ? trimmed : COMMAND_CODE_DEFAULT_MODEL;
  return normalizeModelSlug(base, COMMAND_CODE_DRIVER_KIND) ?? COMMAND_CODE_DEFAULT_MODEL;
}

export function currentCommandCodeModelIdFromSessionSetup(
  sessionSetupResult:
    | EffectAcpSchema.LoadSessionResponse
    | EffectAcpSchema.NewSessionResponse
    | EffectAcpSchema.ResumeSessionResponse,
): string | undefined {
  return sessionSetupResult.models?.currentModelId?.trim() || undefined;
}

/**
 * Select a model through the unstable ACP `session/set_model` capability.
 * Command Code validates the id against its live catalog and rejects unknown
 * ids, so a stale selection surfaces as a typed adapter error rather than a
 * silently ignored switch.
 */
export function applyCommandCodeAcpModelSelection<E>(input: {
  readonly runtime: Pick<AcpSessionRuntime.AcpSessionRuntime["Service"], "setSessionModel">;
  readonly currentModelId: string | undefined;
  readonly requestedModelId: string | undefined;
  readonly mapError: (cause: EffectAcpErrors.AcpError) => E;
}): Effect.Effect<string | undefined, E> {
  const requestedModelId = input.requestedModelId;
  if (requestedModelId === undefined || requestedModelId === input.currentModelId) {
    return Effect.succeed(input.currentModelId);
  }
  return input.runtime
    .setSessionModel(requestedModelId)
    .pipe(Effect.mapError(input.mapError), Effect.as(requestedModelId));
}
