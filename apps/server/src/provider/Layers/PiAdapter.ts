/**
 * PiAdapter — pi coding agent (`pi --mode rpc`) via the JSONL RPC protocol.
 *
 * Mirrors the driver-model adapter conventions used by
 * {@link ./OpenCodeAdapter}: one adapter per configured instance, an Effect
 * factory that captures its services at construction, and a canonical
 * `ProviderRuntimeEvent` stream for the UI.
 *
 * pi exposes no approval/permission protocol in RPC mode, so
 * `respondToRequest` / `respondToUserInput` are resolving no-ops rather than
 * errors.
 *
 * @module PiAdapter
 */
import {
  type ChatAttachment,
  EventId,
  type ModelSelection,
  type PiSettings,
  type ProviderRuntimeEvent,
  type ProviderSession,
  ProviderDriverKind,
  ProviderInstanceId,
  RuntimeItemId,
  type ThreadId,
  type ToolLifecycleItemType,
  TurnId,
} from "@t3tools/contracts";
import { getModelSelectionStringOptionValue } from "@t3tools/shared/model";
import * as Crypto from "effect/Crypto";
import * as DateTime from "effect/DateTime";
import * as Duration from "effect/Duration";
import * as Effect from "effect/Effect";
import * as Exit from "effect/Exit";
import * as Fiber from "effect/Fiber";
import * as FileSystem from "effect/FileSystem";
import * as Option from "effect/Option";
import * as Path from "effect/Path";
import * as Queue from "effect/Queue";
import * as Scope from "effect/Scope";
import * as Semaphore from "effect/Semaphore";
import * as Stream from "effect/Stream";
import * as SynchronizedRef from "effect/SynchronizedRef";
import * as ChildProcessSpawner from "effect/unstable/process/ChildProcessSpawner";

import { resolveAttachmentPath } from "../../attachmentStore.ts";
import { ServerConfig } from "../../config.ts";
import {
  ProviderAdapterProcessError,
  ProviderAdapterRequestError,
  ProviderAdapterSessionNotFoundError,
  ProviderAdapterValidationError,
} from "../Errors.ts";
import {
  asPiRecord,
  makePiRpcSession,
  piRuntimeErrorDetail,
  type PiRpcCommand,
  type PiRpcSession,
  type PiRpcValue,
} from "../piRuntime.ts";
import type { PiAdapterShape } from "../Services/PiAdapter.ts";
import type { ProviderThreadTurnSnapshot } from "../Services/ProviderAdapter.ts";
import { type EventNdjsonLogger, makeEventNdjsonLogger } from "./EventNdjsonLogger.ts";

const PROVIDER = ProviderDriverKind.make("pi");

/**
 * Version tag stamped into the resume cursor. pi sessions are addressed by the
 * exact project session id, which the adapter pins to the thread id, so the
 * cursor only needs to record the id shape for future migrations.
 */
const PI_RESUME_VERSION = 1 as const;

export interface PiAdapterLiveOptions {
  readonly environment?: NodeJS.ProcessEnv;
  readonly nativeEventLogPath?: string;
  readonly nativeEventLogger?: EventNdjsonLogger;
  /** Selections are honored when `modelSelection.instanceId` matches this value. */
  readonly instanceId?: ProviderInstanceId;
}

export function mapPiToolNameToItemType(toolName: string): ToolLifecycleItemType {
  const name = toolName.trim().toLowerCase();
  if (name === "bash" || name === "shell" || name === "exec" || name.endsWith("_bash")) {
    return "command_execution";
  }
  if (
    [
      "write",
      "edit",
      "multi_edit",
      "multiedit",
      "apply_patch",
      "patch",
      "str_replace",
      "create",
      "notebook_edit",
    ].includes(name)
  ) {
    return "file_change";
  }
  if (name.startsWith("mcp") || name.includes("mcp")) {
    return "mcp_tool_call";
  }
  if (name === "web_search" || name === "websearch" || name === "search_web") {
    return "web_search";
  }
  if (name === "view_image" || name === "image_view" || name === "screenshot") {
    return "image_view";
  }
  if (name === "task" || name.startsWith("agent") || name === "collab") {
    return "collab_agent_tool_call";
  }
  return "dynamic_tool_call";
}

/** Parse pi's `provider/modelId` slug. */
export function parsePiModelSlug(
  slug: string,
): { readonly provider: string; readonly modelId: string } | undefined {
  const trimmed = slug.trim();
  if (trimmed.length === 0) return undefined;
  const separator = trimmed.indexOf("/");
  if (separator <= 0 || separator === trimmed.length - 1) return undefined;
  return {
    provider: trimmed.slice(0, separator),
    modelId: trimmed.slice(separator + 1),
  };
}

export interface PiEmission {
  readonly type: ProviderRuntimeEvent["type"];
  readonly payload: unknown;
  readonly itemId?: string;
  readonly turnId?: TurnId;
}

