/**
 * piRuntime — transport for the pi coding agent's RPC mode.
 *
 * `pi --mode rpc` speaks strict JSONL over stdin/stdout: one JSON object per
 * record, delimiting ONLY on LF. This matters because a JSON string may legally
 * contain U+2028 / U+2029 and Node's `readline` would split on those, corrupting
 * the stream. Everything here therefore frames manually instead of using a
 * generic line reader.
 *
 * Commands are written as JSONL; the matching `{type:"response"}` is correlated
 * back by the optional `id` field. Non-response frames (and responses without a
 * live waiter) are surfaced on the event stream. A process exit fails every
 * outstanding request so callers never hang on a dead child.
 *
 * @module piRuntime
 */
import { resolveSpawnCommand } from "@t3tools/shared/shell";
import * as Cause from "effect/Cause";
import * as Deferred from "effect/Deferred";
import * as Effect from "effect/Effect";
import * as Exit from "effect/Exit";
import * as Fiber from "effect/Fiber";
import * as Option from "effect/Option";
import * as Queue from "effect/Queue";
import * as Ref from "effect/Ref";
import * as Schema from "effect/Schema";
import * as Scope from "effect/Scope";
import * as Stream from "effect/Stream";
import { ChildProcess, ChildProcessSpawner } from "effect/unstable/process";

export class PiRuntimeError extends Schema.TaggedErrorClass<PiRuntimeError>()("PiRuntimeError", {
  operation: Schema.String,
  detail: Schema.String,
  cause: Schema.optional(Schema.Defect()),
}) {}

const isPiRuntimeError = Schema.is(PiRuntimeError);

export function piRuntimeErrorDetail(cause: unknown): string {
  if (isPiRuntimeError(cause)) return cause.detail;
  if (cause instanceof Error && cause.message.trim().length > 0) return cause.message.trim();
  return String(cause);
}

/** A decoded JSON object from the pi RPC stream. */
export type PiRpcValue = Record<string, unknown>;

export interface PiRpcResponse extends PiRpcValue {
  readonly type: "response";
  readonly command: string;
  readonly success: boolean;
  readonly data?: unknown;
  readonly error?: string;
  readonly id?: string;
}

export interface PiRpcCommand extends PiRpcValue {
  readonly type: string;
  readonly id?: string;
}

export function isPiRpcResponse(value: PiRpcValue): value is PiRpcResponse {
  return (
    value.type === "response" &&
    typeof value.command === "string" &&
    typeof value.success === "boolean"
  );
}

export function asPiRecord(value: unknown): PiRpcValue | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as PiRpcValue)
    : undefined;
}

/**
 * Split a decoded text buffer into complete LF-delimited records plus the
 * trailing partial record. Strips a single trailing `\r` from each record so
 * CRLF input is accepted; leaves U+2028 / U+2029 untouched, unlike readline.
 */
export function splitPiJsonLines(input: string): {
  readonly lines: ReadonlyArray<string>;
  readonly rest: string;
} {
  const parts = input.split("\n");
  const rest = parts.pop() ?? "";
  const lines = parts.map((line) => (line.endsWith("\r") ? line.slice(0, -1) : line));
  return { lines, rest };
}

const decodePiJsonString = Schema.decodeUnknownOption(Schema.fromJsonString(Schema.Unknown));
const encodePiCommand = Schema.encodeUnknownEffect(Schema.fromJsonString(Schema.Unknown));

/** Parse a single framed record. Blank and malformed records yield undefined. */
export function decodePiJsonLine(line: string): PiRpcValue | undefined {
  const trimmed = line.trim();
  if (trimmed.length === 0) return undefined;
  return Option.match(decodePiJsonString(trimmed), {
    onNone: () => undefined,
    onSome: (value) => asPiRecord(value),
  });
}

/**
 * Decode a byte stream into pi RPC records. Framing state is threaded across
 * chunks; a trailing record without a newline is flushed when the source ends.
 */
export function piJsonLineStream<RE>(
  source: Stream.Stream<Uint8Array, RE>,
): Stream.Stream<PiRpcValue, RE> {
  return source.pipe(
    Stream.decodeText(),
    Stream.mapAccum(
      () => "",
      (buffer, chunk) => {
        const { lines, rest } = splitPiJsonLines(buffer + chunk);
        return [rest, lines] as const;
      },
      {
        onHalt: (rest) => (rest.trim().length === 0 ? [] : [rest]),
      },
    ),
    Stream.flatMap((line) => {
      const decoded = decodePiJsonLine(line);
      return decoded === undefined ? Stream.empty : Stream.succeed(decoded);
    }),
  );
}

