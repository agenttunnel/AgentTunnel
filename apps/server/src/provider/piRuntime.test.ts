import { describe, expect, it } from "@effect/vitest";
import * as Cause from "effect/Cause";
import * as Deferred from "effect/Deferred";
import * as Effect from "effect/Effect";
import * as Exit from "effect/Exit";
import * as Fiber from "effect/Fiber";
import * as Queue from "effect/Queue";
import * as Ref from "effect/Ref";
import * as Schema from "effect/Schema";
import * as Scope from "effect/Scope";
import * as Sink from "effect/Sink";
import * as Stream from "effect/Stream";
import { ChildProcessSpawner } from "effect/unstable/process";

import * as piRuntime from "./piRuntime.ts";

const encodeJson = Schema.encodeSync(Schema.fromJsonString(Schema.Unknown));
const encodeBytes = (value: unknown): Uint8Array =>
  new TextEncoder().encode(`${encodeJson(value)}\n`);

describe("splitPiJsonLines", () => {
  it("splits complete records and preserves the trailing partial", () => {
    const result = piRuntime.splitPiJsonLines('{"a":1}\n{"b":2}\n{"c"');
    expect(result.lines).toEqual(['{"a":1}', '{"b":2}']);
    expect(result.rest).toBe('{"c"');
  });

  it("treats an exhausted buffer as empty rest", () => {
    expect(piRuntime.splitPiJsonLines('{"a":1}\n')).toEqual({
      lines: ['{"a":1}'],
      rest: "",
    });
  });

  it("strips CRLF terminators", () => {
    const result = piRuntime.splitPiJsonLines('{"a":1}\r\n{"b":2}\r\n');
    expect(result.lines).toEqual(['{"a":1}', '{"b":2}']);
    expect(result.rest).toBe("");
  });

  it("does not split on U+2028 inside a JSON string", () => {
    const record = encodeJson({ type: "message", text: "a\u2028b" });
    const result = piRuntime.splitPiJsonLines(`${record}\n`);
    expect(result.lines).toHaveLength(1);
    const decoded = piRuntime.decodePiJsonLine(result.lines[0]!);
    expect(decoded?.text).toBe("a\u2028b");
  });
});

describe("decodePiJsonLine", () => {
  it("ignores blank lines and malformed values", () => {
    expect(piRuntime.decodePiJsonLine("   ")).toBeUndefined();
    expect(piRuntime.decodePiJsonLine("{not json")).toBeUndefined();
    expect(piRuntime.decodePiJsonLine("[1,2,3]")).toBeUndefined();
  });

  it("recognizes responses", () => {
    const value = piRuntime.decodePiJsonLine(
      '{"id":"1","type":"response","command":"get_state","success":true}',
    );
    expect(value).toBeDefined();
    expect(piRuntime.isPiRpcResponse(value!)).toBe(true);
  });
});

describe("piJsonLineStream", () => {
  it("reassembles records split across chunks", () =>
    Effect.gen(function* () {
      const record = encodeJson({ type: "message_update", delta: "hello" });
      const first = new TextEncoder().encode(record.slice(0, 5));
      const second = new TextEncoder().encode(`${record.slice(5)}\n`);
      const decoded = yield* Stream.make(first, second).pipe(
        piRuntime.piJsonLineStream,
        Stream.runCollect,
      );
      expect([...decoded]).toHaveLength(1);
      expect([...decoded][0]?.delta).toBe("hello");
    }).pipe(Effect.runPromise));

  it("handles multiple records in one chunk", () =>
    Effect.gen(function* () {
      const chunk = new TextEncoder().encode(
        `${encodeJson({ type: "a" })}\n${encodeJson({ type: "b" })}\n`,
      );
      const decoded = yield* Stream.make(chunk).pipe(piRuntime.piJsonLineStream, Stream.runCollect);
      expect([...decoded].map((event) => event.type)).toEqual(["a", "b"]);
    }).pipe(Effect.runPromise));

  it("flushes a trailing record that never got a newline", () =>
    Effect.gen(function* () {
      const chunk = new TextEncoder().encode(
        `${encodeJson({ type: "a" })}\n${encodeJson({ type: "tail" })}`,
      );
      const decoded = yield* Stream.make(chunk).pipe(piRuntime.piJsonLineStream, Stream.runCollect);
      expect([...decoded].map((event) => event.type)).toEqual(["a", "tail"]);
    }).pipe(Effect.runPromise));
});

