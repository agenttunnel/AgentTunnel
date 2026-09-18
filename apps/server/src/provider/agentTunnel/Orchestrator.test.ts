import { AgentTunnelSettings } from "@t3tools/contracts";
import { it } from "@effect/vitest";
import * as Effect from "effect/Effect";
import * as Fiber from "effect/Fiber";
import * as Schema from "effect/Schema";
import * as Stream from "effect/Stream";
import * as TestClock from "effect/testing/TestClock";
import * as NodeAssert from "node:assert/strict";

import {
  AgentTunnelProcessError,
  type AgentTunnelEvent,
  type AgentTunnelProcessHandle,
  type AgentTunnelProcessRunner,
  runAgentTunnel,
} from "./Orchestrator.ts";

const decodeSettings = Schema.decodeSync(AgentTunnelSettings);

it.effect("runs independent pipelines in parallel and gates dependent work", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const events: AgentTunnelEvent[] = [];
      const settings = decodeSettings({
        orchestratorHarness: "fake-orchestrator",
        executorHarness: "fake-executor",
        reviewerHarness: "fake-reviewer",
        maxOrchestratorPasses: 3,
      });
      const process: AgentTunnelProcessRunner = {
        spawn: (spec) => {
          const lines = spec.prompt.includes("Summary pass")
            ? ['__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"reviewed"}}']
            : spec.id.startsWith("orchestrator")
              ? [
                  '__AGENTTUNNEL_TOOL__ {"name":"update_context","arguments":{"key":"goal","value":"test workflow"}}',
                  '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Research A","pipeline":"research"}}',
                  '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Research B","pipeline":"research"}}',
                  '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"reviewer","task":"Review the research","pipeline":"review","mode":"sequential","depends_on":["research"],"context":["goal"]}}',
                  '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"tasks dispatched"}}',
                ]
              : [
                  '__AGENTTUNNEL_TOOL__ {"name":"note_finding","arguments":{"key":"findings","value":"work complete"}}',
                  '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"complete"}}',
                ];
          return Effect.succeed({
            pid: 1,
            lines: Stream.fromIterable(lines),
            exitCode: Effect.succeed(0),
            kill: Effect.void,
          } satisfies {
            readonly pid: number;
            readonly lines: Stream.Stream<string, AgentTunnelProcessError>;
            readonly exitCode: Effect.Effect<number, AgentTunnelProcessError>;
            readonly kill: Effect.Effect<void, AgentTunnelProcessError>;
          });
        },
      };

      const result = yield* runAgentTunnel({
        threadId: "thread-1",
        workflowId: "workflow-1",
        userTask: "test workflow",
        cwd: "/tmp",
        model: "test-model",
        settings,
        process,
        emit: (event) => Effect.sync(() => events.push(event)),
        isCancelled: Effect.succeed(false),
      });

      NodeAssert.equal(result.status, "completed");
      NodeAssert.equal(events[0]?.type, "workflow.started");
      NodeAssert.equal(events.filter((event) => event.type === "agent.started").length, 3);
      NodeAssert.equal(events.filter((event) => event.type === "agent.completed").length, 3);
      NodeAssert.equal(events.at(-1)?.type, "workflow.completed");
      const progress = events.find(
        (event): event is Extract<AgentTunnelEvent, { type: "workflow.progress" }> =>
          event.type === "workflow.progress" &&
          event.phases.some((phase) => phase.title === "review"),
      );
      NodeAssert.ok(progress);
      NodeAssert.equal(
        progress.phases.find((phase) => phase.title === "review")?.dependsOn[0],
        "research",
      );
    }),
  ),
);

it.effect("fails a turn when an orchestrator pass hangs past the timeout", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const events: AgentTunnelEvent[] = [];
      const settings = decodeSettings({
        orchestratorHarness: "opencode",
        executorHarness: "opencode",
        reviewerHarness: "opencode",
      });
      const process: AgentTunnelProcessRunner = {
        spawn: () =>
          Effect.succeed({
            pid: 1,
            lines: Stream.never,
            exitCode: Effect.never,
            kill: Effect.void,
          } satisfies AgentTunnelProcessHandle),
      };

      const fiber = yield* runAgentTunnel({
        threadId: "thread-1",
        workflowId: "workflow-1",
        userTask: "hang on the first planning pass",
        cwd: "/tmp",
        settings,
        process,
        emit: (event) => Effect.sync(() => events.push(event)),
        isCancelled: Effect.succeed(false),
        orchestratorPassTimeout: "50 millis",
      }).pipe(Effect.forkChild);

      yield* TestClock.adjust("50 millis");

      const result = yield* Fiber.join(fiber);

      NodeAssert.equal(result.status, "failed");
      NodeAssert.match(result.summary, /timed out/);
      NodeAssert.equal(events.at(-1)?.type, "workflow.completed");
    }),
  ),
);

/**
 * Shared fake harness for the markdown-shape tests: the orchestrator plans a
 * two-executor pipeline plus a dependent reviewer pipeline, and each executor
 * emits one prose line before calling done. The stray prose is what opens the
 * `<details>` blocks whose closing adjacency used to swallow following
 * markdown lines.
 */
