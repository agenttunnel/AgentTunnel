import * as NodeServices from "@effect/platform-node/NodeServices";
import { it } from "@effect/vitest";
import * as Effect from "effect/Effect";
import * as Fiber from "effect/Fiber";
import * as FileSystem from "effect/FileSystem";
import * as Path from "effect/Path";
import * as NodeAssert from "node:assert/strict";

import {
  makeAgentTunnelInheritedContext,
  normalizeContextLearning,
  parseRoleStats,
  type AgentTunnelDispatchOutcome,
} from "./InheritedContextStore.ts";

const makeStore = Effect.gen(function* () {
  const fs = yield* FileSystem.FileSystem;
  const path = yield* Path.Path;
  const contextDir = yield* fs.makeTempDirectoryScoped({
    prefix: "t3-orchestrator-context-",
  });
  return {
    contextDir,
    guidePath: path.join(contextDir, "GUIDE.md"),
    logPath: path.join(contextDir, "LOG.md"),
    rulesDir: path.join(contextDir, "rules"),
    lockPath: path.join(contextDir, ".lock"),
    store: yield* makeAgentTunnelInheritedContext(contextDir),
  };
});

const attribution = {
  threadId: "thread-1",
  workflowId: "workflow-1",
  projectCwd: "/code/t3code",
  status: "completed" as const,
};

