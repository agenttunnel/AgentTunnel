import { AgentTunnelSettings } from "@t3tools/contracts";
import { it } from "@effect/vitest";
import * as Deferred from "effect/Deferred";
import * as Effect from "effect/Effect";
import * as Fiber from "effect/Fiber";
import * as Queue from "effect/Queue";
import * as Schema from "effect/Schema";
import * as Stream from "effect/Stream";
import * as TestClock from "effect/testing/TestClock";
import * as NodeAssert from "node:assert/strict";

import {
  AgentTunnelProcessError,
  type AgentTunnelAgentCommand,
  type AgentTunnelAgentControlChannel,
  type AgentTunnelEvent,
  type AgentTunnelProcessHandle,
  type AgentTunnelProcessRunner,
  type AgentTunnelProcessSpec,
  type AgentTunnelSteeringChannel,
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

const makeSlowWorkflowProcess = (): AgentTunnelProcessRunner => ({
  spawn: (spec) =>
    Effect.succeed(
      spec.id.startsWith("orchestrator")
        ? {
            pid: 1,
            lines: Stream.fromIterable([
              '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Slow work","pipeline":"implementation"}}',
              '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"dispatched"}}',
            ]),
            exitCode: Effect.succeed(0),
            kill: Effect.void,
          }
        : {
            pid: 2,
            lines: Stream.never,
            exitCode: Effect.never,
            kill: Effect.void,
          },
    ),
});

const recapEvents = (events: ReadonlyArray<AgentTunnelEvent>) =>
  events.filter(
    (event): event is Extract<AgentTunnelEvent, { type: "orchestrator.output" }> =>
      event.type === "orchestrator.output" && event.text.includes("\u23f1\ufe0f"),
  );

const runSlowWorkflow = (settings: AgentTunnelSettings) =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const fiber = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "slow workflow",
      cwd: "/tmp",
      settings,
      process: makeSlowWorkflowProcess(),
      emit: (event) => Effect.sync(() => events.push(event)),
      isCancelled: Effect.succeed(false),
    }).pipe(Effect.forkChild);
    return { events, fiber };
  });