const runFakeWorkflow = (options: { readonly failTaskIds?: ReadonlyArray<string> } = {}) =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      maxOrchestratorPasses: 3,
    });
    const failTaskIds = new Set(options.failTaskIds ?? []);
    const process: AgentTunnelProcessRunner = {
      spawn: (spec) => {
        const lines = spec.prompt.includes("Summary pass")
          ? [
              "Reviewing the completed work.",
              '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"reviewed"}}',
            ]
          : spec.id.startsWith("orchestrator")
            ? [
                "**Planning** the work.",
                '__AGENTTUNNEL_TOOL__ {"name":"update_context","arguments":{"key":"goal","value":"test workflow"}}',
                '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Research A","pipeline":"research"}}',
                '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Research B","pipeline":"research"}}',
                '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"reviewer","task":"Review the research","pipeline":"review","mode":"sequential","depends_on":["research"]}}',
                '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"tasks dispatched"}}',
              ]
            : [
                "**Working** on `the` task.",
                '__AGENTTUNNEL_TOOL__ {"name":"note_finding","arguments":{"key":"findings","value":"work complete"}}',
                '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"complete"}}',
              ];
        const failed = [...failTaskIds].some((taskId) => spec.id.endsWith(taskId));
        return Effect.succeed({
          pid: 1,
          lines: Stream.fromIterable(lines),
          exitCode: Effect.succeed(failed ? 1 : 0),
          kill: Effect.void,
        } satisfies AgentTunnelProcessHandle);
      },
    };

    const result = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "test workflow",
      cwd: "/tmp",
      model: "test-model",
      settings,
      process,
      emit: (event) => Effect.sync(() => events.push(event)),
      isCancelled: Effect.succeed(false),
    });
    const markdown = events
      .filter(
        (event): event is Extract<AgentTunnelEvent, { type: "orchestrator.output" }> =>
          event.type === "orchestrator.output",
      )
      .map((event) => event.text)
      .join("");
    return { events, result, markdown };
  });

it.effect("emits well-formed, human-readable workflow markdown", () =>
  Effect.gen(function* () {
    const { events, result, markdown } = yield* runFakeWorkflow();

    NodeAssert.equal(result.status, "completed");
    // Every `</details>` close must be followed by a blank line, otherwise the
    // HTML block swallows the next markdown line and renders raw asterisks.
    NodeAssert.doesNotMatch(markdown, /<\/details>\n(?!\n)/);
    NodeAssert.match(markdown, /## Pipeline: `research`/);
    NodeAssert.match(markdown, /\*\*2 agents\*\* in parallel/);
    NodeAssert.match(markdown, /- 🛠️ \*\*executor · Research A\*\* started in `research`/);
    NodeAssert.match(markdown, /<summary>📋 Output · Research A<\/summary>/);
    NodeAssert.match(
      markdown,
      /✅ \*\*executor · Research A\*\* completed in \d+s · \d+ lines? of output/,
    );
    NodeAssert.match(markdown, /✅ \*\*Pipeline `research` completed\*\* · 2\/2 completed/);
    NodeAssert.match(markdown, /## ✅ Workflow completed · \d+s/);
    NodeAssert.match(markdown, /### Pipelines\n\n\| Pipeline \| Agents \| Status \| Time \|/);
    NodeAssert.match(markdown, /### Agents\n\n\| Agent \| Pipeline \| Status \| Time \| Output \|/);
    NodeAssert.match(markdown, /\| 🛠️ executor · Research B \| `research` \| ✅ completed \|/);
    NodeAssert.match(
      markdown,
      /\| 🔍 reviewer · Review the research \| `review` \| ✅ completed \|/,
    );
    NodeAssert.doesNotMatch(markdown, /### Failures/);

    // Structured activity summaries are plain text: the log rows and Agents
    // panel render them without a markdown pass.
    for (const event of events) {
      if (event.type === "agent.completed") {
        NodeAssert.doesNotMatch(event.summary ?? "", /\*|`/);
      }
      if (event.type === "agent.progress") {
        NodeAssert.doesNotMatch(event.summary, /\*|`/);
      }
    }
    const completedEvents = events.filter(
      (event): event is Extract<AgentTunnelEvent, { type: "agent.completed" }> =>
        event.type === "agent.completed",
    );
    NodeAssert.ok(
      completedEvents.every((event) =>
        /^(Research A|Research B|Review the research) — (completed|failed|stopped) in \d+s/.test(
          event.summary ?? "",
        ),
      ),
    );
  }),
);

it.effect("reports failed agents in the workflow summary", () =>
  Effect.gen(function* () {
    const { result, markdown } = yield* runFakeWorkflow({ failTaskIds: ["task_1"] });

    NodeAssert.equal(result.status, "failed");
    NodeAssert.match(markdown, /❌ \*\*executor · Research A\*\* failed in \d+s/);
    NodeAssert.match(markdown, /❌ \*\*Pipeline `research` failed\*\* · 1\/2 completed · 1 failed/);
    NodeAssert.match(markdown, /### Failures\n\n- ❌ \*\*executor · Research A\*\*/);
    NodeAssert.match(markdown, /\| 🛠️ executor · Research A \| `research` \| ❌ failed \|/);
  }),
);
