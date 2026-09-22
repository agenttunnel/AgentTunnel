import * as NodeServices from "@effect/platform-node/NodeServices";
import { it } from "@effect/vitest";
import * as Effect from "effect/Effect";
import * as FileSystem from "effect/FileSystem";
import * as Path from "effect/Path";
import * as NodeAssert from "node:assert/strict";

import {
  makeAgentTunnelThreadContext,
  threadContextResume,
  threadResumeNotice,
  type AgentTunnelThreadContext,
  type AgentTunnelThreadContextStore,
  type AgentTunnelThreadRunStatus,
} from "./ThreadContextStore.ts";

const makeStore = Effect.gen(function* () {
  const fs = yield* FileSystem.FileSystem;
  const path = yield* Path.Path;
  const contextDir = yield* fs.makeTempDirectoryScoped({ prefix: "t3-thread-context-" });
  return {
    contextDir,
    threadsDir: path.join(contextDir, "threads"),
    fileFor: (threadId: string) => path.join(contextDir, "threads", `${threadId}.json`),
    store: yield* makeAgentTunnelThreadContext(contextDir),
  };
});

const record = (
  store: AgentTunnelThreadContextStore,
  input: {
    readonly task: string;
    readonly summary: string;
    readonly status?: AgentTunnelThreadRunStatus;
    readonly sharedContext?: string;
  },
) => store.record({ threadId: "thread-1", status: "completed", ...input });

it.layer(NodeServices.layer)("ThreadContextStore", (it) => {
  it.effect("round-trips a turn's notebook and history through the thread file", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const { store, fileFor } = yield* makeStore;
        const fs = yield* FileSystem.FileSystem;

        NodeAssert.equal(yield* store.load("thread-1"), null);

        yield* record(store, {
          task: "Ship the durable context",
          summary: "Context survives restarts.",
          sharedContext: "### user_goal\n\nKeep the thread's context.",
        });

        const loaded = yield* store.load("thread-1");
        NodeAssert.ok(loaded);
        NodeAssert.equal(loaded.threadId, "thread-1");
        NodeAssert.equal(loaded.sharedContext, "### user_goal\n\nKeep the thread's context.");
        NodeAssert.deepEqual(
          loaded.runs.map((run) => [run.task, run.status, run.summary]),
          [["Ship the durable context", "completed", "Context survives restarts."]],
        );
        NodeAssert.match(loaded.updatedAt, /^\d{4}-\d{2}-\d{2}T[\d:.]+Z$/);

        // The file is real, inspectable JSON, not a process-local blob.
        const raw = yield* fs.readFileString(fileFor("thread-1"));
        NodeAssert.match(raw, /"sharedContext":"### user_goal/);
      }),
    ),
  );

  it.effect("keeps every turn's history, newest last, and never drops the notebook", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const { store } = yield* makeStore;

        yield* record(store, { task: "first", summary: "done", sharedContext: "### goal\n\none" });
        yield* record(store, { task: "second", summary: "done", status: "failed" });

        const loaded = yield* store.load("thread-1");
        NodeAssert.ok(loaded);
        NodeAssert.deepEqual(
          loaded.runs.map((run) => `${run.task}:${run.status}`),
          ["first:completed", "second:failed"],
        );
        // A turn that crashed carries no notebook, so the previous one stays.
        NodeAssert.equal(loaded.sharedContext, "### goal\n\none");
      }),
    ),
  );

  it.effect("caps stored history at the newest 50 turns", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const { store } = yield* makeStore;
        for (let index = 0; index < 55; index += 1) {
          yield* record(store, { task: `turn ${index}`, summary: `summary ${index}` });
        }
        const loaded = yield* store.load("thread-1");
        NodeAssert.ok(loaded);
        NodeAssert.equal(loaded.runs.length, 50);
        NodeAssert.equal(loaded.runs[0]?.task, "turn 5");
        NodeAssert.equal(loaded.runs.at(-1)?.task, "turn 54");
      }),
    ),
  );

  it.effect("clips an oversized notebook to its newest text and trims long summaries", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const { store } = yield* makeStore;
        yield* record(store, {
          task: "t".repeat(1_000),
          summary: "s".repeat(1_000),
          sharedContext: `OLDEST\n${"x".repeat(30_000)}\nNEWEST`,
        });

        const loaded = yield* store.load("thread-1");
        NodeAssert.ok(loaded);
        NodeAssert.ok(loaded.sharedContext.length <= 24_000);
        NodeAssert.ok(loaded.sharedContext.startsWith("…"));
        NodeAssert.ok(loaded.sharedContext.endsWith("NEWEST"));
        NodeAssert.ok(!loaded.sharedContext.includes("OLDEST"));
        NodeAssert.equal(loaded.runs[0]?.summary.length, 600);
        NodeAssert.equal(loaded.runs[0]?.task.length, 400);
      }),
    ),
  );

  it.effect("degrades to an empty resume when the stored file is unreadable", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const { store, fileFor } = yield* makeStore;
        const fs = yield* FileSystem.FileSystem;
        const path = yield* Path.Path;

        yield* fs.makeDirectory(path.dirname(fileFor("thread-1")), { recursive: true });
        yield* fs.writeFileString(fileFor("thread-1"), "{not json");
        NodeAssert.equal(yield* store.load("thread-1"), null);

        // A shape we do not recognise is not a crash either.
        yield* fs.writeFileString(fileFor("thread-1"), '{"threadId":"thread-1"}');
        NodeAssert.equal(yield* store.load("thread-1"), null);
      }),
    ),
  );

  it.effect("keeps thread ids inside the context directory", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const { store, threadsDir, contextDir } = yield* makeStore;
        const fs = yield* FileSystem.FileSystem;

        yield* store.record({
          threadId: "../../escaped",
          task: "escape attempt",
          status: "completed",
          summary: "nope",
        });
        const entries = yield* fs.readDirectory(threadsDir);
        NodeAssert.deepEqual(entries, [".._.._escaped.json"]);
        NodeAssert.equal(yield* fs.exists(`${contextDir}/../escaped.json`), false);
      }),
    ),
  );

  it.effect("logs instead of failing when the record cannot be written", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const fs = yield* FileSystem.FileSystem;
        const path = yield* Path.Path;
        const contextDir = yield* fs.makeTempDirectoryScoped({ prefix: "t3-thread-context-" });
        // A file where the threads directory belongs: the write cannot succeed.
        yield* fs.writeFileString(path.join(contextDir, "threads"), "blocked");
        const store = yield* makeAgentTunnelThreadContext(contextDir);

        yield* store.record({
          threadId: "thread-1",
          task: "still recorded",
          status: "completed",
          summary: "the turn must not fail",
        });
        NodeAssert.equal(yield* store.load("thread-1"), null);
      }),
    ),
  );
});