it.effect("emits a periodic progress recap while tasks stay running", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const settings = decodeSettings({
        orchestratorHarness: "fake-orchestrator",
        executorHarness: "fake-executor",
        reviewerHarness: "fake-reviewer",
        maxOrchestratorPasses: 1,
      });
      const { events, fiber } = yield* runSlowWorkflow(settings);

      yield* TestClock.adjust("1 second");
      yield* TestClock.adjust("31 seconds");

      const recaps = recapEvents(events);
      NodeAssert.ok(recaps.length >= 1, "expected at least one progress recap");
      NodeAssert.match(recaps[0]!.text, /implementation`?: 0\/1/);
      yield* Fiber.interrupt(fiber);
    }),
  ),
);

it.effect("honors a custom recap interval", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const settings = decodeSettings({
        orchestratorHarness: "fake-orchestrator",
        executorHarness: "fake-executor",
        reviewerHarness: "fake-reviewer",
        maxOrchestratorPasses: 1,
        orchestratorRecapIntervalSeconds: 5,
      });
      const { events, fiber } = yield* runSlowWorkflow(settings);

      yield* TestClock.adjust("1 second");
      NodeAssert.equal(recapEvents(events).length, 0, "no recap before the interval elapses");

      yield* TestClock.adjust("6 seconds");
      NodeAssert.ok(recapEvents(events).length >= 1, "expected a recap after the custom interval");
      yield* Fiber.interrupt(fiber);
    }),
  ),
);

it.effect("turns the recap heartbeat off when the interval is 0", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const settings = decodeSettings({
        orchestratorHarness: "fake-orchestrator",
        executorHarness: "fake-executor",
        reviewerHarness: "fake-reviewer",
        maxOrchestratorPasses: 1,
        orchestratorRecapIntervalSeconds: 0,
      });
      const { events, fiber } = yield* runSlowWorkflow(settings);

      yield* TestClock.adjust("1 second");
      yield* TestClock.adjust("10 minutes");

      NodeAssert.equal(recapEvents(events).length, 0, "expected no recaps when disabled");
      yield* Fiber.interrupt(fiber);
    }),
  ),
);

/**
 * Queue-backed steering source, mirroring the adapter: `pending` never blocks,
 * `takeWhenAvailable` parks until a message lands. `Queue.takeAll` blocks for at
 * least one message, hence the size check in both drains.
 */
const steeringChannel = (queue: Queue.Queue<string>): AgentTunnelSteeringChannel => {
  const drainQueued = () =>
    Queue.size(queue).pipe(
      Effect.flatMap((size) =>
        size === 0
          ? Effect.succeed<ReadonlyArray<string>>([])
          : Queue.takeAll(queue).pipe(Effect.map((entries) => Array.from(entries))),
      ),
    );
  return {
    pending: drainQueued,
    takeWhenAvailable: () =>
      Effect.gen(function* () {
        const first = yield* Queue.take(queue);
        return [first, ...(yield* drainQueued())];
      }),
  };
};

it.effect("runs a steering pass while subagents keep working", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const events: AgentTunnelEvent[] = [];
      const prompts: string[] = [];
      const settings = decodeSettings({
        orchestratorHarness: "fake-orchestrator",
        executorHarness: "fake-executor",
        reviewerHarness: "fake-reviewer",
        maxOrchestratorPasses: 4,
      });
      const steeringQueue = yield* Queue.unbounded<string>();
      const executorStarted = yield* Deferred.make<void>();
      const steeringPassStarted = yield* Deferred.make<void>();
      const followUpCompleted = yield* Deferred.make<void>();
      const process: AgentTunnelProcessRunner = {
        spawn: (spec) =>
          Effect.gen(function* () {
            prompts.push(spec.prompt);
            if (spec.id.startsWith("orchestrator")) {
              if (spec.prompt.includes("## Steering pass")) {
                yield* Deferred.succeed(steeringPassStarted, undefined);
              }
              return {
                pid: 1,
                lines: Stream.fromIterable(
                  spec.prompt.includes("## Current user task")
                    ? [
                        '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Long running work","pipeline":"implementation"}}',
                        '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"dispatched"}}',
                      ]
                    : spec.prompt.includes("## Steering pass")
                      ? [
                          '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Format the fix","pipeline":"follow_up"}}',
                          '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"follow-up dispatched"}}',
                        ]
                      : ['__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"reviewed"}}'],
                ),
                exitCode: Effect.succeed(0),
                kill: Effect.void,
              } satisfies AgentTunnelProcessHandle;
            }
            // The planned work never finishes: the workflow is still running
            // when the steering pass has to react to the message.
            return spec.prompt.includes("Long running work")
              ? {
                  pid: 2,
                  lines: Stream.never,
                  exitCode: Effect.never,
                  kill: Effect.void,
                }
              : {
                  pid: 3,
                  lines: Stream.fromIterable([
                    '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"formatting fixed"}}',
                  ]),
                  exitCode: Effect.succeed(0),
                  kill: Effect.void,
                };
          }),
      };

      const fiber = yield* runAgentTunnel({
        threadId: "thread-1",
        workflowId: "workflow-1",
        userTask: "long task",
        cwd: "/tmp",
        settings,
        process,
        emit: (event) =>
          Effect.sync(() => {
            events.push(event);
          }).pipe(
            Effect.andThen(
              event.type === "agent.started"
                ? Deferred.succeed(executorStarted, undefined)
                : event.type === "agent.completed"
                  ? Deferred.succeed(followUpCompleted, undefined)
                  : Effect.void,
            ),
          ),
        isCancelled: Effect.succeed(false),
        steering: steeringChannel(steeringQueue),
      }).pipe(Effect.forkChild);

      yield* Deferred.await(executorStarted);
      yield* Queue.offer(steeringQueue, "the 712 MR pipeline fails on formatting");
      yield* Deferred.await(steeringPassStarted);

      // The pass saw the running agent, not a settled team.
      const steeringPrompt = prompts.find((prompt) => prompt.includes("## Steering pass"));
      NodeAssert.ok(steeringPrompt, "expected an orchestrator pass carrying the steering");
      NodeAssert.match(steeringPrompt, /the 712 MR pipeline fails on formatting/);
      NodeAssert.match(steeringPrompt, /## Agents working right now/);
      NodeAssert.match(steeringPrompt, /Long running work/);

      const markdown = collectMarkdown(events);
      NodeAssert.match(markdown, /## Steering pass 2/);
      NodeAssert.match(markdown, /while the team keeps working/);
      NodeAssert.match(markdown, /Steering received/);

      // Work dispatched by the steering pass runs without waiting for the
      // long-running agent, which was left alone.
      yield* Deferred.await(followUpCompleted);
      const completed = events.filter(
        (event): event is Extract<AgentTunnelEvent, { type: "agent.completed" }> =>
          event.type === "agent.completed",
      );
      NodeAssert.equal(completed.length, 1);
      NodeAssert.match(completed[0]?.summary ?? "", /Format the fix — completed/);
      NodeAssert.ok(
        events.every((event) => event.type !== "workflow.completed"),
        "workflow must still be running while the planned agent works",
      );

      yield* Fiber.interrupt(fiber);
    }),
  ),
);

it.effect("hands steering that arrives during a pass to the next pass", () =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const prompts: string[] = [];
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      maxOrchestratorPasses: 3,
    });
    const steeringQueue = yield* Queue.unbounded<string>();
    const process: AgentTunnelProcessRunner = {
      spawn: (spec) =>
        Effect.gen(function* () {
          prompts.push(spec.prompt);
          // A harness pass cannot be told mid-run, so the message lands while
          // the planning pass is running and has to be picked up afterwards.
          if (spec.id === "orchestrator_1") {
            yield* Queue.offer(steeringQueue, "also update the docs");
          }
          return {
            pid: 1,
            lines: Stream.fromIterable([
              '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"nothing to do"}}',
            ]),
            exitCode: Effect.succeed(0),
            kill: Effect.void,
          } satisfies AgentTunnelProcessHandle;
        }),
    };

    const result = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "steer me",
      cwd: "/tmp",
      settings,
      process,
      emit: (event) => Effect.sync(() => events.push(event)),
      isCancelled: Effect.succeed(false),
      steering: steeringChannel(steeringQueue),
    });

    NodeAssert.equal(result.status, "completed");
    const steeringPrompts = prompts.filter((prompt) => prompt.includes("## Steering pass"));
    NodeAssert.equal(steeringPrompts.length, 1);
    NodeAssert.match(steeringPrompts[0]!, /also update the docs/);
    // No agents were in flight by then, so the pass is not told about a team.
    NodeAssert.doesNotMatch(steeringPrompts[0]!, /## Agents working right now/);
    NodeAssert.match(collectMarkdown(events), /Steering received/);
  }),
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
              '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"reviewed","summary":"Implemented the requested change; all tests pass.","next_steps":["Review the diff","Run the app to verify"]}}',
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

const collectMarkdown = (events: ReadonlyArray<AgentTunnelEvent>): string =>
  events
    .filter(
      (event): event is Extract<AgentTunnelEvent, { type: "orchestrator.output" }> =>
        event.type === "orchestrator.output",
    )
    .map((event) => event.text)
    .join("");

/**
 * Fake harness for the final-report tests: the caller controls the planning
 * lines (pass 0) and the summary-pass lines, so a test can put a payload on a
 * planning `done`, on the final no-dispatch `done`, or on neither.
 */
const runReportWorkflow = (input: {
  readonly planningLines: ReadonlyArray<string>;
  readonly summaryLines: ReadonlyArray<string>;
}) =>
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
          ? input.summaryLines
          : spec.id.startsWith("orchestrator")
            ? input.planningLines
            : [
                '__AGENTTUNNEL_TOOL__ {"name":"note_finding","arguments":{"key":"findings","value":"work complete"}}',
                '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"complete"}}',
              ];
        return Effect.succeed({
          pid: 1,
          lines: Stream.fromIterable(lines),
          exitCode: Effect.succeed(0),
          kill: Effect.void,
        } satisfies AgentTunnelProcessHandle);
      },
    };

    const result = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "report workflow",
      cwd: "/tmp",
      model: "test-model",
      settings,
      process,
      emit: (event) => Effect.sync(() => events.push(event)),
      isCancelled: Effect.succeed(false),
    });
    const completed = events.find(
      (event): event is Extract<AgentTunnelEvent, { type: "workflow.completed" }> =>
        event.type === "workflow.completed",
    );
    return { events, result, markdown: collectMarkdown(events), completed };
  });

const reportPlanningLines: ReadonlyArray<string> = [
  "**Planning** the work.",
  '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Implement the feature","pipeline":"implementation"}}',
  '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"dispatched"}}',
];

it.effect("renders the orchestrator's final report and condenses it into summaries", () =>
  Effect.gen(function* () {
    const { result, markdown, completed } = yield* runReportWorkflow({
      planningLines: reportPlanningLines,
      summaryLines: [
        "**Implemented** the requested `feature`; the test suite passes.",
        '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"complete","summary":"**Implemented** the requested `feature`; the test suite passes.","next_steps":["Review the diff","Ship the change"]}}',
      ],
    });

    NodeAssert.equal(result.status, "completed");
    // The payload wins and both sections render, ahead of the machinery tables.
    NodeAssert.match(markdown, /### What happened\n\n\*\*Implemented\*\* the requested `feature`/);
    NodeAssert.match(markdown, /### Next steps\n\n- Review the diff\n- Ship the change/);
    NodeAssert.ok(markdown.indexOf("### What happened") < markdown.indexOf("### Pipelines"));

    // Structured summaries carry a plain-text condensation, never markdown.
    const expected = "Implemented the requested feature; the test suite passes.";
    NodeAssert.equal(result.summary, expected);
    NodeAssert.equal(completed?.summary, expected);
    NodeAssert.doesNotMatch(completed?.summary ?? "", /\*|`/);

    // Persisted for follow-up turns, keeping the human-readable markdown.
    NodeAssert.match(
      result.contextDump ?? "",
      /### workflow_result\n\n\*\*Implemented\*\* the requested/,
    );
    NodeAssert.match(
      result.contextDump ?? "",
      /### next_steps\n\n- Review the diff\n- Ship the change/,
    );
  }),
);