export interface PiRpcProcessExit {
  readonly code: number;
  readonly stderr: string;
}

export interface PiRpcSession {
  readonly pid: number | undefined;
  /** Send a command and await its correlated response. */
  readonly send: (command: PiRpcCommand) => Effect.Effect<PiRpcResponse, PiRuntimeError>;
  /** Fire a command without waiting for a response. */
  readonly write: (command: PiRpcCommand) => Effect.Effect<void, PiRuntimeError>;
  /** Non-response frames from the child. */
  readonly events: Stream.Stream<PiRpcValue>;
  /** Resolves once the child exits (successfully or otherwise). */
  readonly awaitExit: Effect.Effect<PiRpcProcessExit>;
}

export interface PiRpcSessionInput {
  readonly binaryPath: string;
  readonly args: ReadonlyArray<string>;
  readonly cwd?: string;
  readonly environment?: NodeJS.ProcessEnv;
}

const makeExitError = (code: number, stderr: string): PiRuntimeError =>
  new PiRuntimeError({
    operation: "session",
    // Keep the surfaced detail bounded rather than forwarding raw CLI output.
    detail: `pi process exited with code ${code} (${stderr.trim().length} bytes on stderr).`,
  });

/**
 * Spawn a pi RPC child bound to the caller's scope. Closing the scope shuts the
 * stdin pump down and kills the child, so a stopped session cannot leak a
 * process. Outstanding requests fail if the process exits first.
 */
export const makePiRpcSession = (
  input: PiRpcSessionInput,
): Effect.Effect<
  PiRpcSession,
  PiRuntimeError,
  ChildProcessSpawner.ChildProcessSpawner | Scope.Scope