interface FakePi {
  readonly spawner: ChildProcessSpawner.ChildProcessSpawner["Service"];
  readonly incoming: Stream.Stream<piRuntime.PiRpcValue>;
  readonly emit: (value: unknown) => Effect.Effect<void>;
  /** Resolve the exit code without closing stdout. */
  readonly signalExit: (code: number) => Effect.Effect<void>;
  /** Close the stdout pipe, letting the consumer fiber drain and finish. */
  readonly endStdout: Effect.Effect<void>;
  readonly exitWith: (code: number) => Effect.Effect<void>;
}

const makeFakePi = (): Effect.Effect<FakePi, never, Scope.Scope> =>
  Effect.gen(function* () {
    const stdout = yield* Queue.unbounded<Uint8Array, Cause.Done>();
    const incomingQueue = yield* Queue.unbounded<piRuntime.PiRpcValue>();
    const buffer = yield* Ref.make("");
    const exitCode = yield* Deferred.make<number>();

    const spawner = ChildProcessSpawner.make(() =>
      Effect.sync(() =>
        ChildProcessSpawner.makeHandle({
          pid: ChildProcessSpawner.ProcessId(4242),
          exitCode: Deferred.await(exitCode).pipe(
            Effect.map((code) => ChildProcessSpawner.ExitCode(code)),
          ),
          isRunning: Effect.succeed(true),
          kill: () => Effect.void,
          unref: Effect.succeed(Effect.void),
          stdin: Sink.forEach((chunk: Uint8Array) =>
            Effect.gen(function* () {
              const next = (yield* Ref.get(buffer)) + new TextDecoder().decode(chunk);
              const { lines, rest } = piRuntime.splitPiJsonLines(next);
              yield* Ref.set(buffer, rest);
              for (const line of lines) {
                const command = piRuntime.decodePiJsonLine(line);
                if (command !== undefined) {
                  yield* Queue.offer(incomingQueue, command);
                }
              }
            }),
          ),
          stdout: Stream.fromQueue(stdout),
          stderr: Stream.empty,
          all: Stream.empty,
          getInputFd: () => Sink.drain,
          getOutputFd: () => Stream.empty,
        }),
      ),
    );

    return {
      spawner,
      incoming: Stream.fromQueue(incomingQueue),
      emit: (value) => Queue.offer(stdout, encodeBytes(value)).pipe(Effect.asVoid),
      signalExit: (code) => Deferred.succeed(exitCode, code).pipe(Effect.asVoid),
      endStdout: Queue.end(stdout),
      exitWith: (code) =>
        Effect.all([Deferred.succeed(exitCode, code), Queue.end(stdout)]).pipe(Effect.asVoid),
    };
  });

describe("PiRuntimeError", () => {
  it("derives a non-empty message from operation and detail", () => {
    const error = new piRuntime.PiRuntimeError({
      operation: "spawn",
      detail: "Failed to spawn 'pi'.",
    });
    expect(error.message.length).toBeGreaterThan(0);
    expect(error.message).toContain("spawn");
    expect(error.message).toContain("Failed to spawn 'pi'.");
  });

  it("exposes the detail through piRuntimeErrorDetail", () => {
    const error = new piRuntime.PiRuntimeError({
      operation: "encodeCommand",
      detail: "Failed to encode pi command 'get_state'.",
    });
    expect(piRuntime.piRuntimeErrorDetail(error)).toBe(error.detail);
    expect(error.message).toContain(piRuntime.piRuntimeErrorDetail(error));
    expect(piRuntime.isPiRuntimeError(error)).toBe(true);
  });
});