export interface PiEventMapperState {
  turnId: TurnId | undefined;
  interrupted: boolean;
  active: boolean;
  assistantItemId: string | undefined;
  assistantItemCounter: number;
  contentKinds: Map<number, "text" | "thinking" | "toolcall">;
  contentBuffers: Map<number, string>;
  lastStopReason: string | undefined;
  compactionItemId: string | undefined;
}

export function createPiEventMapperState(): PiEventMapperState {
  return {
    turnId: undefined,
    interrupted: false,
    active: false,
    assistantItemId: undefined,
    assistantItemCounter: 0,
    contentKinds: new Map(),
    contentBuffers: new Map(),
    lastStopReason: undefined,
    compactionItemId: undefined,
  };
}

const asString = (value: unknown): string | undefined =>
  typeof value === "string" ? value : undefined;

const asFiniteInt = (value: unknown): number | undefined =>
  typeof value === "number" && Number.isFinite(value) ? Math.max(0, Math.round(value)) : undefined;

function mapPiUsage(usage: PiRpcValue): Record<string, unknown> | undefined {
  const input = asFiniteInt(usage.input);
  const output = asFiniteInt(usage.output);
  const cached = asFiniteInt(usage.cacheRead);
  const total = asFiniteInt(usage.totalTokens);
  const usedTokens = total ?? (input ?? 0) + (output ?? 0);
  if (usedTokens === 0 && input === undefined && output === undefined) {
    return undefined;
  }
  return {
    usedTokens,
    ...(input !== undefined ? { inputTokens: input, lastInputTokens: input } : {}),
    ...(cached !== undefined ? { cachedInputTokens: cached, lastCachedInputTokens: cached } : {}),
    ...(output !== undefined ? { outputTokens: output, lastOutputTokens: output } : {}),
  };
}

function applyPiAssistantMessageEvent(
  state: PiEventMapperState,
  event: PiRpcValue,
): ReadonlyArray<PiEmission> {
  const assistantEvent = asPiRecord(event.assistantMessageEvent);
  if (assistantEvent === undefined) return [];
  const index = typeof assistantEvent.contentIndex === "number" ? assistantEvent.contentIndex : 0;
  const kind = asString(assistantEvent.type);
  switch (kind) {
    case "text_start":
      state.contentKinds.set(index, "text");
      state.contentBuffers.set(index, "");
      return [];
    case "thinking_start":
      state.contentKinds.set(index, "thinking");
      state.contentBuffers.set(index, "");
      return [];
    case "toolcall_start":
      state.contentKinds.set(index, "toolcall");
      state.contentBuffers.set(index, "");
      return [];
    case "text_delta":
    case "thinking_delta": {
      const delta = asString(assistantEvent.delta) ?? "";
      if (delta.length === 0) return [];
      state.contentBuffers.set(index, (state.contentBuffers.get(index) ?? "") + delta);
      return [
        {
          type: "content.delta",
          payload: {
            streamKind: kind === "text_delta" ? "assistant_text" : "reasoning_text",
            delta,
            contentIndex: index,
          },
        },
      ];
    }
    case "toolcall_delta": {
      const delta = asString(assistantEvent.delta) ?? "";
      // Tool arguments are surfaced authoritatively by tool_execution_start,
      // so the fragmented JSON deltas are only buffered for completeness.
      state.contentBuffers.set(index, (state.contentBuffers.get(index) ?? "") + delta);
      return [];
    }
    case "text_end":
    case "thinking_end":
    case "toolcall_end": {
      state.contentBuffers.delete(index);
      state.contentKinds.delete(index);
      return [];
    }
    default:
      return [];
  }
}

/**
 * Reduce one parsed pi event into zero or more canonical emissions. Pure and
 * synchronous (mutating only `state`) so delta assembly, tool lifecycle, and
 * turn state are unit-testable without a live child.
 */
