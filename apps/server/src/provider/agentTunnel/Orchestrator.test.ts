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