describe("makePiRpcSession", () => {
  it.effect("correlates responses by id and streams other frames", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const fake = yield* makeFakePi();
        const session = yield* piRuntime
          .makePiRpcSession({ binaryPath: "pi", args: ["--mode", "rpc"] })
          .pipe(Effect.provideService(ChildProcessSpawner.ChildProcessSpawner, fake.spawner));

        yield* fake.incoming.pipe(
          Stream.runForEach((command) =>
            command.type === "get_state"
              ? fake.emit({
                  id: command.id,
                  type: "response",
                  command: "get_state",
                  success: true,
                  data: { sessionId: "thread-abc" },
                })
              : Effect.void,
          ),
          Effect.forkScoped,
        );

        const response = yield* session.send({ type: "get_state" });
        expect(response.success).toBe(true);
        expect(piRuntime.asPiRecord(response.data)?.sessionId).toBe("thread-abc");

        const eventsFiber = yield* session.events.pipe(
          Stream.take(1),
          Stream.runCollect,
          Effect.forkScoped,
        );
        yield* fake.emit({ type: "agent_start" });
        const events = yield* Fiber.join(eventsFiber);
        expect([...events][0]?.type).toBe("agent_start");
      }),
    ),
  );

  it.effect("fails outstanding requests when the process exits", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const fake = yield* makeFakePi();
        const session = yield* piRuntime
          .makePiRpcSession({ binaryPath: "pi", args: ["--mode", "rpc"] })
          .pipe(Effect.provideService(ChildProcessSpawner.ChildProcessSpawner, fake.spawner));

        const sendFiber = yield* session.send({ type: "get_state" }).pipe(Effect.forkScoped);
        yield* Effect.yieldNow;
        yield* fake.exitWith(3);

        const exit = yield* session.awaitExit;
        expect(exit.code).toBe(3);
        const sendExit = yield* Fiber.await(sendFiber);
        expect(Exit.isFailure(sendExit)).toBe(true);
      }),
    ),
  );

  it.effect("surfaces a clean exit code", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const fake = yield* makeFakePi();
        const session = yield* piRuntime
          .makePiRpcSession({ binaryPath: "pi", args: ["--mode", "rpc"] })
          .pipe(Effect.provideService(ChildProcessSpawner.ChildProcessSpawner, fake.spawner));
        yield* fake.exitWith(0);
        expect((yield* session.awaitExit).code).toBe(0);
      }),
    ),
  );

  it.effect("rejects sends issued after the process exits", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const fake = yield* makeFakePi();
        const session = yield* piRuntime
          .makePiRpcSession({ binaryPath: "pi", args: ["--mode", "rpc"] })
          .pipe(Effect.provideService(ChildProcessSpawner.ChildProcessSpawner, fake.spawner));

        yield* fake.exitWith(0);
        yield* session.awaitExit;

        const exit = yield* session.send({ type: "get_state" }).pipe(Effect.exit);
        expect(Exit.isFailure(exit)).toBe(true);
      }),
    ),
  );

  it.effect("ends the event stream when the process exits", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const fake = yield* makeFakePi();
        const session = yield* piRuntime
          .makePiRpcSession({ binaryPath: "pi", args: ["--mode", "rpc"] })
          .pipe(Effect.provideService(ChildProcessSpawner.ChildProcessSpawner, fake.spawner));

        const eventsFiber = yield* session.events.pipe(Stream.runCollect, Effect.forkScoped);
        yield* fake.exitWith(0);
        yield* session.awaitExit;

        const events = yield* Fiber.join(eventsFiber);
        expect([...events]).toEqual([]);
      }),
    ),
  );

  it.effect("drains buffered stdout before sweeping pending requests on exit", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const fake = yield* makeFakePi();
        const session = yield* piRuntime
          .makePiRpcSession({ binaryPath: "pi", args: ["--mode", "rpc"] })
          .pipe(Effect.provideService(ChildProcessSpawner.ChildProcessSpawner, fake.spawner));

        const sendFiber = yield* session.send({ type: "get_state" }).pipe(Effect.forkScoped);
        yield* Effect.yieldNow;
        // Signal exit without closing stdout: the correlated response is still
        // buffered, so the sweep must wait for the consumer fiber to drain it.
        yield* fake.signalExit(0);
        yield* Effect.forEach(Array.from({ length: 20 }), () => Effect.yieldNow, {
          discard: true,
        });
        yield* fake.emit({
          id: "pi-1",
          type: "response",
          command: "get_state",
          success: true,
          data: { sessionId: "late" },
        });
        yield* fake.endStdout;

        const response = yield* Fiber.join(sendFiber);
        expect(response.success).toBe(true);
        expect(piRuntime.asPiRecord(response.data)?.sessionId).toBe("late");
        expect((yield* session.awaitExit).code).toBe(0);
      }),
    ),
  );
});
