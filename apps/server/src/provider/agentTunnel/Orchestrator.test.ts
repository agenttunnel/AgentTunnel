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

it.effect("emits a periodic progress recap while tasks stay running", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const events: AgentTunnelEvent[] = [];
      const settings = decodeSettings({
        orchestratorHarness: "fake-orchestrator",
        executorHarness: "fake-executor",
        reviewerHarness: "fake-reviewer",
        maxOrchestratorPasses: 1,
      });
      const process: AgentTunnelProcessRunner = {
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
      };

      const fiber = yield* runAgentTunnel({
        threadId: "thread-1",
        workflowId: "workflow-1",
        userTask: "slow workflow",
        cwd: "/tmp",
        settings,
        process,
        emit: (event) => Effect.sync(() => events.push(event)),
        isCancelled: Effect.succeed(false),
      }).pipe(Effect.forkChild);

      yield* TestClock.adjust("1 second");
      yield* TestClock.adjust("31 seconds");

      const recaps = events.filter(
        (event): event is Extract<AgentTunnelEvent, { type: "orchestrator.output" }> =>
          event.type === "orchestrator.output" && event.text.includes("\u23f1\ufe0f"),
      );
      NodeAssert.ok(recaps.length >= 1, "expected at least one progress recap");
      NodeAssert.match(recaps[0]!.text, /implementation`?: 0\/1/);
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

const collectMarkdown = (events: ReadonlyArray<AgentTunnelEvent>): string =>
  events
    .filter(
      (event): event is Extract<AgentTunnelEvent, { type: "orchestrator.output" }> =>
        event.type === "orchestrator.output",
    )
    .map((event) => event.text)
    .join("");

/** Fake harness that replays the given orchestrator planning lines for every pass. */
const makeDispatchRunner = (input: {
  readonly dispatchLines: ReadonlyArray<string>;
  readonly specs: Array<AgentTunnelProcessSpec>;
}): AgentTunnelProcessRunner => ({
  spawn: (spec) => {
    input.specs.push(spec);
    const lines = spec.prompt.includes("Summary pass")
      ? ['__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"reviewed"}}']
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
