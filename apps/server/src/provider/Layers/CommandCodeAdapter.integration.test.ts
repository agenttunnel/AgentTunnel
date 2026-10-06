// @effect-diagnostics nodeBuiltinImport:off
import * as NodePath from "node:path";
import * as NodeOS from "node:os";
import * as NodeFSP from "node:fs/promises";
import * as NodeURL from "node:url";

import * as NodeServices from "@effect/platform-node/NodeServices";
import { assert, it } from "@effect/vitest";
import * as Effect from "effect/Effect";
import * as Fiber from "effect/Fiber";
import * as Layer from "effect/Layer";
import * as Schema from "effect/Schema";
import * as Stream from "effect/Stream";

import { CommandCodeSettings, ProviderDriverKind, ThreadId } from "@t3tools/contracts";

import { ServerConfig } from "../../config.ts";
import { makeCommandCodeAdapter } from "./CommandCodeAdapter.ts";

const decodeCommandCodeSettings = Schema.decodeSync(CommandCodeSettings);

const __dirname = NodePath.dirname(NodeURL.fileURLToPath(import.meta.url));
const mockAgentPath = NodePath.join(__dirname, "../../../scripts/acp-mock-agent.ts");

async function makeMockAgentWrapper(extraEnv?: Record<string, string>) {
  const dir = await NodeFSP.mkdtemp(NodePath.join(NodeOS.tmpdir(), "commandcode-acp-mock-"));
  const wrapperPath = NodePath.join(dir, "fake-command-code.sh");
  const envExports = Object.entries(extraEnv ?? {})
    .map(([key, value]) => `export ${key}=${JSON.stringify(value)}`)
    .join("\n");
  const script = `#!/bin/sh
${envExports}
exec ${JSON.stringify(process.execPath)} ${JSON.stringify(mockAgentPath)} "$@"
`;
  await NodeFSP.writeFile(wrapperPath, script, "utf8");
  await NodeFSP.chmod(wrapperPath, 0o755);
  return wrapperPath;
}

const baseLayer = ServerConfig.layerTest(process.cwd(), {
  prefix: "t3code-commandcode-adapter-test-",
}).pipe(Layer.provideMerge(NodeServices.layer));

it.effect("starts a session and maps the mock ACP prompt flow to runtime events", () =>
  Effect.gen(function* () {
    const wrapperPath = yield* Effect.promise(() => makeMockAgentWrapper());
    const settings = decodeCommandCodeSettings({ binaryPath: wrapperPath });
    const adapter = yield* makeCommandCodeAdapter(settings);
    const threadId = ThreadId.make("commandcode-mock-thread");

    const runtimeEventsFiber = yield* Stream.take(adapter.streamEvents, 9).pipe(
      Stream.runCollect,
      Effect.forkChild,
    );

    const session = yield* adapter.startSession({
      threadId,
      provider: ProviderDriverKind.make("commandcode"),
      cwd: process.cwd(),
      runtimeMode: "full-access",
    });

    assert.equal(session.provider, "commandcode");
    assert.deepStrictEqual(session.resumeCursor, {
      schemaVersion: 1,
      sessionId: "mock-session-1",
    });

    yield* adapter.sendTurn({
      threadId,
      input: "hello mock",
      attachments: [],
    });

    const runtimeEvents = Array.from(yield* Fiber.join(runtimeEventsFiber));
    const types = runtimeEvents.map((event) => event.type);

    for (const type of [
      "session.started",
      "session.state.changed",
      "thread.started",
      "turn.started",
      "item.started",
      "content.delta",
      "turn.plan.updated",
      "item.completed",
      "turn.completed",
    ] as const) {
      assert.include(types, type);
    }

    const delta = runtimeEvents.find((event) => event.type === "content.delta");
    assert.isDefined(delta);
    if (delta?.type === "content.delta") {
      assert.equal(delta.payload.delta, "hello from mock");
    }

    yield* adapter.stopSession(threadId);
  }).pipe(Effect.provide(baseLayer), Effect.scoped),
);
