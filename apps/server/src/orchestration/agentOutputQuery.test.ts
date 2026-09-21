// @effect-diagnostics nodeBuiltinImport:off
import * as NodeFS from "node:fs";
import * as NodeOS from "node:os";
import * as NodePath from "node:path";

import * as NodeServices from "@effect/platform-node/NodeServices";
import { it } from "@effect/vitest";
import { OrchestrationAgentLogEntry } from "@t3tools/contracts";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Schema from "effect/Schema";
import { afterAll, assert, describe } from "vite-plus/test";

import * as ServerConfig from "../config.ts";
import { encodeAgentLogEntry } from "../provider/Layers/AgentTunnelAdapter.ts";
import { agentOutputRoot, readAgentOutput } from "./agentOutputQuery.ts";

const decodeAgentLogEntry = Schema.decodeSync(Schema.fromJsonString(OrchestrationAgentLogEntry));

const baseDir = NodeFS.mkdtempSync(NodePath.join(NodeOS.tmpdir(), "t3-agent-output-test-"));
const providerLogsDir = NodePath.join(baseDir, "userdata", "logs", "provider");
const root = agentOutputRoot(providerLogsDir);
const logPath = NodePath.join(root, "thread-1", "agent_1.log");
NodeFS.mkdirSync(NodePath.dirname(logPath), { recursive: true });
NodeFS.writeFileSync(
  logPath,
  [
    JSON.stringify({ kind: "text", text: "line one" }),
    JSON.stringify({ kind: "reasoning", text: "weighing options" }),
    JSON.stringify({
      kind: "tool",
      tool: "bash",
      title: "echo hello",
      status: "completed",
      output: "hello\n",
    }),
    "not json at all",
    "",
  ].join("\n"),
);
const outside = NodePath.join(baseDir, "outside.log");
NodeFS.writeFileSync(outside, "evil\n");
const rawPath = logPath.replace(/\.log$/, ".raw.log");
NodeFS.writeFileSync(
  rawPath,
  ['{"type":"text","part":{"text":"line one"}}', "", "not json raw", ""].join("\n"),
);
const noRawPath = NodePath.join(root, "thread-1", "agent_2.log");
NodeFS.writeFileSync(noRawPath, `${JSON.stringify({ kind: "text", text: "second" })}\n`);
const link = NodePath.join(root, "sneaky.log");
try {
  NodeFS.symlinkSync(outside, link);
} catch (error) {
  if ((error as NodeJS.ErrnoException).code !== "EEXIST") {
    throw error;
  }
}
if (!NodeFS.lstatSync(link).isSymbolicLink()) {
  throw new Error("test setup: sneaky.log must be a symlink");
}

afterAll(() => {
  NodeFS.rmSync(baseDir, { recursive: true, force: true });
});

const TestLayer = ServerConfig.ServerConfig.layerTest(process.cwd(), baseDir).pipe(
  Layer.provideMerge(NodeServices.layer),
);

describe("readAgentOutput containment", () => {
  it.effect("serves parsed log entries under the outputs root", () =>
    Effect.gen(function* () {
      const result = yield* readAgentOutput({ outputPath: logPath });
      assert.deepEqual(result.entries, [
        { kind: "text", text: "line one" },
        { kind: "reasoning", text: "weighing options" },
        {
          kind: "tool",
          tool: "bash",
          title: "echo hello",
          status: "completed",
          output: "hello\n",
        },
        { kind: "raw", text: "not json at all" },
      ]);
      assert.equal(result.truncated, false);
    }).pipe(Effect.provide(TestLayer)),
  );

  it.effect("returns the sibling raw stream when present, and omits it otherwise", () =>
    Effect.gen(function* () {
      const withRaw = yield* readAgentOutput({ outputPath: logPath });
      assert.deepEqual(withRaw.raw, {
        contents: '{"type":"text","part":{"text":"line one"}}\n\nnot json raw\n',
        truncated: false,
      });
      const withoutRaw = yield* readAgentOutput({ outputPath: noRawPath });
      assert.equal(withoutRaw.raw, undefined);
    }).pipe(Effect.provide(TestLayer)),
  );

  it.effect("rejects relative and non-log paths", () =>
    Effect.gen(function* () {
      const relative = yield* Effect.exit(readAgentOutput({ outputPath: "agent.log" }));
      assert.equal(relative._tag, "Failure");
      const nonLog = yield* Effect.exit(
        readAgentOutput({ outputPath: logPath.replace(".log", ".txt") }),
      );
      assert.equal(nonLog._tag, "Failure");
    }).pipe(Effect.provide(TestLayer)),
  );

  it.effect("rejects paths outside the root and symlink escapes", () =>
    Effect.gen(function* () {
      const escaped = yield* Effect.exit(readAgentOutput({ outputPath: outside }));
      assert.equal(escaped._tag, "Failure");
      const sneaky = yield* readAgentOutput({ outputPath: link }).pipe(
        Effect.flip,
        Effect.map((error) => error.reason),
      );
      assert.equal(sneaky, "outside-root");
    }).pipe(Effect.provide(TestLayer)),
  );
});

describe("usage entry timestamps", () => {
  const AT = "2026-08-14T09:05:30.000Z";

  it.effect("round-trips the time a usage entry was persisted, and still reads older ones", () =>
    Effect.gen(function* () {
      const usageLogPath = NodePath.join(root, "thread-1", "agent_usage.log");
      NodeFS.writeFileSync(
        usageLogPath,
        [
          // As the adapter writes it now: stamped with the persistence time.
          encodeAgentLogEntry({ kind: "usage", totalTokens: 1_200, inputTokens: 1_000 }, AT),
          // As an older adapter wrote it: no stamp to read.
          '{"kind":"usage","totalTokens":500}\n',
        ].join(""),
      );

      const result = yield* readAgentOutput({ outputPath: usageLogPath });

      assert.deepEqual(result.entries, [
        { kind: "usage", totalTokens: 1_200, inputTokens: 1_000, at: AT },
        { kind: "usage", totalTokens: 500 },
      ]);
      const stamped = result.entries[0];
      if (stamped?.kind !== "usage") {
        throw new Error("expected the first entry to decode as usage");
      }
      assert.equal(stamped.at, AT);
      assert.equal(Number.isNaN(Date.parse(stamped.at ?? "")), false);
    }).pipe(Effect.provide(TestLayer)),
  );

  it("stamps usage entries only", () => {
    assert.deepEqual(
      decodeAgentLogEntry(encodeAgentLogEntry({ kind: "text", text: "working" }, AT)),
      { kind: "text", text: "working" },
    );
  });
});