export function applyPiEvent(
  state: PiEventMapperState,
  event: PiRpcValue,
): ReadonlyArray<PiEmission> {
  switch (asString(event.type)) {
    case "agent_start": {
      state.active = true;
      return [];
    }
    case "agent_end": {
      state.active = false;
      if (event.willRetry === true) return [];
      const turnId = state.turnId;
      if (turnId === undefined) return [];
      state.turnId = undefined;
      if (state.interrupted) {
        state.interrupted = false;
        state.lastStopReason = undefined;
        return [{ type: "turn.aborted", turnId, payload: { reason: "Interrupted by user." } }];
      }
      const stopReason = state.lastStopReason;
      state.lastStopReason = undefined;
      const turnState =
        stopReason === "aborted" ? "interrupted" : stopReason === "error" ? "failed" : "completed";
      return [
        {
          type: "turn.completed",
          turnId,
          payload: {
            state: turnState,
            ...(stopReason !== undefined ? { stopReason } : {}),
          },
        },
      ];
    }
    case "message_start": {
      const message = asPiRecord(event.message);
      if (message?.role !== "assistant") return [];
      state.assistantItemCounter += 1;
      state.assistantItemId =
        asString(message.responseId) ?? `pi-assistant-${state.assistantItemCounter}`;
      return [
        {
          type: "item.started",
          itemId: state.assistantItemId,
          payload: { itemType: "assistant_message", status: "inProgress" },
        },
      ];
    }
    case "message_update":
      return applyPiAssistantMessageEvent(state, event);
    case "message_end": {
      const message = asPiRecord(event.message);
      if (message?.role !== "assistant") return [];
      state.assistantItemCounter += 1;
      const itemId = state.assistantItemId ?? `pi-assistant-${state.assistantItemCounter}`;
      state.assistantItemId = undefined;
      const stopReason = asString(message.stopReason);
      const status =
        stopReason === "aborted" ? "declined" : stopReason === "error" ? "failed" : "completed";
      const emissions: Array<PiEmission> = [
        {
          type: "item.completed",
          itemId,
          payload: { itemType: "assistant_message", status, data: message },
        },
      ];
      const usage = asPiRecord(message.usage);
      const mappedUsage = usage === undefined ? undefined : mapPiUsage(usage);
      if (mappedUsage !== undefined) {
        emissions.push({
          type: "thread.token-usage.updated",
          payload: { usage: mappedUsage },
        });
      }
      return emissions;
    }
    case "tool_execution_start": {
      const toolCallId = asString(event.toolCallId);
      if (toolCallId === undefined) return [];
      const toolName = asString(event.toolName) ?? "tool";
      return [
        {
          type: "item.started",
          itemId: toolCallId,
          payload: {
            itemType: mapPiToolNameToItemType(toolName),
            status: "inProgress",
            title: toolName,
            data: { args: event.args },
          },
        },
      ];
    }
    case "tool_execution_update": {
      const toolCallId = asString(event.toolCallId);
      if (toolCallId === undefined) return [];
      const toolName = asString(event.toolName) ?? "tool";
      return [
        {
          type: "item.updated",
          itemId: toolCallId,
          payload: {
            itemType: mapPiToolNameToItemType(toolName),
            status: "inProgress",
            title: toolName,
            data: { partialResult: event.partialResult },
          },
        },
      ];
    }
    case "tool_execution_end": {
      const toolCallId = asString(event.toolCallId);
      if (toolCallId === undefined) return [];
      const toolName = asString(event.toolName) ?? "tool";
      const isError = event.isError === true;
      return [
        {
          type: "item.completed",
          itemId: toolCallId,
          payload: {
            itemType: mapPiToolNameToItemType(toolName),
            status: isError ? "failed" : "completed",
            title: toolName,
            data: { result: event.result, isError },
          },
        },
      ];
    }
    case "turn_end": {
      const message = asPiRecord(event.message);
      state.lastStopReason = asString(message?.stopReason);
      return [];
    }
    case "compaction_start": {
      const itemId = state.compactionItemId ?? "pi-compaction";
      state.compactionItemId = itemId;
      return [
        {
          type: "item.started",
          itemId,
          payload: {
            itemType: "context_compaction",
            status: "inProgress",
            ...(asString(event.reason) !== undefined ? { detail: asString(event.reason) } : {}),
          },
        },
      ];
    }
    case "compaction_end": {
      const itemId = state.compactionItemId ?? "pi-compaction";
      state.compactionItemId = undefined;
      const aborted = event.aborted === true;
      return [
        {
          type: "item.completed",
          itemId,
          payload: {
            itemType: "context_compaction",
            status: aborted ? "declined" : "completed",
            data: { result: event.result },
          },
        },
      ];
    }
    case "auto_retry_start":
      return [
        {
          type: "runtime.warning",
          payload: {
            message: `pi is retrying (attempt ${asFiniteInt(event.attempt) ?? "?"}/${asFiniteInt(event.maxAttempts) ?? "?"}).`,
            ...(asString(event.errorMessage) !== undefined
              ? { detail: asString(event.errorMessage) }
              : {}),
          },
        },
      ];
    case "auto_retry_end": {
      if (event.success === true) return [];
      return [
        {
          type: "runtime.error",
          payload: {
            message: "pi exhausted its automatic retries.",
            class: "provider_error",
            ...(asString(event.finalError) !== undefined
              ? { detail: asString(event.finalError) }
              : {}),
          },
        },
      ];
    }
    case "extension_error":
      return [
        {
          type: "runtime.error",
          payload: {
            message: asString(event.error) ?? "pi extension error.",
            class: "provider_error",
            detail: { extensionPath: event.extensionPath, event: event.event },
          },
        },
      ];
    case "extension_ui_request":
      return [
        {
          type: "runtime.warning",
          payload: {
            message: "pi requested interactive UI, which AgentTunnel does not support.",
            detail: event.method,
          },
        },
      ];
    default:
      return [];
  }
}

