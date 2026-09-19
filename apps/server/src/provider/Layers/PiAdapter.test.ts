import * as NodeCrypto from "@effect/platform-node/NodeCrypto";
import * as NodeFileSystem from "@effect/platform-node/NodeFileSystem";
import * as NodePath from "@effect/platform-node/NodePath";
import { describe, expect, it } from "@effect/vitest";
import {
  PiSettings,
  ProviderDriverKind,
  type ProviderRuntimeEvent,
  ThreadId,
  TurnId,
} from "@t3tools/contracts";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Queue from "effect/Queue";
import * as Ref from "effect/Ref";
import * as Schema from "effect/Schema";
import * as Scope from "effect/Scope";
import * as Sink from "effect/Sink";
import * as Stream from "effect/Stream";
import { ChildProcessSpawner } from "effect/unstable/process";

import { ServerConfig } from "../../config.ts";
import * as piRuntime from "../piRuntime.ts";
import {
  applyPiEvent,
  createPiEventMapperState,
  makePiAdapter,
  mapPiMessagesToTurns,
  mapPiToolNameToItemType,
  parsePiModelSlug,
} from "./PiAdapter.ts";

const decodePiSettings = Schema.decodeSync(PiSettings);
const encodeFrame = Schema.encodeSync(Schema.fromJsonString(Schema.Unknown));
const frameBytes = (value: unknown): Uint8Array =>
  new TextEncoder().encode(`${encodeFrame(value)}\n`);

interface FakePi {
  readonly spawner: ChildProcessSpawner.ChildProcessSpawner["Service"];
}

/**
 * A pi RPC child whose first `prompt` is rejected (`success:false`, no events)
 * and whose later prompts are accepted and run to completion.
 */