it("threadContextResume hands a resumed session the thread's newest runs and notebook", () => {
  const prior: AgentTunnelThreadContext = {
    threadId: "thread-1",
    updatedAt: "2026-01-01T00:00:00.000Z",
    sharedContext: "### decisions\n\n- chose the file store",
    runs: Array.from({ length: 12 }, (_, index) => ({
      at: "2026-01-01T00:00:00.000Z",
      task: `task ${index}`,
      status: "completed" as const,
      summary: `summary ${index}`,
    })),
  };

  const resumed = threadContextResume(prior);
  NodeAssert.equal(resumed.threadHistory.length, 10);
  NodeAssert.deepEqual(resumed.threadHistory[0], { task: "task 2", summary: "summary 2" });
  NodeAssert.equal(resumed.sharedContext, "### decisions\n\n- chose the file store");

  NodeAssert.deepEqual(threadContextResume(null), {
    threadHistory: [],
    sharedContext: null,
  });
  NodeAssert.equal(threadContextResume({ ...prior, sharedContext: "   " }).sharedContext, null);
});

it("threadResumeNotice prints where a resumed thread stands, or nothing for a new thread", () => {
  NodeAssert.equal(threadResumeNotice(null), null);

  const prior: AgentTunnelThreadContext = {
    threadId: "thread-1",
    updatedAt: "2026-01-01T00:00:00.000Z",
    sharedContext: [
      "### next_steps",
      "",
      "- Attach the screenshots",
      "- Merge the PR",
      "- Watch the release",
      "- This fourth step stays in the notebook",
    ].join("\n"),
    runs: [
      {
        at: "2026-01-01T00:00:00.000Z",
        task: "Ship the durable context",
        status: "completed",
        summary: "Context survives restarts.",
      },
    ],
  };

  const notice = threadResumeNotice(prior) ?? "";
  NodeAssert.match(notice, /Resumed with 1 prior turn of context\./);
  NodeAssert.match(notice, /\*\*Last turn:\*\* Context survives restarts\./);
  NodeAssert.match(notice, /- Attach the screenshots/);
  NodeAssert.match(notice, /- Watch the release/);
  NodeAssert.doesNotMatch(notice, /fourth step/);
});