export function mapPiMessagesToTurns(
  messages: ReadonlyArray<unknown>,
): ReadonlyArray<ProviderThreadTurnSnapshot> {
  const turns: Array<ProviderThreadTurnSnapshot> = [];
  for (const raw of messages) {
    const message = asPiRecord(raw);
    if (message?.role !== "assistant") continue;
    const responseId = asString(message.responseId);
    turns.push({
      id: TurnId.make(responseId ?? `pi-turn-${turns.length + 1}`),
      items: [message],
    });
  }
  return turns;
}

interface PiSessionContext {
  readonly threadId: ThreadId;
  session: ProviderSession;
  readonly rpc: PiRpcSession;
  readonly scope: Scope.Closeable;
  readonly mapper: PiEventMapperState;
  eventFiber: Fiber.Fiber<void, never> | undefined;
  activeTurnId: TurnId | undefined;
  promptsInFlight: number;
  stopped: boolean;
}

export function makePiAdapter(piSettings: PiSettings, options?: PiAdapterLiveOptions) {
  return Effect.gen(function* () {
    const boundInstanceId = options?.instanceId ?? ProviderInstanceId.make("pi");
    const serverConfig = yield* ServerConfig;
    const spawner = yield* ChildProcessSpawner.ChildProcessSpawner;
    const crypto = yield* Crypto.Crypto;
    const fileSystem = yield* FileSystem.FileSystem;
    const path = yield* Path.Path;
    const nativeEventLogger =
      options?.nativeEventLogger ??
      (options?.nativeEventLogPath !== undefined
        ? yield* makeEventNdjsonLogger(options.nativeEventLogPath, { stream: "native" })
        : undefined);
    const managedNativeEventLogger =
      options?.nativeEventLogger === undefined ? nativeEventLogger : undefined;

    const runtimeEvents = yield* Queue.unbounded<ProviderRuntimeEvent>();
    const sessions = new Map<ThreadId, PiSessionContext>();
    const threadLocksRef = yield* SynchronizedRef.make(new Map<string, Semaphore.Semaphore>());

    const nowIso = Effect.map(DateTime.now, DateTime.formatIso);
    const randomUUIDv4 = crypto.randomUUIDv4.pipe(Effect.orDie);
    const nextEventId = Effect.map(randomUUIDv4, (id) => EventId.make(id));

    const getThreadSemaphore = (threadId: string) =>
      SynchronizedRef.modifyEffect(threadLocksRef, (current) => {
        const existing = Option.fromNullishOr(current.get(threadId));
        return Option.match(existing, {
          onNone: () =>
            Semaphore.make(1).pipe(
              Effect.map((semaphore) => {
                const next = new Map(current);
                next.set(threadId, semaphore);
                return [semaphore, next] as const;
              }),
            ),
          onSome: (semaphore) => Effect.succeed([semaphore, current] as const),
        });
      });

    const withThreadLock = <A, E, R>(threadId: string, effect: Effect.Effect<A, E, R>) =>
      Effect.flatMap(getThreadSemaphore(threadId), (semaphore) => semaphore.withPermit(effect));

    const emit = (event: ProviderRuntimeEvent) =>
      Queue.offer(runtimeEvents, event).pipe(Effect.asVoid);
    const writeNativeEvent = (threadId: ThreadId, event: PiRpcValue) =>
      nativeEventLogger === undefined
        ? Effect.void
        : Effect.gen(function* () {
            const observedAt = yield* nowIso;
            yield* nativeEventLogger.write({ observedAt, event }, threadId);
          }).pipe(Effect.catchCause(() => Effect.void));

    const buildEvent = (
      threadId: ThreadId,
      turnId: TurnId | undefined,
      emission: PiEmission,
    ): Effect.Effect<ProviderRuntimeEvent> =>
      Effect.all({ eventId: nextEventId, createdAt: nowIso }).pipe(
        Effect.map(
          ({ eventId, createdAt }) =>
            ({
              eventId,
              provider: PROVIDER,
              providerInstanceId: boundInstanceId,
              threadId,
              createdAt,
              ...(turnId !== undefined ? { turnId } : {}),
              ...(emission.itemId !== undefined
                ? { itemId: RuntimeItemId.make(emission.itemId) }
                : {}),
              type: emission.type,
              payload: emission.payload,
            }) as unknown as ProviderRuntimeEvent,
        ),
      );

    const requireSession = (
      threadId: ThreadId,
    ): Effect.Effect<PiSessionContext, ProviderAdapterSessionNotFoundError> => {
      const context = sessions.get(threadId);
      if (context === undefined || context.stopped) {
        return Effect.fail(
          new ProviderAdapterSessionNotFoundError({ provider: PROVIDER, threadId }),
        );
      }
      return Effect.succeed(context);
    };

    const sendPiCommand = (
      rpc: PiRpcSession,
      command: PiRpcCommand,
      method: string,
      timeout: Duration.Input = "15 seconds",
    ): Effect.Effect<PiRpcResponseLike, ProviderAdapterRequestError> =>
      rpc.send(command).pipe(
        Effect.timeoutOption(timeout),
        Effect.flatMap((option) =>
          Option.match(option, {
            onNone: () =>
              Effect.fail(
                new ProviderAdapterRequestError({
                  provider: PROVIDER,
                  method,
                  detail: "Timed out waiting for a pi response.",
                }),
              ),
            onSome: (response) =>
              response.success
                ? Effect.succeed(response)
                : Effect.fail(
                    new ProviderAdapterRequestError({
                      provider: PROVIDER,
                      method,
                      detail:
                        response.error !== undefined && response.error.trim().length > 0
                          ? response.error
                          : "pi command failed.",
                    }),
                  ),
          }),
        ),
        Effect.catchTags({
          PiRuntimeError: (cause) =>
            Effect.fail(
              new ProviderAdapterRequestError({
                provider: PROVIDER,
                method,
                detail: piRuntimeErrorDetail(cause),
                cause,
              }),
            ),
        }),
      );

    const readPiImages = (
      attachments: ReadonlyArray<ChatAttachment>,
    ): Effect.Effect<
      ReadonlyArray<{ type: "image"; data: string; mimeType: string }>,
      ProviderAdapterValidationError | ProviderAdapterRequestError
    > =>
      Effect.gen(function* () {
        const images: Array<{ type: "image"; data: string; mimeType: string }> = [];
        for (const attachment of attachments) {
          if (attachment.type !== "image") continue;
          const attachmentPath = resolveAttachmentPath({
            attachmentsDir: serverConfig.attachmentsDir,
            attachment,
          });
          if (attachmentPath === null) {
            return yield* new ProviderAdapterValidationError({
              provider: PROVIDER,
              operation: "sendTurn",
              issue: `Invalid attachment id '${attachment.id}'.`,
            });
          }
          const bytes = yield* fileSystem.readFile(attachmentPath).pipe(
            Effect.mapError(
              (cause) =>
                new ProviderAdapterRequestError({
                  provider: PROVIDER,
                  method: "prompt",
                  detail: cause.message,
                  cause,
                }),
            ),
          );
          images.push({
            type: "image",
            data: Buffer.from(bytes).toString("base64"),
            mimeType: attachment.mimeType,
          });
        }
        return images;
      });

    const applyPiModelSelection = (
      rpc: PiRpcSession,
      modelSelection: ModelSelection,
      method: string,
    ): Effect.Effect<void, ProviderAdapterRequestError | ProviderAdapterValidationError> =>
      Effect.gen(function* () {
        const parsed = parsePiModelSlug(modelSelection.model);
        if (parsed === undefined) {
          return yield* new ProviderAdapterValidationError({
            provider: PROVIDER,
            operation: method,
            issue: `Invalid pi model '${modelSelection.model}' (expected provider/model).`,
          });
        }
        yield* sendPiCommand(
          rpc,
          { type: "set_model", provider: parsed.provider, modelId: parsed.modelId },
          "set_model",
        );
        const thinkingLevel =
          getModelSelectionStringOptionValue(modelSelection, "thinkingLevel") ??
          getModelSelectionStringOptionValue(modelSelection, "thinking") ??
          getModelSelectionStringOptionValue(modelSelection, "effort");
        if (thinkingLevel !== undefined) {
          yield* sendPiCommand(
            rpc,
            { type: "set_thinking_level", level: thinkingLevel },
            "set_thinking_level",
          ).pipe(Effect.ignore);
        }
      });

    const consumePiEvent = (context: PiSessionContext, event: PiRpcValue): Effect.Effect<void> =>
      Effect.gen(function* () {
        yield* writeNativeEvent(context.threadId, event);
        const emissions = applyPiEvent(context.mapper, event);
        for (const emission of emissions) {
          const turnId = emission.turnId ?? context.activeTurnId;
          yield* emit(yield* buildEvent(context.threadId, turnId, emission));
          if (emission.type === "turn.completed" || emission.type === "turn.aborted") {
            context.activeTurnId = undefined;
            context.promptsInFlight = 0;
            const { activeTurnId: _activeTurnId, ...rest } = context.session;
            context.session = {
              ...rest,
              status: "ready",
              updatedAt: yield* nowIso,
            };
          }
        }
      }).pipe(Effect.catchCause(() => Effect.void));

    const handlePiProcessExit = (
      context: PiSessionContext,
      exit: { readonly code: number; readonly stderr: string },
    ): Effect.Effect<void> =>
      Effect.gen(function* () {
        if (context.stopped) return;
        context.stopped = true;
        sessions.delete(context.threadId);
        const activeTurnId = context.activeTurnId;
        context.activeTurnId = undefined;
        const { activeTurnId: _activeTurnId, ...rest } = context.session;
        context.session = {
          ...rest,
          status: exit.code === 0 ? "closed" : "error",
          updatedAt: yield* nowIso,
          ...(exit.code !== 0
            ? {
                lastError:
                  exit.stderr.trim().length > 0
                    ? exit.stderr.trim()
                    : `pi process exited with code ${exit.code}.`,
              }
            : {}),
        };
        if (activeTurnId !== undefined) {
          yield* emit(
            yield* buildEvent(context.threadId, activeTurnId, {
              type: "turn.completed",
              turnId: activeTurnId,
              payload: {
                state: "failed",
                errorMessage: `pi process exited with code ${exit.code}.`,
              },
            }),
          );
        }
        yield* emit(
          yield* buildEvent(context.threadId, undefined, {
            type: "session.exited",
            payload: {
              reason: `pi process exited with code ${exit.code}.`,
              recoverable: false,
              exitKind: exit.code === 0 ? "graceful" : "error",
            },
          }),
        );
      });

    const stopSessionInternal = (context: PiSessionContext): Effect.Effect<void> =>
      Effect.gen(function* () {
        if (context.stopped) return;
        context.stopped = true;
        sessions.delete(context.threadId);
        yield* Scope.close(context.scope, Exit.void).pipe(Effect.ignore);
        yield* emit(
          yield* buildEvent(context.threadId, undefined, {
            type: "session.exited",
            payload: { exitKind: "graceful" },
          }),
        );
      });

    const startSession: PiAdapterShape["startSession"] = (input) =>
      withThreadLock(
        input.threadId,
        Effect.gen(function* () {
          if (input.provider !== undefined && input.provider !== PROVIDER) {
            return yield* new ProviderAdapterValidationError({
              provider: PROVIDER,
              operation: "startSession",
              issue: `Expected provider '${PROVIDER}' but received '${input.provider}'.`,
            });
          }
          if (!input.cwd?.trim()) {
            return yield* new ProviderAdapterValidationError({
              provider: PROVIDER,
              operation: "startSession",
              issue: "cwd is required and must be non-empty.",
            });
          }
          const cwd = path.resolve(input.cwd.trim());
          const modelSelection =
            input.modelSelection?.instanceId === boundInstanceId ? input.modelSelection : undefined;

          const existing = sessions.get(input.threadId);
          if (existing !== undefined && !existing.stopped) {
            yield* stopSessionInternal(existing);
          }

          const sessionScope = yield* Scope.make("sequential");
          let sessionScopeTransferred = false;
          yield* Effect.addFinalizer(() =>
            sessionScopeTransferred ? Effect.void : Scope.close(sessionScope, Exit.void),
          );

          const rpc = yield* makePiRpcSession({
            binaryPath: piSettings.binaryPath,
            args: ["--mode", "rpc", "--session-id", input.threadId],
            cwd,
            ...(options?.environment !== undefined ? { environment: options.environment } : {}),
          }).pipe(
            Effect.provideService(ChildProcessSpawner.ChildProcessSpawner, spawner),
            Effect.provideService(Scope.Scope, sessionScope),
            Effect.mapError(
              (cause) =>
                new ProviderAdapterProcessError({
                  provider: PROVIDER,
                  threadId: input.threadId,
                  detail: piRuntimeErrorDetail(cause),
                  cause,
                }),
            ),
          );

          const stateResponse = yield* sendPiCommand(rpc, { type: "get_state" }, "get_state").pipe(
            Effect.mapError(
              (cause) =>
                new ProviderAdapterProcessError({
                  provider: PROVIDER,
                  threadId: input.threadId,
                  detail: cause.detail,
                  cause,
                }),
            ),
          );
          const stateData = asPiRecord(stateResponse.data) ?? {};
          const providerThreadId = asString(stateData.sessionId) ?? input.threadId;
          const modelFromState = (() => {
            const model = asPiRecord(stateData.model);
            const provider = asString(model?.provider);
            const id = asString(model?.id);
            return provider !== undefined && id !== undefined ? `${provider}/${id}` : undefined;
          })();

          if (modelSelection !== undefined) {
            yield* applyPiModelSelection(rpc, modelSelection, "startSession").pipe(
              Effect.mapError(
                (cause) =>
                  new ProviderAdapterProcessError({
                    provider: PROVIDER,
                    threadId: input.threadId,
                    detail: cause.message,
                    cause,
                  }),
              ),
            );
          }

          const now = yield* nowIso;
          const session: ProviderSession = {
            provider: PROVIDER,
            providerInstanceId: boundInstanceId,
            status: "ready",
            runtimeMode: input.runtimeMode,
            cwd,
            model: modelSelection?.model ?? modelFromState,
            threadId: input.threadId,
            resumeCursor: { schemaVersion: PI_RESUME_VERSION, sessionId: providerThreadId },
            createdAt: now,
            updatedAt: now,
          };

          const context: PiSessionContext = {
            threadId: input.threadId,
            session,
            rpc,
            scope: sessionScope,
            mapper: createPiEventMapperState(),
            eventFiber: undefined,
            activeTurnId: undefined,
            promptsInFlight: 0,
            stopped: false,
          };

          context.eventFiber = yield* rpc.events.pipe(
            Stream.runForEach((event) => consumePiEvent(context, event)),
            Effect.catchCause(() => Effect.void),
            Effect.forkIn(sessionScope),
          );
          // Register before forking the exit watcher so a child that dies during
          // startup is removed from `sessions` instead of being re-added after
          // `handlePiProcessExit` already marked it stopped.
          sessions.set(input.threadId, context);
          sessionScopeTransferred = true;
          yield* rpc.awaitExit.pipe(
            Effect.flatMap((exit) => handlePiProcessExit(context, exit)),
            Effect.forkIn(sessionScope),
          );

          yield* emit(
            yield* buildEvent(input.threadId, undefined, {
              type: "session.started",
              payload: { resume: session.resumeCursor },
            }),
          );
          yield* emit(
            yield* buildEvent(input.threadId, undefined, {
              type: "session.state.changed",
              payload: { state: "ready", reason: "pi RPC session ready" },
            }),
          );
          yield* emit(
            yield* buildEvent(input.threadId, undefined, {
              type: "thread.started",
              payload: { providerThreadId },
            }),
          );

          return session;
        }).pipe(Effect.scoped),
      );

    /**
     * A new turn binds its id and emits `turn.started` before pi confirms the
     * prompt. pi reports a rejected prompt as `success:false` with no events
     * (e.g. missing credentials), so without a rollback the session would stay
     * `running` and the next message would be misrouted as a steer. Undo the
     * turn state and report the failure the way the other adapters do: a
     * `turn.completed` with `state: "failed"`.
     */
    const rollbackRejectedTurn = (
      context: PiSessionContext,
      turnId: TurnId,
      detail: string,
    ): Effect.Effect<void> =>
      Effect.gen(function* () {
        if (context.activeTurnId !== turnId) {
          return;
        }
        context.activeTurnId = undefined;
        context.mapper.turnId = undefined;
        // A rejected non-steering prompt never ran, so discard any interrupt
        // the user issued during admission; otherwise the next turn's
        // `agent_end` would be misreported as `turn.aborted`.
        context.mapper.interrupted = false;
        context.mapper.lastStopReason = undefined;
        context.promptsInFlight = 0;
        const { activeTurnId: _activeTurnId, ...rest } = context.session;
        context.session = {
          ...rest,
          status: "ready",
          updatedAt: yield* nowIso,
        };
        yield* emit(
          yield* buildEvent(context.threadId, turnId, {
            type: "turn.completed",
            turnId,
            payload: { state: "failed", errorMessage: detail },
          }),
        );
      });

    const sendTurn: PiAdapterShape["sendTurn"] = (input) =>
      withThreadLock(
        input.threadId,
        Effect.gen(function* () {
          const context = yield* requireSession(input.threadId);
          const modelSelection =
            input.modelSelection?.instanceId === boundInstanceId ? input.modelSelection : undefined;

          if (modelSelection !== undefined && modelSelection.model !== context.session.model) {
            yield* applyPiModelSelection(context.rpc, modelSelection, "sendTurn");
            context.session = {
              ...context.session,
              model: modelSelection.model,
              updatedAt: yield* nowIso,
            };
          }

          const message = input.input?.trim() ?? "";
          const images = yield* readPiImages(input.attachments ?? []);
          if (message.length === 0 && images.length === 0) {
            return yield* new ProviderAdapterValidationError({
              provider: PROVIDER,
              operation: "sendTurn",
              issue: "Turn requires non-empty text or attachments.",
            });
          }

          // Decide steering only after the async attachment reads and under the
          // per-thread lock: two concurrent admissions must not both claim to be
          // a fresh non-steering turn and overwrite `activeTurnId`. Active turn
          // => the user is steering, and pi's `steer` behavior folds the message
          // into the current run so the turn id stays stable.
          const isSteering = context.activeTurnId !== undefined;
          const turnId = isSteering ? context.activeTurnId! : TurnId.make(yield* randomUUIDv4);
          context.promptsInFlight += 1;

          return yield* Effect.gen(function* () {
            if (!isSteering) {
              context.activeTurnId = turnId;
              context.mapper.turnId = turnId;
              context.session = {
                ...context.session,
                status: "running",
                activeTurnId: turnId,
                updatedAt: yield* nowIso,
              };
              yield* emit(
                yield* buildEvent(input.threadId, turnId, {
                  type: "turn.started",
                  payload:
                    context.session.model !== undefined ? { model: context.session.model } : {},
                }),
              );
            }

            yield* sendPiCommand(
              context.rpc,
              {
                type: "prompt",
                message,
                ...(images.length > 0 ? { images } : {}),
                ...(isSteering ? { streamingBehavior: "steer" } : {}),
              },
              "prompt",
            ).pipe(
              Effect.tapError((cause) =>
                isSteering ? Effect.void : rollbackRejectedTurn(context, turnId, cause.detail),
              ),
            );

            return {
              threadId: input.threadId,
              turnId,
              resumeCursor: context.session.resumeCursor,
            };
          }).pipe(
            Effect.ensuring(
              Effect.sync(() => {
                context.promptsInFlight = Math.max(0, context.promptsInFlight - 1);
              }),
            ),
          );
        }),
      );

    const interruptTurn: PiAdapterShape["interruptTurn"] = (threadId, turnId) =>
      Effect.gen(function* () {
        const context = yield* requireSession(threadId);
        // A cancellation can be delivered after the turn it targeted already
        // finished; ignore it rather than aborting whatever turn is live now.
        if (turnId !== undefined && context.activeTurnId !== turnId) {
          return;
        }
        if (context.activeTurnId !== undefined) {
          context.mapper.interrupted = true;
        }
        yield* sendPiCommand(context.rpc, { type: "abort" }, "abort", "5 seconds").pipe(
          Effect.ignore,
        );
      });

    const respondToRequest: PiAdapterShape["respondToRequest"] = () => Effect.void;
    const respondToUserInput: PiAdapterShape["respondToUserInput"] = () => Effect.void;

    const readThread: PiAdapterShape["readThread"] = (threadId) =>
      Effect.gen(function* () {
        const context = yield* requireSession(threadId);
        const response = yield* sendPiCommand(
          context.rpc,
          { type: "get_messages" },
          "get_messages",
        );
        const data = asPiRecord(response.data);
        const messages = Array.isArray(data?.messages) ? data.messages : [];
        return { threadId, turns: mapPiMessagesToTurns(messages) };
      });

    const rollbackThread: PiAdapterShape["rollbackThread"] = (threadId, numTurns) =>
      Effect.gen(function* () {
        if (!Number.isInteger(numTurns) || numTurns < 1) {
          return yield* new ProviderAdapterValidationError({
            provider: PROVIDER,
            operation: "rollbackThread",
            issue: "numTurns must be an integer >= 1.",
          });
        }
        yield* requireSession(threadId);
        // pi exposes no in-session rollback primitive; returning the unchanged
        // live history as success would silently keep the turns the caller
        // asked to drop, so fail explicitly like the other adapters that lack
        // provider-side rollback.
        return yield* new ProviderAdapterRequestError({
          provider: PROVIDER,
          method: "thread/rollback",
          detail: "pi RPC sessions do not support provider-side rollback yet.",
        });
      });

    const stopSession: PiAdapterShape["stopSession"] = (threadId) =>
      withThreadLock(
        threadId,
        Effect.gen(function* () {
          const context = yield* requireSession(threadId);
          yield* stopSessionInternal(context);
        }),
      );

    const listSessions: PiAdapterShape["listSessions"] = () =>
      Effect.sync(() => Array.from(sessions.values(), (context) => ({ ...context.session })));

    const hasSession: PiAdapterShape["hasSession"] = (threadId) =>
      Effect.sync(() => {
        const context = sessions.get(threadId);
        return context !== undefined && !context.stopped;
      });

    const stopAll: PiAdapterShape["stopAll"] = () =>
      Effect.forEach(Array.from(sessions.values()), stopSessionInternal, { discard: true });

    yield* Effect.addFinalizer(() =>
      Effect.forEach(Array.from(sessions.values()), stopSessionInternal, { discard: true }).pipe(
        Effect.tap(() => Queue.shutdown(runtimeEvents)),
        Effect.tap(() => managedNativeEventLogger?.close() ?? Effect.void),
        Effect.ignore,
      ),
    );

    return {
      provider: PROVIDER,
      capabilities: { sessionModelSwitch: "in-session" },
      startSession,
      sendTurn,
      interruptTurn,
      respondToRequest,
      respondToUserInput,
      stopSession,
      listSessions,
      hasSession,
      readThread,
      rollbackThread,
      stopAll,
      streamEvents: Stream.fromQueue(runtimeEvents),
    } satisfies PiAdapterShape;
  });
}

type PiRpcResponseLike = {
  readonly success: boolean;
  readonly data?: unknown;
  readonly error?: string;
};