const makeFakePi = (): Effect.Effect<FakePi, never, Scope.Scope> =>
  Effect.gen(function* () {
    const stdout = yield* Queue.unbounded<Uint8Array>();
    const incoming = yield* Queue.unbounded<piRuntime.PiRpcValue>();
    const buffer = yield* Ref.make("");
    const promptCount = yield* Ref.make(0);

    const emit = (value: unknown) => Queue.offer(stdout, frameBytes(value)).pipe(Effect.asVoid);

    const spawner = ChildProcessSpawner.make(() =>
      Effect.sync(() =>
        ChildProcessSpawner.makeHandle({
          pid: ChildProcessSpawner.ProcessId(4242),
          exitCode: Effect.never,
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
                  yield* Queue.offer(incoming, command);
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

    yield* Stream.fromQueue(incoming).pipe(
      Stream.runForEach((command) =>
        Effect.gen(function* () {
          if (command.type === "get_state") {
            yield* emit({
              id: command.id,
              type: "response",
              command: "get_state",
              success: true,
              data: { sessionId: "thread-abc" },
            });
            return;
          }
          if (command.type !== "prompt") {
            return;
          }
          const count = yield* Ref.updateAndGet(promptCount, (n) => n + 1);
          if (count === 1) {
            yield* emit({
              id: command.id,
              type: "response",
              command: "prompt",
              success: false,
              error: "No API key found for pi.",
            });
            return;
          }
          yield* emit({
            id: command.id,
            type: "response",
            command: "prompt",
            success: true,
          });
          yield* emit({ type: "agent_start" });
          yield* emit({ type: "message_start", message: { role: "assistant" } });
          yield* emit({
            type: "message_end",
            message: { role: "assistant", stopReason: "stop" },
          });
          yield* emit({ type: "turn_end", message: { stopReason: "stop" } });
          yield* emit({ type: "agent_end", willRetry: false });
        }),
      ),
      Effect.forkScoped,
    );

    return { spawner };
  });

const adapterServices = Layer.mergeAll(
  NodeCrypto.layer,
  ServerConfig.layerTest(process.cwd(), process.cwd()),
).pipe(Layer.provideMerge(Layer.mergeAll(NodeFileSystem.layer, NodePath.layer)));

const settle = Effect.forEach(Array.from({ length: 20 }), () => Effect.yieldNow, {
  discard: true,
});

describe("mapPiToolNameToItemType", () => {
  it("maps known pi tools onto canonical lifecycle item types", () => {
    expect(mapPiToolNameToItemType("bash")).toBe("command_execution");
    expect(mapPiToolNameToItemType("write")).toBe("file_change");
    expect(mapPiToolNameToItemType("edit")).toBe("file_change");
    expect(mapPiToolNameToItemType("mcp__server__tool")).toBe("mcp_tool_call");
    expect(mapPiToolNameToItemType("web_search")).toBe("web_search");
    expect(mapPiToolNameToItemType("view_image")).toBe("image_view");
    expect(mapPiToolNameToItemType("task")).toBe("collab_agent_tool_call");
    expect(mapPiToolNameToItemType("some_custom_tool")).toBe("dynamic_tool_call");
  });
});

describe("parsePiModelSlug", () => {
  it("splits provider/model and rejects malformed slugs", () => {
    expect(parsePiModelSlug("google/gemini-3-pro-preview")).toEqual({
      provider: "google",
      modelId: "gemini-3-pro-preview",
    });
    expect(parsePiModelSlug("model")).toBeUndefined();
    expect(parsePiModelSlug("/model")).toBeUndefined();
    expect(parsePiModelSlug("provider/")).toBeUndefined();
  });
});

describe("applyPiEvent text and thinking deltas", () => {
  it("assembles deltas by contentIndex and strips non-text content", () => {
    const state = createPiEventMapperState();
    const started = applyPiEvent(state, {
      type: "message_start",
      message: { role: "assistant" },
    });
    expect(started[0]?.type).toBe("item.started");
    expect(started[0]?.payload).toEqual({
      itemType: "assistant_message",
      status: "inProgress",
    });

    expect(
      applyPiEvent(state, {
        type: "message_update",
        assistantMessageEvent: { type: "text_start", contentIndex: 0 },
      }),
    ).toEqual([]);
    expect(
      applyPiEvent(state, {
        type: "message_update",
        assistantMessageEvent: { type: "text_delta", contentIndex: 0, delta: "Hello" },
      }),
    ).toEqual([
      {
        type: "content.delta",
        payload: { streamKind: "assistant_text", delta: "Hello", contentIndex: 0 },
      },
    ]);
    expect(
      applyPiEvent(state, {
        type: "message_update",
        assistantMessageEvent: {
          type: "thinking_delta",
          contentIndex: 1,
          delta: "reasoning",
        },
      }),
    ).toEqual([
      {
        type: "content.delta",
        payload: { streamKind: "reasoning_text", delta: "reasoning", contentIndex: 1 },
      },
    ]);
    expect(
      applyPiEvent(state, {
        type: "message_update",
        assistantMessageEvent: { type: "toolcall_delta", contentIndex: 2, delta: '{"a"' },
      }),
    ).toEqual([]);
  });

  it("emits the authoritative message and usage on message_end", () => {
    const state = createPiEventMapperState();
    applyPiEvent(state, { type: "message_start", message: { role: "assistant" } });
    const emissions = applyPiEvent(state, {
      type: "message_end",
      message: {
        role: "assistant",
        stopReason: "stop",
        usage: { input: 10, output: 4, totalTokens: 14 },
      },
    });
    expect(emissions[0]?.type).toBe("item.completed");
    expect(emissions[1]?.type).toBe("thread.token-usage.updated");
    expect(emissions[1]?.payload).toEqual({
      usage: { usedTokens: 14, inputTokens: 10, lastInputTokens: 10, outputTokens: 4, lastOutputTokens: 4 },
    });
  });
});

describe("applyPiEvent tool lifecycle", () => {
  it("maps tool_execution_start/update/end to item lifecycle events", () => {
    const state = createPiEventMapperState();
    const started = applyPiEvent(state, {
      type: "tool_execution_start",
      toolCallId: "call-1",
      toolName: "bash",
      args: { command: "ls" },
    });
    expect(started).toEqual([
      {
        type: "item.started",
        itemId: "call-1",
        payload: {
          itemType: "command_execution",
          status: "inProgress",
          title: "bash",
          data: { args: { command: "ls" } },
        },
      },
    ]);

    const updated = applyPiEvent(state, {
      type: "tool_execution_update",
      toolCallId: "call-1",
      toolName: "bash",
      partialResult: { content: [] },
    });
    expect(updated[0]?.type).toBe("item.updated");

    const completed = applyPiEvent(state, {
      type: "tool_execution_end",
      toolCallId: "call-1",
      toolName: "bash",
      result: { content: [{ type: "text", text: "ok" }] },
      isError: false,
    });
    expect(completed[0]).toMatchObject({
      type: "item.completed",
      itemId: "call-1",
      payload: { itemType: "command_execution", status: "completed" },
    });

    const failed = applyPiEvent(state, {
      type: "tool_execution_end",
      toolCallId: "call-2",
      toolName: "bash",
      isError: true,
    });
    expect(failed[0]).toMatchObject({
      type: "item.completed",
      payload: { status: "failed" },
    });
  });
});

describe("applyPiEvent turn lifecycle", () => {
  it("completes a turn on agent_end", () => {
    const state = createPiEventMapperState();
    state.turnId = TurnId.make("turn-1");
    const emissions = applyPiEvent(state, { type: "agent_end", willRetry: false });
    expect(emissions).toEqual([
      {
        type: "turn.completed",
        turnId: TurnId.make("turn-1"),
        payload: { state: "completed" },
      },
    ]);
    expect(state.turnId).toBeUndefined();
  });

  it("emits turn.aborted when interrupted", () => {
    const state = createPiEventMapperState();
    state.turnId = TurnId.make("turn-2");
    state.interrupted = true;
    const emissions = applyPiEvent(state, { type: "agent_end" });
    expect(emissions).toEqual([
      {
        type: "turn.aborted",
        turnId: TurnId.make("turn-2"),
        payload: { reason: "Interrupted by user." },
      },
    ]);
    expect(state.interrupted).toBe(false);
  });

  it("waits for retries instead of completing early", () => {
    const state = createPiEventMapperState();
    state.turnId = TurnId.make("turn-3");
    expect(applyPiEvent(state, { type: "agent_end", willRetry: true })).toEqual([]);
    expect(state.turnId).toBe(TurnId.make("turn-3"));
  });

  it("records the stop reason from turn_end", () => {
    const state = createPiEventMapperState();
    state.turnId = TurnId.make("turn-4");
    applyPiEvent(state, { type: "turn_end", message: { stopReason: "error" } });
    const emissions = applyPiEvent(state, { type: "agent_end" });
    expect(emissions[0]?.payload).toEqual({ state: "failed", stopReason: "error" });
  });
});

describe("mapPiMessagesToTurns", () => {
  it("keeps only assistant messages", () => {
    const turns = mapPiMessagesToTurns([
      { role: "user", content: [] },
      { role: "assistant", responseId: "resp-1", content: [] },
      { role: "toolResult", content: [] },
      { role: "assistant", content: [] },
    ]);
    expect(turns).toHaveLength(2);
    expect(turns[0]?.id).toBe(TurnId.make("resp-1"));
    expect(turns[1]?.id).toBe(TurnId.make("pi-turn-2"));
  });
});

describe("makePiAdapter sendTurn rejection", () => {
  it.effect("rolls back a rejected prompt so the next send is a fresh turn, not a steer", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const fake = yield* makeFakePi();
        const adapter = yield* makePiAdapter(
          decodePiSettings({ enabled: true, binaryPath: "pi" }),
        ).pipe(
          Effect.provide(adapterServices),
          Effect.provideService(ChildProcessSpawner.ChildProcessSpawner, fake.spawner),
        );

        const events = yield* Ref.make<ReadonlyArray<ProviderRuntimeEvent>>([]);
        yield* adapter.streamEvents.pipe(
          Stream.runForEach((event) => Ref.update(events, (list) => [...list, event])),
          Effect.forkScoped,
        );

        const threadId = ThreadId.make("thread-pi-reject");
        yield* adapter.startSession({
          provider: ProviderDriverKind.make("pi"),
          threadId,
          cwd: process.cwd(),
          runtimeMode: "full-access",
        });

        const rejected = yield* adapter.sendTurn({ threadId, input: "hello" }).pipe(Effect.result);
        expect(rejected._tag).toBe("Failure");

        yield* settle;
        const afterReject = yield* Ref.get(events);
        const sessions = yield* adapter.listSessions();
        expect(sessions[0]?.status).toBe("ready");
        expect(sessions[0]?.activeTurnId).toBeUndefined();

        const startedAfterReject = afterReject.filter((event) => event.type === "turn.started");
        expect(startedAfterReject).toHaveLength(1);
        const firstTurnId = startedAfterReject[0]?.turnId;
        const failed = afterReject.filter((event) => event.type === "turn.completed");
        expect(failed).toHaveLength(1);
        expect(failed[0]?.payload).toMatchObject({ state: "failed" });

        const second = yield* adapter.sendTurn({ threadId, input: "again" });
        yield* settle;
        const afterSecond = yield* Ref.get(events);
        expect(afterSecond.filter((event) => event.type === "turn.started")).toHaveLength(2);
        expect(second.turnId).not.toBe(firstTurnId);
      }),
    ),
  );
});