> =>
  Effect.gen(function* () {
    const spawner = yield* ChildProcessSpawner.ChildProcessSpawner;
    const runtimeScope = yield* Scope.Scope;
    const spawnCommand = yield* resolveSpawnCommand(
      input.binaryPath,
      input.args,
      input.environment !== undefined ? { env: input.environment } : {},
    );
    const child = yield* spawner
      .spawn(
        ChildProcess.make(spawnCommand.command, spawnCommand.args, {
          shell: spawnCommand.shell,
          ...(input.cwd !== undefined ? { cwd: input.cwd } : {}),
          ...(input.environment !== undefined ? { env: input.environment, extendEnv: true } : {}),
        }),
      )
      .pipe(
        Effect.provideService(Scope.Scope, runtimeScope),
        Effect.mapError(
          (cause) =>
            new PiRuntimeError({
              operation: "spawn",
              detail: `Failed to spawn '${input.binaryPath}': ${piRuntimeErrorDetail(cause)}`,
              cause,
            }),
        ),
      );

    const stderrRef = yield* Ref.make("");
    const pending = yield* Ref.make(
      new Map<string, Deferred.Deferred<PiRpcResponse, PiRuntimeError>>(),
    );
    const inputQueue = yield* Queue.unbounded<Uint8Array>();
    const eventQueue = yield* Queue.unbounded<PiRpcValue, Cause.Done>();
    const exitDeferred = yield* Deferred.make<PiRpcProcessExit>();
    const exitInfo = yield* Ref.make<PiRpcProcessExit | undefined>(undefined);
    const idCounter = yield* Ref.make(0);

    yield* Scope.addFinalizer(
      runtimeScope,
      Effect.gen(function* () {
        yield* Queue.shutdown(inputQueue);
        yield* child.kill({ killSignal: "SIGTERM", forceKillAfter: "2 seconds" });
      }).pipe(Effect.ignore),
    );

    yield* Stream.fromQueue(inputQueue).pipe(
      Stream.run(child.stdin),
      Effect.ignore,
      Effect.forkIn(runtimeScope),
    );

    const handleMessage = (message: PiRpcValue) =>
      Effect.gen(function* () {
        const id = typeof message.id === "string" ? message.id : undefined;
        if (isPiRpcResponse(message) && id !== undefined) {
          const map = yield* Ref.get(pending);
          const waiter = map.get(id);
          if (waiter !== undefined) {
            yield* Ref.update(pending, (current) => {
              const next = new Map(current);
              next.delete(id);
              return next;
            });
            yield* Deferred.succeed(waiter, message);
            return;
          }
        }
        yield* Queue.offer(eventQueue, message);
      });

    const stdoutFiber = yield* child.stdout.pipe(
      piJsonLineStream,
      Stream.runForEach(handleMessage),
      Effect.ignore,
      Effect.forkIn(runtimeScope),
    );
    const stderrFiber = yield* child.stderr.pipe(
      Stream.decodeText(),
      Stream.runForEach((chunk) => Ref.update(stderrRef, (acc) => acc + chunk)),
      Effect.ignore,
      Effect.forkIn(runtimeScope),
    );

    yield* child.exitCode.pipe(
      Effect.exit,
      Effect.flatMap((exit) =>
        Effect.gen(function* () {
          const code = Exit.isSuccess(exit) ? Number(exit.value) : 1;
          // Drain the stdout/stderr consumers before sweeping. The child can
          // exit while frames are still buffered in its pipes; joining the
          // consumer fibers lets those frames correlate their responses and
          // reach the event stream instead of being dropped by the sweep.
          yield* Fiber.join(stdoutFiber).pipe(Effect.ignore);
          yield* Fiber.join(stderrFiber).pipe(Effect.ignore);
          const stderr = yield* Ref.get(stderrRef);
          // Record the exit before sweeping pending so a `send` racing with the
          // sweep is rejected by its own post-registration check instead of
          // waiting forever on a dead child.
          yield* Ref.set(exitInfo, { code, stderr });
          const waiters = yield* Ref.get(pending);
          yield* Ref.set(pending, new Map());
          yield* Effect.forEach(
            [...waiters.values()],
            (waiter) => Deferred.fail(waiter, makeExitError(code, stderr)).pipe(Effect.ignore),
            { discard: true },
          );
          // End the event stream so consumers of `events` finish instead of
          // retaining the session's fiber after the child is gone.
          yield* Queue.end(eventQueue);
          yield* Deferred.succeed(exitDeferred, { code, stderr });
        }),
      ),
      Effect.ignore,
      Effect.forkIn(runtimeScope),
    );

    const write = (command: PiRpcCommand): Effect.Effect<void, PiRuntimeError> =>
      encodePiCommand(command).pipe(
        Effect.mapError(
          (cause) =>
            new PiRuntimeError({
              operation: "encodeCommand",
              detail: `Failed to encode pi command '${command.type}'.`,
              cause,
            }),
        ),
        Effect.flatMap((encoded) =>
          Queue.offer(inputQueue, new TextEncoder().encode(`${encoded}\n`)),
        ),
        Effect.asVoid,
      );

    const removeWaiter = (
      id: string,
      waiter: Deferred.Deferred<PiRpcResponse, PiRuntimeError>,
    ): Effect.Effect<void> =>
      Ref.update(pending, (current) => {
        if (current.get(id) !== waiter) return current;
        const next = new Map(current);
        next.delete(id);
        return next;
      });

    const send = (command: PiRpcCommand): Effect.Effect<PiRpcResponse, PiRuntimeError> =>
      Effect.gen(function* () {
        const exited = yield* Ref.get(exitInfo);
        if (exited !== undefined) {
          return yield* makeExitError(exited.code, exited.stderr);
        }
        const id =
          command.id ?? `pi-${yield* Ref.updateAndGet(idCounter, (counter) => counter + 1)}`;
        const waiter = yield* Deferred.make<PiRpcResponse, PiRuntimeError>();
        yield* Ref.update(pending, (current) => {
          const next = new Map(current);
          next.set(id, waiter);
          return next;
        });
        // The exit handler may have swept between the check above and the
        // registration; re-check so the request fails instead of hanging.
        const racedExit = yield* Ref.get(exitInfo);
        if (racedExit !== undefined) {
          yield* removeWaiter(id, waiter);
          return yield* makeExitError(racedExit.code, racedExit.stderr);
        }
        yield* write({ ...command, id });
        return yield* Deferred.await(waiter).pipe(
          // A timed-out `send` is interrupted while its entry is still pending;
          // drop it so a late response is surfaced on `events` instead of being
          // swallowed by an abandoned waiter.
          Effect.onInterrupt(() => removeWaiter(id, waiter)),
        );
      });

    return {
      pid: Number(child.pid),
      send,
      write,
      events: Stream.fromQueue(eventQueue),
      awaitExit: Deferred.await(exitDeferred),
    } satisfies PiRpcSession;
  });