it.layer(NodeServices.layer)("InheritedContextStore", (it) => {
  it.effect("seeds the hidden folder and renders the guide plus rules for prompts", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const { store, contextDir } = yield* makeStore;
        const rendered = yield* store.renderForPrompt();

        NodeAssert.match(rendered, /Orchestrator inherited context/);
        NodeAssert.match(rendered, /### rules\/design-decisions\.md/);
        NodeAssert.match(rendered, /### rules\/model-fit\.md/);
        NodeAssert.match(rendered, /### rules\/model-quality\.md/);

        const fs = yield* FileSystem.FileSystem;
        NodeAssert.ok(yield* fs.exists(contextDir));
        const names = yield* fs.readDirectory(contextDir);
        NodeAssert.deepEqual([...names].sort(), ["GUIDE.md", "LOG.md", "rules"].sort());
      }),
    ),
  );

  it.effect("appends dated, attributed entries to the matching rules file", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const { store, guidePath, logPath, rulesDir, lockPath } = yield* makeStore;
        const fs = yield* FileSystem.FileSystem;
        const path = yield* Path.Path;

        const written = yield* store.record({
          learnings: [
            { category: "model-fit", entry: "codex handled the refactor well." },
            { category: "model-fit", entry: "pi was slow on the test sweep." },
            { category: "design-decisions", entry: "Prefers small PRs.\nWith one concern." },
          ],
          attribution,
        });
        NodeAssert.equal(written.learnings, 3);
        NodeAssert.equal(written.candidates, 0);

        const modelFit = yield* fs.readFileString(path.join(rulesDir, "model-fit.md"));
        NodeAssert.match(modelFit, /## \d{4}-\d{2}-\d{2}T[\d:.]+Z · completed/);
        NodeAssert.match(
          modelFit,
          /From thread `thread-1`, project `t3code`, workflow `workflow-1`/,
        );
        NodeAssert.match(modelFit, /- codex handled the refactor well\./);
        NodeAssert.match(modelFit, /- pi was slow on the test sweep\./);

        const design = yield* fs.readFileString(path.join(rulesDir, "design-decisions.md"));
        NodeAssert.match(design, /- Prefers small PRs\. With one concern\./);

        const log = yield* fs.readFileString(logPath);
        NodeAssert.match(log, /workflow-1/);
        NodeAssert.match(log, /3 entries \(model-fit ×2, design-decisions ×1\)/);

        const guide = yield* fs.readFileString(guidePath);
        NodeAssert.match(guide, /`rules\/model-fit\.md`/);

        NodeAssert.equal(yield* fs.exists(lockPath), false);
      }),
    ),
  );

  it.effect("serializes concurrent flushes through the context semaphore", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const { store, rulesDir } = yield* makeStore;
        const fs = yield* FileSystem.FileSystem;
        const path = yield* Path.Path;

        const first = yield* store
          .record({
            learnings: [{ category: "model-quality", entry: "first concurrent entry" }],
            attribution: { ...attribution, threadId: "thread-a" },
          })
          .pipe(Effect.forkChild);
        const second = yield* store
          .record({
            learnings: [{ category: "model-quality", entry: "second concurrent entry" }],
            attribution: { ...attribution, threadId: "thread-b" },
          })
          .pipe(Effect.forkChild);
        NodeAssert.equal((yield* Fiber.join(first)).learnings, 1);
        NodeAssert.equal((yield* Fiber.join(second)).learnings, 1);

        const contents = yield* fs.readFileString(path.join(rulesDir, "model-quality.md"));
        NodeAssert.match(contents, /first concurrent entry/);
        NodeAssert.match(contents, /second concurrent entry/);
        NodeAssert.match(contents, /thread-a/);
        NodeAssert.match(contents, /thread-b/);
      }),
    ),
  );

  it.effect("ignores invalid learnings and creates files for new categories", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const { store, rulesDir } = yield* makeStore;
        const fs = yield* FileSystem.FileSystem;
        const path = yield* Path.Path;

        NodeAssert.deepEqual(
          yield* store.record({
            learnings: [
              { category: "!!!", entry: "no category" },
              { category: "model-fit", entry: "   " },
            ],
            attribution,
          }),
          { learnings: 0, candidates: 0 },
        );

        NodeAssert.deepEqual(
          yield* store.record({
            learnings: [
              { category: "Release Hygiene", entry: "Run the focused suite before committing." },
            ],
            attribution,
          }),
          { learnings: 1, candidates: 0 },
        );
        const created = yield* fs.readFileString(path.join(rulesDir, "release-hygiene.md"));
        NodeAssert.match(created, /Run the focused suite before committing\./);

        NodeAssert.equal(normalizeContextLearning("!!!", "x"), null);
        NodeAssert.equal(normalizeContextLearning("model-fit", "  "), null);
        const clipped = normalizeContextLearning("model-fit", "x".repeat(3_000));
        NodeAssert.ok(clipped);
        NodeAssert.equal(clipped.entry.length, 1_200);
        NodeAssert.ok(clipped.entry.endsWith("…"));
      }),
    ),
  );

  it.effect("folds dispatch outcomes into per-role candidates across workflows", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const { contextDir, store } = yield* makeStore;
        const fs = yield* FileSystem.FileSystem;
        const path = yield* Path.Path;
        const statsPath = path.join(contextDir, "role-stats.json");

        const executorDispatch = (
          overrides: Partial<AgentTunnelDispatchOutcome> = {},
        ): AgentTunnelDispatchOutcome => ({
          role: "executor",
          harness: "opencode",
          model: "qwen/qwen3-coder-27b",
          status: "completed",
          durationMs: 1_000,
          totalTokens: 1_000,
          inputTokens: 900,
          outputTokens: 100,
          cachedInputTokens: 0,
          reasoningOutputTokens: 0,
          ...overrides,
        });

        const first = yield* store.record({
          learnings: [],
          dispatches: [
            executorDispatch(),
            executorDispatch({ status: "failed", totalTokens: 3_000, costUsd: 0.02 }),
            {
              role: "tester",
              harness: "pi",
              status: "completed",
              durationMs: 500,
              totalTokens: 500,
              inputTokens: 400,
              outputTokens: 100,
              cachedInputTokens: 0,
              reasoningOutputTokens: 0,
            },
            executorDispatch({ role: "   " }),
          ],
          attribution,
        });
        NodeAssert.equal(first.candidates, 3);

        // A later workflow merges into the same candidate instead of replacing it.
        yield* store.record({
          learnings: [],
          dispatches: [executorDispatch({ totalTokens: 2_000, costUsd: 0.01 })],
          attribution: { ...attribution, workflowId: "workflow-2" },
        });

        const stats = parseRoleStats(yield* fs.readFileString(statsPath), "");
        const candidate = stats.roles["executor"]?.[0];
        NodeAssert.equal(stats.roles["executor"]?.length, 1);
        NodeAssert.equal(candidate?.tasks, 3);
        NodeAssert.equal(candidate?.totalTokens, 6_000);
        NodeAssert.equal(stats.roles["tester"]?.length, 1);

        const rendered = yield* store.renderForPrompt();
        NodeAssert.match(rendered, /## Recorded role candidates/);
        NodeAssert.match(
          rendered,
          /\| executor \| opencode \| qwen\/qwen3-coder-27b \| 3 \| 2 \| 1 \| 2,000 \| 6,000 \| \$0\.03 \|/,
        );
        NodeAssert.match(rendered, /\| tester \| pi \| default \| 1 \| 1 \| 0 \|/);
      }),
    ),
  );

  it.effect("rebuilds a corrupt candidate ledger instead of failing the flush", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const { contextDir, store } = yield* makeStore;
        const fs = yield* FileSystem.FileSystem;
        const path = yield* Path.Path;
        const statsPath = path.join(contextDir, "role-stats.json");
        yield* fs.writeFileString(statsPath, "{ not json");

        const written = yield* store.record({
          learnings: [],
          dispatches: [
            {
              role: "reviewer",
              harness: "claude",
              status: "completed",
              durationMs: 10,
              totalTokens: 42,
              inputTokens: 40,
              outputTokens: 2,
              cachedInputTokens: 0,
              reasoningOutputTokens: 0,
            },
          ],
          attribution,
        });
        NodeAssert.equal(written.candidates, 1);

        const stats = parseRoleStats(yield* fs.readFileString(statsPath), "");
        NodeAssert.equal(stats.roles["reviewer"]?.[0]?.tasks, 1);
      }),
    ),
  );
});
