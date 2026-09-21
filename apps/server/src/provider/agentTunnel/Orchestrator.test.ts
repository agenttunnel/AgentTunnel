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
  type AgentTunnelProcessSpec,
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

it.effect("hands steering messages to the next planning pass", () =>
  Effect.gen(function* () {
    const events: AgentTunnelEvent[] = [];
    const prompts: string[] = [];
    const settings = decodeSettings({
      orchestratorHarness: "fake-orchestrator",
      executorHarness: "fake-executor",
      reviewerHarness: "fake-reviewer",
      maxOrchestratorPasses: 2,
    });
    const process: AgentTunnelProcessRunner = {
      spawn: (spec) => {
        prompts.push(spec.prompt);
        return Effect.succeed({
          pid: 1,
          lines: Stream.fromIterable(
            spec.prompt.includes("Summary pass")
              ? ['__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"reviewed"}}']
              : spec.id.startsWith("orchestrator")
                ? [
                    '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Work","pipeline":"implementation"}}',
                    '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"dispatched"}}',
                  ]
                : [
                    '__AGENTTUNNEL_TOOL__ {"name":"note_finding","arguments":{"key":"findings","value":"done"}}',
                    '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"complete"}}',
                  ],
          ),
          exitCode: Effect.succeed(0),
          kill: Effect.void,
        } satisfies AgentTunnelProcessHandle);
      },
    };
    let delivered = false;

    const result = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "steer me",
      cwd: "/tmp",
      settings,
      process,
      emit: (event) => Effect.sync(() => events.push(event)),
      isCancelled: Effect.succeed(false),
      takeSteeringMessages: () =>
        Effect.sync(() => {
          if (delivered) return [];
          delivered = true;
          return ["also update the docs"];
        }),
    });

    NodeAssert.equal(result.status, "completed");
    const markdown = events
      .filter(
        (event): event is Extract<AgentTunnelEvent, { type: "orchestrator.output" }> =>
          event.type === "orchestrator.output",
      )
      .map((event) => event.text)
      .join("");
    NodeAssert.match(markdown, /Steering received/);
    NodeAssert.ok(
      prompts.some(
        (prompt) => prompt.includes("Summary pass") && prompt.includes("also update the docs"),
      ),
      "expected steering in the summary pass prompt",
    );
  }),
);

it.effect("takes another pass when steering arrives during the final pass", () =>
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
        return Effect.succeed({
          pid: 1,
          lines: Stream.fromIterable([
            '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"nothing to do"}}',
          ]),
          exitCode: Effect.succeed(0),
          kill: Effect.void,
        } satisfies AgentTunnelProcessHandle);
      },
    };
    let drains = 0;

    const result = yield* runAgentTunnel({
      threadId: "thread-1",
      workflowId: "workflow-1",
      userTask: "steer late",
      cwd: "/tmp",
      settings,
      process,
      emit: (event) => Effect.sync(() => events.push(event)),
      isCancelled: Effect.succeed(false),
      takeSteeringMessages: () =>
        Effect.sync(() => {
          drains += 1;
          return drains === 1 ? ["also update the docs"] : [];
        }),
    });

    NodeAssert.equal(result.status, "completed");
    NodeAssert.ok(
      prompts.some(
        (prompt) => prompt.includes("Summary pass") && prompt.includes("also update the docs"),
      ),
      "expected a follow-up pass carrying the late steering",
    );
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