it.effect("bounds the plain-text structured summary and strips markdown", () =>
  Effect.gen(function* () {
    const longSummary = `**${"detailed result ".repeat(40)}**`;
    const { result, completed } = yield* runReportWorkflow({
      planningLines: reportPlanningLines,
      summaryLines: [
        `__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"complete","summary":"${longSummary}"}}`,
      ],
    });

    NodeAssert.equal(result.status, "completed");
    NodeAssert.ok(
      result.summary.length <= 300,
      `expected <=300 chars, got ${result.summary.length}`,
    );
    NodeAssert.equal(completed?.summary, result.summary);
    NodeAssert.doesNotMatch(result.summary, /[*`]/);
  }),
);

it.effect("falls back to non-empty report sections when the orchestrator sends no payload", () =>
  Effect.gen(function* () {
    const { result, markdown } = yield* runReportWorkflow({
      planningLines: reportPlanningLines,
      summaryLines: ['__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"reviewed"}}'],
    });

    NodeAssert.equal(result.status, "completed");
    NodeAssert.match(
      markdown,
      /### What happened\n\nAll planned work finished — the orchestrator found no follow-up tasks\./,
    );
    NodeAssert.match(
      markdown,
      /### Next steps\n\n- Review the changes and run the relevant tests to verify the result\./,
    );
    // The report is visible by default: it sits outside the collapsed thinking block.
    NodeAssert.ok(markdown.indexOf("### What happened") > markdown.lastIndexOf("</details>"));
  }),
);

it.effect("derives failure next steps when a task failed", () =>
  Effect.gen(function* () {
    const { result, markdown } = yield* runFakeWorkflow({ failTaskIds: ["task_1"] });

    NodeAssert.equal(result.status, "failed");
    NodeAssert.match(markdown, /### What happened\n\nOne or more pipelines failed\./);
    NodeAssert.match(markdown, /### Next steps\n\n- Fix the 1 failed task/);
  }),
);

it.effect("does not let a planning pass's done payload clobber the final report", () =>
  Effect.gen(function* () {
    const { result, markdown } = yield* runReportWorkflow({
      planningLines: [
        '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Implement the feature","pipeline":"implementation"}}',
        '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"dispatched","summary":"planned outcome must not win","next_steps":["planned step must not win"]}}',
      ],
      summaryLines: [
        '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"complete","summary":"the final outcome wins","next_steps":["the final step wins"]}}',
      ],
    });

    NodeAssert.equal(result.status, "completed");
    NodeAssert.match(markdown, /the final outcome wins/);
    NodeAssert.match(markdown, /the final step wins/);
    NodeAssert.doesNotMatch(markdown, /planned outcome must not win/);
    NodeAssert.doesNotMatch(markdown, /planned step must not win/);
    NodeAssert.equal(result.summary, "the final outcome wins");
  }),
);

/** Fake harness that replays the given orchestrator planning lines for every pass. */
const makeDispatchRunner = (input: {
  readonly dispatchLines: ReadonlyArray<string>;
  readonly specs: Array<AgentTunnelProcessSpec>;
}): AgentTunnelProcessRunner => ({
  spawn: (spec) => {
    input.specs.push(spec);
    const lines = spec.prompt.includes("Summary pass")
      ? [
          '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"reviewed","summary":"Workflow complete.","next_steps":["Verify the changes"]}}',
        ]
      : spec.id.startsWith("orchestrator")
        ? input.dispatchLines
        : [
            '__AGENTTUNNEL_TOOL__ {"name":"note_finding","arguments":{"key":"findings","value":"work complete"}}',
            '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"complete"}}',
          ];
    return Effect.succeed({
      pid: 1,
      lines: Stream.fromIterable(lines),
      exitCode: Effect.succeed(0),
      kill: Effect.void,
    } satisfies AgentTunnelProcessHandle);
  },
});

it.effect("auto mode uses the dispatched model for executor and reviewer tasks", () =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const specs: Array<AgentTunnelProcessSpec> = [];
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      executorModel: "fallback/executor",
      reviewerModel: "fallback/reviewer",
      autoMode: true,
      maxOrchestratorPasses: 3,
    });
    const process = makeDispatchRunner({
      specs,
      dispatchLines: [
        '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Research A","pipeline":"research","model":"auto/executor"}}',
        '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"reviewer","task":"Review the research","pipeline":"review","depends_on":["research"],"model":"auto/reviewer"}}',
        '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"tasks dispatched"}}',
      ],
    });

    const result = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "test auto mode",
      cwd: "/tmp",
      model: "session/model",
      availableModels: ["auto/executor", "auto/reviewer"],
      settings,
      process,
      emit: (event) => Effect.sync(() => events.push(event)),
      isCancelled: Effect.succeed(false),
    });

    NodeAssert.equal(result.status, "completed");
    NodeAssert.equal(
      specs.find((spec) => spec.harness === "fake-executor")?.model,
      "auto/executor",
    );
    NodeAssert.equal(
      specs.find((spec) => spec.harness === "fake-reviewer")?.model,
      "auto/reviewer",
    );
    const started = events.filter(
      (event): event is Extract<AgentTunnelEvent, { type: "agent.started" }> =>
        event.type === "agent.started",
    );
    NodeAssert.equal(started.find((event) => event.role === "executor")?.model, "auto/executor");
    NodeAssert.equal(started.find((event) => event.role === "reviewer")?.model, "auto/reviewer");
    // The panel shows which CLI ran each task, so the started event names it.
    NodeAssert.equal(started.find((event) => event.role === "executor")?.harness, "fake-executor");
    NodeAssert.equal(started.find((event) => event.role === "reviewer")?.harness, "fake-reviewer");
    NodeAssert.equal(
      events.find((event) => event.type === "workflow.started")?.harness,
      "fake-orchestrator",
    );

    const orchestratorPrompts = specs
      .filter((spec) => spec.id.startsWith("orchestrator"))
      .map((spec) => spec.prompt);
    NodeAssert.equal(orchestratorPrompts.length, 2);
    for (const prompt of orchestratorPrompts) {
      NodeAssert.match(prompt, /## Available models\n\nauto\/executor\nauto\/reviewer/);
    }
    const markdown = collectMarkdown(events);
    NodeAssert.match(markdown, /Queued \*\*executor · Research A\*\*[^\n]*auto\/executor/);
    NodeAssert.match(markdown, /Queued \*\*reviewer · Review the research\*\*[^\n]*auto\/reviewer/);
  }),
);

it.effect("auto mode warns on unknown models and falls back to role and session models", () =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const specs: Array<AgentTunnelProcessSpec> = [];
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      executorModel: "fallback/executor",
      autoMode: true,
      maxOrchestratorPasses: 3,
    });
    const process = makeDispatchRunner({
      specs,
      dispatchLines: [
        '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Research A","pipeline":"research","model":"bogus/executor"}}',
        '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"reviewer","task":"Review the research","pipeline":"review","depends_on":["research"],"model":"bogus/reviewer"}}',
        '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"tasks dispatched"}}',
      ],
    });

    const result = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "test invalid auto mode models",
      cwd: "/tmp",
      model: "session/model",
      availableModels: ["known/model"],
      settings,
      process,
      emit: (event) => Effect.sync(() => events.push(event)),
      isCancelled: Effect.succeed(false),
    });

    NodeAssert.equal(result.status, "completed");
    NodeAssert.equal(
      specs.find((spec) => spec.harness === "fake-executor")?.model,
      "fallback/executor",
    );
    NodeAssert.equal(
      specs.find((spec) => spec.harness === "fake-reviewer")?.model,
      "session/model",
    );
    const markdown = collectMarkdown(events);
    NodeAssert.ok(markdown.includes("⚠️ Model `bogus/executor` is not available"));
    NodeAssert.ok(markdown.includes("⚠️ Model `bogus/reviewer` is not available"));
  }),
);

it.effect("auto mode rejects task models when the available list is empty or absent", () =>
  Effect.gen(function* () {
    const cases: ReadonlyArray<{
      readonly label: string;
      readonly availableModels?: ReadonlyArray<string>;
    }> = [{ label: "absent" }, { label: "empty", availableModels: [] }];

    for (const [index, testCase] of cases.entries()) {
      const events: AgentTunnelEvent[] = [];
      const specs: Array<AgentTunnelProcessSpec> = [];
      const settings = decodeSettings({
        orchestratorHarness: "fake-orchestrator",
        executorHarness: "fake-executor",
        reviewerHarness: "fake-reviewer",
        executorModel: "fallback/executor",
        autoMode: true,
        maxOrchestratorPasses: 3,
      });
      const process = makeDispatchRunner({
        specs,
        dispatchLines: [
          '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Research A","pipeline":"research","model":"known/model"}}',
          '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"reviewer","task":"Review the research","pipeline":"review","depends_on":["research"],"model":"known/model"}}',
          '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"tasks dispatched"}}',
        ],
      });

      const result = yield* runAgentTunnel({
        threadId: `thread-${testCase.label}`,
        workflowId: `workflow-${testCase.label}`,
        userTask: "test unavailable task models",
        cwd: "/tmp",
        model: "session/model",
        ...(testCase.availableModels === undefined
          ? {}
          : { availableModels: testCase.availableModels }),
        settings,
        process,
        emit: (event) => Effect.sync(() => events.push(event)),
        isCancelled: Effect.succeed(false),
      });

      NodeAssert.equal(result.status, "completed");
      NodeAssert.equal(
        specs.find((spec) => spec.harness === "fake-executor")?.model,
        "fallback/executor",
      );
      NodeAssert.equal(
        specs.find((spec) => spec.harness === "fake-reviewer")?.model,
        "session/model",
      );
      const markdown = collectMarkdown(events);
      NodeAssert.ok(
        markdown.includes("⚠️ Model `known/model` is not available"),
        `expected a warning with ${testCase.label} available models`,
      );
      const orchestratorPrompts = specs
        .filter((spec) => spec.id.startsWith("orchestrator"))
        .map((spec) => spec.prompt);
      NodeAssert.equal(orchestratorPrompts.length, 2);
      for (const prompt of orchestratorPrompts) {
        NodeAssert.doesNotMatch(prompt, /## Available models/);
      }
    }
  }),
);

it.effect("falls back to the orchestrator model when no task or role model is set", () =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const specs: Array<AgentTunnelProcessSpec> = [];
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      orchestratorModel: "orchestrator/model",
      autoMode: true,
      maxOrchestratorPasses: 3,
    });
    const process = makeDispatchRunner({
      specs,
      dispatchLines: [
        '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Research A","pipeline":"research"}}',
        '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"reviewer","task":"Review the research","pipeline":"review","depends_on":["research"]}}',
        '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"tasks dispatched"}}',
      ],
    });

    const result = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "test orchestrator model fallback",
      cwd: "/tmp",
      model: "session/model",
      availableModels: ["other/model"],
      settings,
      process,
      emit: (event) => Effect.sync(() => events.push(event)),
      isCancelled: Effect.succeed(false),
    });

    NodeAssert.equal(result.status, "completed");
    NodeAssert.equal(
      specs.find((spec) => spec.harness === "fake-executor")?.model,
      "orchestrator/model",
    );
    NodeAssert.equal(
      specs.find((spec) => spec.harness === "fake-reviewer")?.model,
      "orchestrator/model",
    );
    const started = events.filter(
      (event): event is Extract<AgentTunnelEvent, { type: "agent.started" }> =>
        event.type === "agent.started",
    );
    NodeAssert.equal(
      started.find((event) => event.role === "executor")?.model,
      "orchestrator/model",
    );
    NodeAssert.equal(
      started.find((event) => event.role === "reviewer")?.model,
      "orchestrator/model",
    );
    NodeAssert.ok(!specs.some((spec) => spec.model === "session/model"));
  }),
);

it.effect("manual mode ignores a task-level model", () =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const specs: Array<AgentTunnelProcessSpec> = [];
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      executorModel: "fallback/executor",
      maxOrchestratorPasses: 3,
    });
    const process = makeDispatchRunner({
      specs,
      dispatchLines: [
        '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Research A","pipeline":"research","model":"known/model"}}',
        '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"tasks dispatched"}}',
      ],
    });

    const result = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "test manual mode",
      cwd: "/tmp",
      model: "session/model",
      availableModels: ["known/model"],
      settings,
      process,
      emit: (event) => Effect.sync(() => events.push(event)),
      isCancelled: Effect.succeed(false),
    });

    NodeAssert.equal(result.status, "completed");
    NodeAssert.equal(
      specs.find((spec) => spec.harness === "fake-executor")?.model,
      "fallback/executor",
    );
    NodeAssert.ok(!specs.some((spec) => spec.model === "known/model"));
    NodeAssert.ok(!collectMarkdown(events).includes("known/model"));
    for (const spec of specs.filter((entry) => entry.id.startsWith("orchestrator"))) {
      NodeAssert.doesNotMatch(spec.prompt, /## Available models/);
    }
  }),
);

it.effect("injects the inherited context and returns deduplicated learnings", () =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const specs: Array<AgentTunnelProcessSpec> = [];
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      maxOrchestratorPasses: 3,
    });
    const learningLine =
      '__AGENTTUNNEL_TOOL__ {"name":"record_learning","arguments":{"category":"model-fit","entry":"fake-executor + test-model handled Research A well."}}';
    const process: AgentTunnelProcessRunner = {
      spawn: (spec) => {
        specs.push(spec);
        const lines = spec.prompt.includes("Summary pass")
          ? [
              learningLine,
              '__AGENTTUNNEL_TOOL__ {"name":"record_learning","arguments":{"category":"!!!","entry":"DROP"}}',
              learningLine,
              '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"reviewed"}}',
            ]
          : spec.id.startsWith("orchestrator")
            ? [
                '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Research A","pipeline":"research"}}',
                '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"reviewer","task":"Review the research","pipeline":"review","depends_on":["research"]}}',
                '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"tasks dispatched"}}',
              ]
            : ['__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"complete"}}'];
        return Effect.succeed({
          pid: 1,
          lines: Stream.fromIterable(lines),
          exitCode: Effect.succeed(0),
          kill: Effect.void,
        } satisfies AgentTunnelProcessHandle);
      },
    };

    const result = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "test inherited context",
      cwd: "/tmp",
      model: "test-model",
      settings,
      process,
      emit: (event) => Effect.sync(() => events.push(event)),
      isCancelled: Effect.succeed(false),
      inheritedContext: "### rules/model-fit.md\n\n- codex is strong at multi-file refactors.",
    });

    NodeAssert.equal(result.status, "completed");
    const planning = specs.find((spec) => spec.id === "orchestrator_1");
    NodeAssert.match(planning?.prompt ?? "", /## Inherited context/);
    NodeAssert.match(planning?.prompt ?? "", /codex is strong at multi-file refactors\./);
    const summary = specs.find((spec) => spec.prompt.includes("Summary pass"));
    NodeAssert.match(summary?.prompt ?? "", /\| Role \| Harness \| Model \| Status \| Task \|/);
    NodeAssert.match(
      summary?.prompt ?? "",
      /\| executor \| fake-executor \| test-model \| completed \|/,
    );
    NodeAssert.match(summary?.prompt ?? "", /- codex is strong at multi-file refactors\./);

    NodeAssert.deepEqual(result.learnings, [
      { category: "model-fit", entry: "fake-executor + test-model handled Research A well." },
    ]);
    const markdown = collectMarkdown(events);
    NodeAssert.match(markdown, /### Inherited context updates/);
    NodeAssert.match(
      markdown,
      /- `model-fit` · fake-executor \+ test-model handled Research A well\./,
    );
    NodeAssert.ok(!markdown.includes("DROP"));
  }),
);

it.effect("runs planner and tester tasks on their role harnesses and prompts", () =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const specs: Array<AgentTunnelProcessSpec> = [];
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      plannerHarness: "fake-planner",
      testerHarness: "fake-tester",
      maxOrchestratorPasses: 3,
    });
    const process = makeDispatchRunner({
      specs,
      dispatchLines: [
        '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"planner","task":"Plan the migration","pipeline":"plan"}}',
        '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"tester","task":"Verify the migration","pipeline":"verify","depends_on":["plan"]}}',
        '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"archivist","task":"Not a role","pipeline":"bogus"}}',
        '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"tasks dispatched"}}',
      ],
    });

    const result = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "test role wiring",
      cwd: "/tmp",
      model: "test-model",
      settings,
      process,
      emit: (event) => Effect.sync(() => events.push(event)),
      isCancelled: Effect.succeed(false),
    });

    NodeAssert.equal(result.status, "completed");
    const plannerSpec = specs.find((spec) => spec.harness === "fake-planner");
    const testerSpec = specs.find((spec) => spec.harness === "fake-tester");
    NodeAssert.match(plannerSpec?.prompt ?? "", /You are a planner agent in AgentTunnel/);
    NodeAssert.match(testerSpec?.prompt ?? "", /You are a tester agent in AgentTunnel/);
    const started = events.filter(
      (event): event is Extract<AgentTunnelEvent, { type: "agent.started" }> =>
        event.type === "agent.started",
    );
    NodeAssert.deepEqual(
      started.map((event) => event.role),
      ["planner", "tester"],
    );
    NodeAssert.match(collectMarkdown(events), /Invalid dispatch_task arguments/);
  }),
);

/**
 * Queue-backed agent-control source, mirroring the adapter: `pending` never
 * blocks, `takeWhenAvailable` parks until a command lands.
 */
const agentControlChannel = (
  queue: Queue.Queue<AgentTunnelAgentCommand>,
): AgentTunnelAgentControlChannel => {
  const drainQueued = () =>
    Queue.size(queue).pipe(
      Effect.flatMap((size) =>
        size === 0
          ? Effect.succeed<ReadonlyArray<AgentTunnelAgentCommand>>([])
          : Queue.takeAll(queue).pipe(Effect.map((entries) => Array.from(entries))),
      ),
    );
  return {
    pending: drainQueued,
    takeWhenAvailable: () =>
      Effect.gen(function* () {
        const first = yield* Queue.take(queue);
        return [first, ...(yield* drainQueued())];
      }),
  };
};

/** Planning lines for the control tests: one executor on one pipeline. */
const flakyPlanningLines: ReadonlyArray<string> = [
  '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Flaky work","pipeline":"work"}}',
  '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"dispatched"}}',
];

const summaryDoneLine = '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"reviewed"}}';

it.effect("auto-retries a failed agent and completes when the retry succeeds", () =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const attemptsByAgentId = new Map<string, number>();
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      maxOrchestratorPasses: 3,
      maxAgentRetries: 1,
      agentRetryWaitMinutes: 0,
    });
    const process: AgentTunnelProcessRunner = {
      spawn: (spec) => {
        const summary = spec.prompt.includes("Summary pass");
        const orchestrator = spec.id.startsWith("orchestrator");
        const attempt = orchestrator ? 0 : (attemptsByAgentId.get(spec.id) ?? 0) + 1;
        if (!orchestrator) attemptsByAgentId.set(spec.id, attempt);
        return Effect.succeed({
          pid: 1,
          lines: Stream.fromIterable(
            summary
              ? [summaryDoneLine]
              : orchestrator
                ? flakyPlanningLines
                : [`attempt-${attempt}-output`],
          ),
          exitCode: Effect.succeed(!orchestrator && attempt === 1 ? 1 : 0),
          kill: Effect.void,
        } satisfies AgentTunnelProcessHandle);
      },
    };

    const result = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "retry the flaky work",
      cwd: "/tmp",
      settings,
      process,
      emit: (event) => Effect.sync(() => events.push(event)),
      isCancelled: Effect.succeed(false),
    });

    NodeAssert.equal(result.status, "completed");
    NodeAssert.equal(attemptsByAgentId.get("agenttunnel_workflow-1_task_1"), 2);
    const started = events.filter(
      (event): event is Extract<AgentTunnelEvent, { type: "agent.started" }> =>
        event.type === "agent.started",
    );
    // Same agent id on both attempts; the attempt number marks the second run.
    NodeAssert.deepEqual(
      started.map((event) => [event.agentId, event.attempt]),
      [
        ["agenttunnel_workflow-1_task_1", undefined],
        ["agenttunnel_workflow-1_task_1", 2],
      ],
    );
    const markdown = collectMarkdown(events);
    NodeAssert.match(markdown, /↻ \*\*executor · Flaky work\*\* retrying \(attempt 2\)/);
    // One row for the retried agent, carrying the attempt count.
    NodeAssert.equal(markdown.match(/\| 🛠️ executor · Flaky work \|/g)?.length, 1);
    NodeAssert.match(
      markdown,
      /\| 🛠️ executor · Flaky work \| `work` \| ✅ completed \| \d+s \| 1 line · 2 attempts \|/,
    );
    NodeAssert.doesNotMatch(markdown, /### Failures/);
    // The re-run replaces the failed attempt in the notebook: only the newest
    // output survives into the next pass.
    NodeAssert.match(result.contextDump ?? "", /attempt-2-output/);
    NodeAssert.doesNotMatch(result.contextDump ?? "", /attempt-1-output/);
  }),
);

it.effect("re-runs a failed task on a retry command and starts its dependent pipeline", () =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const attemptsByAgentId = new Map<string, number>();
    const controlQueue = yield* Queue.unbounded<AgentTunnelAgentCommand>();
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      maxOrchestratorPasses: 4,
      // Only the manual retry is exercised here.
      maxAgentRetries: 0,
      agentRetryWaitMinutes: 0,
    });
    let retryRequested = false;
    const process: AgentTunnelProcessRunner = {
      spawn: (spec) => {
        const summary = spec.prompt.includes("Summary pass");
        const orchestrator = spec.id.startsWith("orchestrator");
        if (orchestrator) {
          return Effect.succeed({
            pid: 1,
            lines: Stream.fromIterable(
              summary
                ? [summaryDoneLine]
                : [
                    '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Flaky work","pipeline":"work"}}',
                    '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"reviewer","task":"Review the flaky work","pipeline":"review","depends_on":["work"]}}',
                    '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"dispatched"}}',
                  ],
            ),
            exitCode: Effect.succeed(0),
            kill: Effect.void,
          } satisfies AgentTunnelProcessHandle);
        }
        const attempt = (attemptsByAgentId.get(spec.id) ?? 0) + 1;
        attemptsByAgentId.set(spec.id, attempt);
        const executor = spec.id.endsWith("task_1");
        return Effect.succeed({
          pid: 2,
          lines: Stream.fromIterable([executor ? `executor-attempt-${attempt}` : "review output"]),
          exitCode: Effect.succeed(executor && attempt === 1 ? 1 : 0),
          kill: Effect.void,
        } satisfies AgentTunnelProcessHandle);
      },
    };

    const result = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "retry the flaky work",
      cwd: "/tmp",
      settings,
      process,
      emit: (event) =>
        Effect.gen(function* () {
          events.push(event);
          if (retryRequested) return;
          if (event.type !== "agent.completed" || event.status !== "failed") return;
          retryRequested = true;
          yield* Queue.offer(controlQueue, {
            kind: "retry",
            agentId: "agenttunnel_workflow-1_task_1",
          });
        }),
      isCancelled: Effect.succeed(false),
      agentControl: agentControlChannel(controlQueue),
    });

    NodeAssert.equal(result.status, "completed");
    NodeAssert.equal(attemptsByAgentId.get("agenttunnel_workflow-1_task_1"), 2);
    const markdown = collectMarkdown(events);
    NodeAssert.match(
      markdown,
      /↻ \*\*executor · Flaky work\*\* retrying \(attempt 2\) — retried from the Agents panel/,
    );
    // The dependent pipeline was blocked on the failed one and runs after it.
    NodeAssert.match(markdown, /✅ \*\*reviewer · Review the flaky work\*\* completed in/);
    NodeAssert.match(markdown, /## ✅ Workflow completed/);
    NodeAssert.doesNotMatch(markdown, /### Failures/);
    NodeAssert.doesNotMatch(markdown, /Workflow failed/);
  }),
);

it.effect("records an interrupted agent as stopped and does not fail the workflow", () =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const controlQueue = yield* Queue.unbounded<AgentTunnelAgentCommand>();
    const spawned = yield* Deferred.make<void>();
    const agentId = "agenttunnel_workflow-1_task_1";
    let executorHandle: AgentTunnelProcessHandle | undefined;
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      maxOrchestratorPasses: 3,
      maxAgentRetries: 0,
      agentRetryWaitMinutes: 0,
    });
    const process: AgentTunnelProcessRunner = {
      spawn: (spec) =>
        Effect.gen(function* () {
          if (spec.id.startsWith("orchestrator")) {
            return {
              pid: 1,
              lines: Stream.fromIterable(
                spec.prompt.includes("Summary pass") ? [summaryDoneLine] : flakyPlanningLines,
              ),
              exitCode: Effect.succeed(0),
              kill: Effect.void,
            } satisfies AgentTunnelProcessHandle;
          }
          const killed = yield* Deferred.make<void>();
          const handle: AgentTunnelProcessHandle = {
            pid: 2,
            lines: Stream.fromIterable(["working on the slow task"]),
            // A real harness only ends when the adapter signals it.
            exitCode: Deferred.await(killed).pipe(Effect.as(143)),
            kill: Deferred.succeed(killed, undefined).pipe(Effect.asVoid),
          };
          executorHandle = handle;
          yield* Deferred.succeed(spawned, undefined);
          return handle;
        }),
    };

    const fiber = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "interrupt the slow work",
      cwd: "/tmp",
      settings,
      process,
      emit: (event) => Effect.sync(() => events.push(event)),
      isCancelled: Effect.succeed(false),
      agentControl: agentControlChannel(controlQueue),
    }).pipe(Effect.forkChild);

    // The adapter queues the command and then kills that agent's process.
    yield* Deferred.await(spawned);
    yield* Queue.offer(controlQueue, { kind: "interrupt", agentId });
    yield* executorHandle!.kill.pipe(Effect.orDie);

    const result = yield* Fiber.join(fiber);

    NodeAssert.equal(result.status, "stopped");
    const completion = events.find(
      (event): event is Extract<AgentTunnelEvent, { type: "agent.completed" }> =>
        event.type === "agent.completed",
    );
    NodeAssert.equal(completion?.status, "stopped");
    const markdown = collectMarkdown(events);
    NodeAssert.match(markdown, /⏹️ \*\*executor · Flaky work\*\* stopped in \d+s/);
    NodeAssert.doesNotMatch(markdown, /❌ \*\*executor · Flaky work\*\* failed/);
    NodeAssert.match(
      markdown,
      /### Failures\n\n- ⏹️ \*\*executor · Flaky work\*\* — stopped before it finished/,
    );
    NodeAssert.match(markdown, /## ⏹️ Workflow stopped/);
    NodeAssert.doesNotMatch(markdown, /## ❌ Workflow failed/);
  }),
);

it.effect("does not spawn an agent whose interrupt was queued before it started", () =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const controlQueue = yield* Queue.unbounded<AgentTunnelAgentCommand>();
    const agentSpecs: Array<AgentTunnelProcessSpec> = [];
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      maxOrchestratorPasses: 3,
      maxAgentRetries: 0,
      agentRetryWaitMinutes: 0,
    });
    const process: AgentTunnelProcessRunner = {
      spawn: (spec) => {
        agentSpecs.push(spec);
        const orchestrator = spec.id.startsWith("orchestrator");
        return Effect.succeed({
          pid: 1,
          lines: Stream.fromIterable(
            !orchestrator
              ? [`attempt-output`]
              : spec.prompt.includes("Summary pass")
                ? [summaryDoneLine]
                : flakyPlanningLines,
          ),
          exitCode: Effect.succeed(0),
          kill: Effect.void,
        } satisfies AgentTunnelProcessHandle);
      },
    };

    const result = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "interrupt the queued work",
      cwd: "/tmp",
      settings,
      process,
      emit: (event) =>
        Effect.gen(function* () {
          events.push(event);
          // Queued the moment the agent is announced, before its process spawns.
          if (event.type === "agent.started") {
            yield* Queue.offer(controlQueue, {
              kind: "interrupt",
              agentId: "agenttunnel_workflow-1_task_1",
            });
          }
        }),
      isCancelled: Effect.succeed(false),
      agentControl: agentControlChannel(controlQueue),
    });

    NodeAssert.equal(result.status, "stopped");
    NodeAssert.equal(
      agentSpecs.filter((spec) => spec.id.endsWith("task_1")).length,
      0,
      "an interrupted agent must not spawn",
    );
    const markdown = collectMarkdown(events);
    NodeAssert.match(
      markdown,
      /⏹️ \*\*executor · Flaky work\*\* stopped in \d+s · 0 lines of output/,
    );
  }),
);

it.effect("waits for a retry after an unresolved failure and gives up on the deadline", () =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const controlQueue = yield* Queue.unbounded<AgentTunnelAgentCommand>();
    const waiting = yield* Deferred.make<void>();
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      maxOrchestratorPasses: 3,
      maxAgentRetries: 0,
      agentRetryWaitMinutes: 2,
    });
    const process: AgentTunnelProcessRunner = {
      spawn: (spec) => {
        const orchestrator = spec.id.startsWith("orchestrator");
        return Effect.succeed({
          pid: 1,
          lines: Stream.fromIterable(
            !orchestrator
              ? ["the flaky work failed"]
              : spec.prompt.includes("Summary pass")
                ? [summaryDoneLine]
                : flakyPlanningLines,
          ),
          exitCode: Effect.succeed(orchestrator ? 0 : 1),
          kill: Effect.void,
        } satisfies AgentTunnelProcessHandle);
      },
    };

    const fiber = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "let the flaky work fail",
      cwd: "/tmp",
      settings,
      process,
      emit: (event) =>
        Effect.gen(function* () {
          events.push(event);
          if (event.type === "orchestrator.output" && event.text.includes("Waiting for a retry")) {
            yield* Deferred.succeed(waiting, undefined);
          }
        }),
      isCancelled: Effect.succeed(false),
      agentControl: agentControlChannel(controlQueue),
    }).pipe(Effect.forkChild);

    yield* Deferred.await(waiting);
    yield* TestClock.adjust("2 minutes");
    const result = yield* Fiber.join(fiber);

    NodeAssert.equal(result.status, "failed");
    const markdown = collectMarkdown(events);
    NodeAssert.match(
      markdown,
      /> ⏸️ \*\*Waiting for a retry\*\* — retry a failed agent from the Agents panel, or stop the turn\. Giving up in 2 minutes\./,
    );
    NodeAssert.match(markdown, /### Failures\n\n- ❌ \*\*executor · Flaky work\*\*/);
  }),
);

it.effect("resumes the workflow when a retry arrives while it waits for one", () =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const attemptsByAgentId = new Map<string, number>();
    const controlQueue = yield* Queue.unbounded<AgentTunnelAgentCommand>();
    const waiting = yield* Deferred.make<void>();
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      maxOrchestratorPasses: 3,
      maxAgentRetries: 0,
      agentRetryWaitMinutes: 5,
    });
    const process: AgentTunnelProcessRunner = {
      spawn: (spec) => {
        const orchestrator = spec.id.startsWith("orchestrator");
        const attempt = orchestrator ? 0 : (attemptsByAgentId.get(spec.id) ?? 0) + 1;
        if (!orchestrator) attemptsByAgentId.set(spec.id, attempt);
        return Effect.succeed({
          pid: 1,
          lines: Stream.fromIterable(
            !orchestrator
              ? [`attempt-${attempt}-output`]
              : spec.prompt.includes("Summary pass")
                ? [summaryDoneLine]
                : flakyPlanningLines,
          ),
          exitCode: Effect.succeed(orchestrator || attempt > 1 ? 0 : 1),
          kill: Effect.void,
        } satisfies AgentTunnelProcessHandle);
      },
    };

    const fiber = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "retry the failed work while it waits",
      cwd: "/tmp",
      settings,
      process,
      emit: (event) =>
        Effect.gen(function* () {
          events.push(event);
          if (event.type === "orchestrator.output" && event.text.includes("Waiting for a retry")) {
            yield* Deferred.succeed(waiting, undefined);
          }
        }),
      isCancelled: Effect.succeed(false),
      agentControl: agentControlChannel(controlQueue),
    }).pipe(Effect.forkChild);

    yield* Deferred.await(waiting);
    // The wait is still parked: a retry arriving now has to resume the run
    // instead of the deadline ending it.
    yield* Queue.offer(controlQueue, { kind: "retry", agentId: "agenttunnel_workflow-1_task_1" });
    const result = yield* Fiber.join(fiber);

    NodeAssert.equal(result.status, "completed");
    NodeAssert.equal(attemptsByAgentId.get("agenttunnel_workflow-1_task_1"), 2);
    NodeAssert.match(collectMarkdown(events), /## ✅ Workflow completed/);
  }),
);

it.effect("records decisions in the shared notebook and reports them in the transcript", () =>
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
          const lines = spec.id.startsWith("orchestrator")
            ? [
                '__AGENTTUNNEL_TOOL__ {"name":"record_decision","arguments":{"decision":"Keep the thread context in one file per thread","rationale":"survives restarts and stays readable"}}',
                '__AGENTTUNNEL_TOOL__ {"name":"record_decision","arguments":{"decision":"   "}}',
                '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"nothing to dispatch"}}',
              ]
            : [];
          return Effect.succeed({
            pid: 1,
            lines: Stream.fromIterable(lines),
            exitCode: Effect.succeed(0),
            kill: Effect.void,
          } satisfies AgentTunnelProcessHandle);
        },
      };

      const result = yield* runAgentTunnel({
        threadId: "thread-1",
        workflowId: "workflow-1",
        userTask: "record the decisions",
        cwd: "/tmp",
        settings,
        process,
        emit: (event) => Effect.sync(() => events.push(event)),
        isCancelled: Effect.succeed(false),
      });

      const dump = result.contextDump ?? "";
      NodeAssert.match(dump, /### decisions/);
      NodeAssert.match(
        dump,
        /- Keep the thread context in one file per thread — survives restarts and stays readable _\(\d{4}-\d{2}-\d{2}T[\d:.]+Z\)_/,
      );
      // A malformed call is reported, not silently recorded as an empty bullet.
      NodeAssert.doesNotMatch(dump, /- _\(/);
      const markdown = collectMarkdown(events);
      NodeAssert.match(
        markdown,
        /🧭 \*\*Decision recorded\*\* — Keep the thread context in one file/,
      );
      NodeAssert.match(markdown, /Invalid record_decision arguments/);
    }),
  ),
);

it.effect("rehydrates a resumed thread's notebook and appends new decisions after its own", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const events: AgentTunnelEvent[] = [];
      const prompts: string[] = [];
      const settings = decodeSettings({
        orchestratorHarness: "fake-orchestrator",
        executorHarness: "fake-executor",
        reviewerHarness: "fake-reviewer",
        maxOrchestratorPasses: 3,
      });
      const process: AgentTunnelProcessRunner = {
        spawn: (spec) => {
          prompts.push(spec.prompt);
          const lines = spec.id.startsWith("orchestrator")
            ? [
                '__AGENTTUNNEL_TOOL__ {"name":"record_decision","arguments":{"decision":"Recorded on resume"}}',
                '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"nothing to dispatch"}}',
              ]
            : [];
          return Effect.succeed({
            pid: 1,
            lines: Stream.fromIterable(lines),
            exitCode: Effect.succeed(0),
            kill: Effect.void,
          } satisfies AgentTunnelProcessHandle);
        },
      };
      const priorDump = [
        "### decisions",
        "",
        "- Chose the file store _(2026-01-01T00:00:00.000Z)_",
        "",
        "### user_goal",
        "",
        "Keep the thread's context.",
      ].join("\n");

      const result = yield* runAgentTunnel({
        threadId: "thread-1",
        workflowId: "workflow-1",
        userTask: "continue",
        cwd: "/tmp",
        settings,
        process,
        emit: (event) => Effect.sync(() => events.push(event)),
        isCancelled: Effect.succeed(false),
        priorSharedContext: priorDump,
      });

      // The orchestrator reads its own past keys back as notebook sections
      // rather than as one nested dump.
      const planning = prompts.find((prompt) => prompt.includes("## Shared context")) ?? "";
      NodeAssert.match(planning, /### user_goal/);
      NodeAssert.match(planning, /Keep the thread's context\./);
      NodeAssert.match(planning, /Chose the file store/);
      NodeAssert.doesNotMatch(planning, /### prior_thread_context/);

      const dump = result.contextDump ?? "";
      const priorIndex = dump.indexOf("Chose the file store");
      const newIndex = dump.indexOf("Recorded on resume");
      NodeAssert.ok(priorIndex >= 0 && newIndex > priorIndex);
    }),
  ),
);

it.effect("folds each sub-agent's end-of-run context into the thread notebook", () =>
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
        spawn: (spec) =>
          Effect.succeed({
            pid: 1,
            lines: Stream.fromIterable(
              spec.id.startsWith("orchestrator")
                ? [
                    '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Audit the parser","pipeline":"audit"}}',
                    '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Audit the store","pipeline":"audit"}}',
                    '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"dispatched"}}',
                  ]
                : [
                    '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"finished","context":"Touched one file; left the docs update to a follow-up."}}',
                  ],
            ),
            exitCode: Effect.succeed(0),
            kill: Effect.void,
          } satisfies AgentTunnelProcessHandle),
      };

      const result = yield* runAgentTunnel({
        threadId: "thread-1",
        workflowId: "workflow-1",
        userTask: "audit everything",
        cwd: "/tmp",
        settings,
        process,
        emit: (event) => Effect.sync(() => events.push(event)),
        isCancelled: Effect.succeed(false),
      });

      const dump = result.contextDump ?? "";
      NodeAssert.match(dump, /### subagent_context/);
      // One bullet per agent, each labelled with its role and task.
      NodeAssert.equal((dump.match(/\*\*executor · /gu) ?? []).length, 2);
      NodeAssert.match(dump, /Touched one file; left the docs update to a follow-up\./);
      NodeAssert.ok(
        events.some(
          (event) =>
            event.type === "agent.progress" &&
            event.summary === "Context handed back to the orchestrator.",
        ),
      );
    }),
  ),
);

it.effect("warns when a completed sub-agent hands back no context", () =>
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
        spawn: (spec) =>
          Effect.succeed({
            pid: 1,
            lines: Stream.fromIterable(
              spec.id.startsWith("orchestrator")
                ? [
                    '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Quiet work","pipeline":"quiet"}}',
                    '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"dispatched"}}',
                  ]
                : ['__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"finished"}}'],
            ),
            exitCode: Effect.succeed(0),
            kill: Effect.void,
          } satisfies AgentTunnelProcessHandle),
      };

      const result = yield* runAgentTunnel({
        threadId: "thread-1",
        workflowId: "workflow-1",
        userTask: "quiet work",
        cwd: "/tmp",
        settings,
        process,
        emit: (event) => Effect.sync(() => events.push(event)),
        isCancelled: Effect.succeed(false),
      });

      NodeAssert.doesNotMatch(result.contextDump ?? "", /### subagent_context/);
      NodeAssert.match(
        collectMarkdown(events),
        /⚠️ \*\*executor · Quiet work\*\* finished without handing back its context\./,
      );
    }),
  ),
);

it.effect("shows the orchestrator what a running sub-agent reported", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const events: AgentTunnelEvent[] = [];
      const prompts: string[] = [];
      const settings = decodeSettings({
        orchestratorHarness: "fake-orchestrator",
        executorHarness: "fake-executor",
        reviewerHarness: "fake-reviewer",
        maxOrchestratorPasses: 4,
      });
      const steeringQueue = yield* Queue.unbounded<string>();
      const executorStarted = yield* Deferred.make<void>();
      const steeringPassStarted = yield* Deferred.make<void>();
      const process: AgentTunnelProcessRunner = {
        spawn: (spec) =>
          Effect.gen(function* () {
            prompts.push(spec.prompt);
            if (spec.id.startsWith("orchestrator")) {
              if (spec.prompt.includes("## Steering pass")) {
                yield* Deferred.succeed(steeringPassStarted, undefined);
              }
              return {
                pid: 1,
                lines: Stream.fromIterable(
                  spec.prompt.includes("## Current user task")
                    ? [
                        '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Crunch the numbers","pipeline":"numbers"}}',
                        '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"dispatched"}}',
                      ]
                    : [
                        '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"steering weighed"}}',
                      ],
                ),
                exitCode: Effect.succeed(0),
                kill: Effect.void,
              } satisfies AgentTunnelProcessHandle;
            }
            // Reports once, then keeps working: the orchestrator has to see the
            // report while the agent is still mid-run.
            return {
              pid: 2,
              lines: Stream.concat(
                Stream.fromIterable([
                  '__AGENTTUNNEL_TOOL__ {"name":"report_progress","arguments":{"status":"Crunching the numbers, halfway through"}}',
                ]),
                Stream.never,
              ),
              exitCode: Effect.never,
              kill: Effect.void,
            } satisfies AgentTunnelProcessHandle;
          }),
      };

      const fiber = yield* runAgentTunnel({
        threadId: "thread-1",
        workflowId: "workflow-1",
        userTask: "crunch numbers",
        cwd: "/tmp",
        settings,
        process,
        emit: (event) =>
          Effect.sync(() => {
            events.push(event);
          }).pipe(
            Effect.andThen(
              event.type === "agent.started"
                ? Deferred.succeed(executorStarted, undefined)
                : Effect.void,
            ),
          ),
        isCancelled: Effect.succeed(false),
        steering: steeringChannel(steeringQueue),
      }).pipe(Effect.forkChild);

      yield* Deferred.await(executorStarted);
      yield* Queue.offer(steeringQueue, "how is it going?");
      yield* Deferred.await(steeringPassStarted);

      const steeringPrompt = prompts.find((prompt) => prompt.includes("## Steering pass")) ?? "";
      NodeAssert.match(steeringPrompt, /## Agents working right now/);
      NodeAssert.match(steeringPrompt, /\| Latest activity \|/);
      NodeAssert.match(steeringPrompt, /Crunching the numbers, halfway through/);

      yield* Fiber.interrupt(fiber);
    }),
  ),
);