export interface PiJsonOneShotInput {
  readonly binaryPath: string;
  readonly prompt: string;
  /** Extra CLI args appended after `--mode json -p <prompt>`. */
  readonly args?: ReadonlyArray<string>;
  readonly cwd?: string;
  readonly environment?: NodeJS.ProcessEnv;
}

export interface PiJsonOneShotResult {
  readonly events: ReadonlyArray<PiRpcValue>;
  readonly text: string | undefined;
  readonly stderr: string;
  readonly code: number;
}

function collectPiMessageText(message: PiRpcValue): string {
  const content = message.content;
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  const parts: Array<string> = [];
  for (const part of content) {
    const record = asPiRecord(part);
    if (record?.type === "text" && typeof record.text === "string") {
      parts.push(record.text);
    }
  }
  return parts.join("");
}

/** Best-effort extraction of the last assistant message's text. */
export function extractLastPiAssistantText(events: ReadonlyArray<PiRpcValue>): string | undefined {
  for (let index = events.length - 1; index >= 0; index -= 1) {
    const event = events[index];
    if (event === undefined || event.type !== "message_end") continue;
    const message = asPiRecord(event.message);
    if (message?.role !== "assistant") continue;
    const text = collectPiMessageText(message);
    if (text.length > 0) return text;
  }
  return undefined;
}

/**
 * One-shot `pi --mode json -p <prompt>` helper. Parses the JSONL event stream,
 * returns every frame plus the final assistant text. Intended for callers that
 * only need a single generation (e.g. text generation); it owns its own scope.
 */
export const runPiJsonOneShot = (
  input: PiJsonOneShotInput,
): Effect.Effect<PiJsonOneShotResult, PiRuntimeError, ChildProcessSpawner.ChildProcessSpawner> =>
  Effect.gen(function* () {
    const spawner = yield* ChildProcessSpawner.ChildProcessSpawner;
    const spawnCommand = yield* resolveSpawnCommand(
      input.binaryPath,
      ["--mode", "json", "-p", input.prompt, ...(input.args ?? [])],
      input.environment !== undefined ? { env: input.environment } : {},
    );
    const child = yield* spawner
      .spawn(
        ChildProcess.make(spawnCommand.command, spawnCommand.args, {
          shell: spawnCommand.shell,
          ...(input.cwd !== undefined ? { cwd: input.cwd } : {}),
          ...(input.environment !== undefined ? { env: input.environment, extendEnv: true } : {}),
        }),
      )
      .pipe(
        Effect.mapError(
          (cause) =>
            new PiRuntimeError({
              operation: "runPiJsonOneShot",
              detail: `Failed to spawn '${input.binaryPath}': ${piRuntimeErrorDetail(cause)}`,
              cause,
            }),
        ),
      );

    // Drain stdout and stderr concurrently: reading one to completion first can
    // deadlock when the child fills the other pipe's buffer and blocks.
    const [events, stderr] = yield* Effect.all(
      [
        child.stdout.pipe(
          piJsonLineStream,
          Stream.runCollect,
          Effect.map((chunk) => [...chunk]),
          Effect.mapError(
            (cause) =>
              new PiRuntimeError({
                operation: "runPiJsonOneShot.stdout",
                detail: "Failed to read the pi JSON event stream.",
                cause,
              }),
          ),
        ),
        child.stderr.pipe(
          Stream.decodeText(),
          Stream.mkString,
          Effect.orElseSucceed(() => ""),
        ),
      ],
      { concurrency: "unbounded" },
    );
    const code = yield* child.exitCode.pipe(
      Effect.exit,
      Effect.map((exit) => (Exit.isSuccess(exit) ? Number(exit.value) : 1)),
      Effect.orElseSucceed(() => 1),
    );

    return {
      events,
      text: extractLastPiAssistantText(events),
      stderr,
      code,
    } satisfies PiJsonOneShotResult;
  }).pipe(Effect.scoped);
