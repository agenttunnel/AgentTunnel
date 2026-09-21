import type { AgentTunnelSettings } from "@t3tools/contracts";
import * as Cause from "effect/Cause";
import * as Clock from "effect/Clock";
import * as Duration from "effect/Duration";
import * as Effect from "effect/Effect";
import * as Exit from "effect/Exit";
import * as Fiber from "effect/Fiber";
import * as Option from "effect/Option";
import * as Schema from "effect/Schema";
import * as Scope from "effect/Scope";
import * as Stream from "effect/Stream";

import {
  normalizeContextLearning,
  type AgentTunnelContextLearning,
} from "./InheritedContextStore.ts";
import { AgentTunnelSharedContext } from "./SharedContext.ts";
import { parseAgentTunnelToolCall, type AgentTunnelToolCall } from "./protocol.ts";
import {
  EXECUTOR_PROMPT,
  ORCHESTRATOR_PROMPT,
  PLANNER_PROMPT,
  REVIEWER_PROMPT,
  TESTER_PROMPT,
} from "./prompts.ts";

/** Sub-agent kinds an orchestrator can dispatch. */
export type AgentTunnelRole = "planner" | "executor" | "tester" | "reviewer";

const AGENT_TUNNEL_ROLES: ReadonlyArray<AgentTunnelRole> = [
  "planner",
  "executor",
  "tester",
  "reviewer",
];

const isAgentTunnelRole = (value: string): value is AgentTunnelRole =>
  (AGENT_TUNNEL_ROLES as ReadonlyArray<string>).includes(value);

interface AgentTunnelRoleSettings {
  readonly harness: string;
  readonly model: string;
  readonly variant: string;
  readonly prompt: string;
}

/** Every per-role knob the provider settings expose, in one lookup. */
function roleSettings(
  settings: AgentTunnelSettings,
  role: AgentTunnelRole,
): AgentTunnelRoleSettings {
  switch (role) {
    case "planner":
      return {
        harness: settings.plannerHarness,
        model: settings.plannerModel,
        variant: settings.plannerVariant,
        prompt: PLANNER_PROMPT,
      };
    case "tester":
      return {
        harness: settings.testerHarness,
        model: settings.testerModel,
        variant: settings.testerVariant,
        prompt: TESTER_PROMPT,
      };
    case "reviewer":
      return {
        harness: settings.reviewerHarness,
        model: settings.reviewerModel,
        variant: settings.reviewerVariant,
        prompt: REVIEWER_PROMPT,
      };
    case "executor":
      return {
        harness: settings.executorHarness,
        model: settings.executorModel,
        variant: settings.executorVariant,
        prompt: EXECUTOR_PROMPT,
      };
  }
}

export interface AgentTunnelProcessSpec {
  readonly id: string;
  readonly threadId: string;
  readonly harness: string;
  readonly model?: string;
  readonly variant?: string;
  readonly cwd: string;
  readonly prompt: string;
  readonly dangerouslySkipPermissions: boolean;
}

export class AgentTunnelProcessError extends Schema.TaggedErrorClass<AgentTunnelProcessError>()(
  "AgentTunnelProcessError",
  {
    detail: Schema.String,
    cause: Schema.optional(Schema.Defect()),
  },
) {}

export interface AgentTunnelProcessHandle {
  readonly pid: number;
  readonly lines: Stream.Stream<string, AgentTunnelProcessError>;
  readonly exitCode: Effect.Effect<number, AgentTunnelProcessError>;
  readonly kill: Effect.Effect<void, AgentTunnelProcessError>;
}

export interface AgentTunnelProcessRunner {
  readonly spawn: (
    spec: AgentTunnelProcessSpec,
  ) => Effect.Effect<AgentTunnelProcessHandle, AgentTunnelProcessError, Scope.Scope>;
}

export interface AgentTunnelWorkflowPhase {
  readonly index: number;
  readonly title: string;
  readonly mode: "parallel" | "sequential";
  readonly dependsOn: ReadonlyArray<string>;
  readonly status: "pending" | "running" | "completed" | "failed";
}

export type AgentTunnelAgentStatus = "completed" | "failed" | "stopped";

export type AgentTunnelEvent =
  | {
      readonly type: "workflow.started";
      readonly workflowId: string;
      readonly title: string;
      /** Harness CLI running the orchestrator passes (settings.orchestratorHarness). */
      readonly harness: string;
      readonly model?: string;
    }
  | {
      readonly type: "workflow.progress";
      readonly workflowId: string;
      readonly phases: ReadonlyArray<AgentTunnelWorkflowPhase>;
      readonly summary?: string;
    }
  | {
      readonly type: "workflow.completed";
      readonly workflowId: string;
      readonly status: "completed" | "failed" | "stopped";
      readonly summary: string;
    }
  | {
      readonly type: "orchestrator.output";
      readonly text: string;
    }
  | {
      readonly type: "agent.started";
      readonly workflowId: string;
      readonly agentId: string;
      readonly role: AgentTunnelRole;
      readonly task: string;
      readonly pipelineId: string;
      readonly agentIndex: number;
      readonly phaseIndex: number;
      /** Harness CLI that will run this agent (role's configured harness). */
      readonly harness: string;
      readonly model?: string;
      /** Run number for this agent id; omitted means the first attempt. */
      readonly attempt?: number;
    }
  | {
      readonly type: "agent.progress";
      readonly workflowId: string;
      readonly agentId: string;
      readonly summary: string;
    }
  | {
      readonly type: "agent.completed";
      readonly workflowId: string;
      readonly agentId: string;
      readonly role: AgentTunnelRole;
      readonly status: AgentTunnelAgentStatus;
      readonly summary?: string;
    };

export interface AgentTunnelRunResult {
  readonly status: "completed" | "failed" | "stopped";
  readonly summary: string;
  readonly contextDump?: string;
  /** Durable cross-project lessons the orchestrator recorded for the inherited context. */
  readonly learnings?: ReadonlyArray<AgentTunnelContextLearning>;
}

export interface AgentTunnelRunOptions {
  readonly threadId: string;
  readonly workflowId: string;
  readonly userTask: string;
  readonly cwd: string;
  readonly model?: string;
  readonly availableModels?: ReadonlyArray<string>;
  readonly settings: AgentTunnelSettings;
  readonly process: AgentTunnelProcessRunner;
  readonly emit: (event: AgentTunnelEvent) => Effect.Effect<void>;
  readonly isCancelled: Effect.Effect<boolean>;
  readonly orchestratorPassTimeout?: Duration.Input;
  readonly threadHistory?: ReadonlyArray<{ readonly task: string; readonly summary?: string }>;
  readonly priorSharedContext?: string;
  /** Rendered global context inherited from earlier orchestrators. */
  readonly inheritedContext?: string;
  /**
   * Steering the user sent while this workflow runs. The orchestrator takes a
   * pass as soon as a message is queued and no pass is in flight, instead of
   * waiting for the running pipelines to finish.
   */
  readonly steering?: AgentTunnelSteeringChannel;
  /**
   * Per-agent control the user sent while this workflow runs: interrupting one
   * agent's process, or re-running a failed task in place. Absent when no
   * control surface is wired up, in which case nothing can be interrupted or
   * retried mid-run.
   */
  readonly agentControl?: AgentTunnelAgentControlChannel;
}

/**
 * Steering queue handed to the orchestrator. `pending` reports what is queued
 * without blocking; `takeWhenAvailable` waits for the next message and drains
 * the rest with it, so a burst of messages costs one pass.
 */
export interface AgentTunnelSteeringChannel {
  readonly pending: () => Effect.Effect<ReadonlyArray<string>>;
  readonly takeWhenAvailable: () => Effect.Effect<ReadonlyArray<string>>;
}

/** One control command aimed at a single sub-agent. */
export type AgentTunnelAgentCommand =
  | { readonly kind: "interrupt"; readonly agentId: string }
  | { readonly kind: "retry"; readonly agentId: string };

/**
 * Per-agent control queue. `pending` reports what is queued without blocking;
 * `takeWhenAvailable` waits for the next command and drains the rest with it.
 *
 * An interrupt is recorded as soon as the loop drains it: the adapter already
 * killed that agent's process, and a killed agent is stopped, not failed. A
 * retry is held until the round settles, so it can never re-run a task whose
 * process is still in flight.
 */
export interface AgentTunnelAgentControlChannel {
  readonly pending: () => Effect.Effect<ReadonlyArray<AgentTunnelAgentCommand>>;
  readonly takeWhenAvailable: () => Effect.Effect<ReadonlyArray<AgentTunnelAgentCommand>>;
}

/** A sub-agent run in flight, as a steering pass needs to see it. */
interface RunningTask {
  readonly role: AgentTunnelRole;
  readonly label: string;
  readonly pipelineId: string;
}

/**
 * What woke the loop while it held the turn open for a retry: the deadline, a
 * retry command that was applied, or steering the user sent instead.
 */
type RetryWaitOutcome =
  | { readonly kind: "timeout" }
  | { readonly kind: "retry" }
  | { readonly kind: "steering"; readonly messages: ReadonlyArray<string> };

interface DispatchedTask {
  readonly id: string;
  readonly role: AgentTunnelRole;
  readonly task: string;
  readonly context: ReadonlyArray<string>;
  readonly pipelineId: string;
  readonly agentIndex: number;
  readonly model?: string;
}

interface PipelineState {
  readonly id: string;
  readonly mode: "parallel" | "sequential";
  readonly dependsOn: ReadonlyArray<string>;
  status: "pending" | "running" | "completed" | "failed";
  durationMs?: number;
}

interface ProcessResult {
  readonly code: number | null;
  readonly error?: string;
}

interface TaskRecord {
  readonly id: string;
  readonly role: AgentTunnelRole;
  readonly label: string;
  readonly pipelineId: string;
  readonly agentIndex: number;
  readonly status: AgentTunnelAgentStatus;
  readonly durationMs: number;
  readonly outputLines: number;
  readonly harness: string;
  readonly model?: string;
  readonly error?: string;
  /** Attempt number when this agent ran more than once; omitted means one run. */
  readonly attempt?: number;
}

/**
 * The orchestrator's closing report: what the workflow actually produced and
 * what the user should do next, captured from the `done` payload of a pass that
 * dispatched no new pipelines.
 */
interface WorkflowFinalReport {
  readonly summary: string;
  readonly nextSteps: ReadonlyArray<string>;
}

interface ResolvedFinalReport {
  readonly resultText: string;
  readonly nextSteps: ReadonlyArray<string>;
  readonly plainSummary: string;
}

const FINAL_SUMMARY_MAX_CHARS = 300;

const MAX_AGENT_OUTPUT_LINES = 200;
const MAX_PROGRESS_CHARS = 500;
const TASK_LABEL_MAX_CHARS = 72;

const roleIcon = (role: AgentTunnelRole): string =>
  role === "planner" ? "🗺️" : role === "tester" ? "🧪" : role === "executor" ? "🛠️" : "🔍";

const statusIcon = (status: AgentTunnelAgentStatus): string =>
  status === "completed" ? "✅" : status === "failed" ? "❌" : "⏹️";

const statusLabel = (status: AgentTunnelAgentStatus): string =>
  status === "completed" ? "completed" : status === "failed" ? "failed" : "stopped";

function formatDuration(durationMs: number): string {
  const seconds = Math.max(0, Math.round(durationMs / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`;
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, "0")}m`;
}

function humanTaskLabel(task: string, max = TASK_LABEL_MAX_CHARS): string {
  const firstLine = task
    .split("\n")
    .map((line) => line.trim())
    .find((line) => line.length > 0);
  const plain = (firstLine ?? "task")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/^[ \t]*[>#]+[ \t]*/, "")
    .replace(/[*_`|]/g, "")
    .replace(/\s+/g, " ")
    .trim();
  if (plain.length === 0) return "task";
  if (plain.length <= max) return plain;
  const cut = plain.slice(0, max - 1);
  const lastSpace = cut.lastIndexOf(" ");
  const body = lastSpace >= max * 0.6 ? cut.slice(0, lastSpace) : cut;
  return `${body.trimEnd()}…`;
}

function plainTextPreview(value: string, max = MAX_PROGRESS_CHARS): string {
  const plain = value
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/^[ \t]*[>#]+[ \t]*/gm, "")
    .replace(/[*_`|]/g, "")
    .replace(/\s+/g, " ")
    .trim();
  const candidate = plain.length > 0 ? plain : value.trim();
  return candidate.length <= max ? candidate : `${candidate.slice(0, max - 1)}...`;
}

const fallbackNextSteps = (
  status: AgentTunnelRunResult["status"],
  taskRecords: ReadonlyArray<TaskRecord>,
): ReadonlyArray<string> => {
  const failures = taskRecords.filter((record) => record.status === "failed");
  if (failures.length > 0) {
    const noun = failures.length === 1 ? "task" : "tasks";
    return [
      `Fix the ${failures.length} failed ${noun} above, re-run the affected pipeline, then verify the result.`,
    ];
  }
  if (status === "stopped") {
    return ["Review what completed, then re-run the workflow to finish the remaining work."];
  }
  return ["Review the changes and run the relevant tests to verify the result."];
};

/**
 * Resolve the closing report: the orchestrator's payload when present, guarded
 * fallbacks otherwise. `plainSummary` is the markdown-free condensation that
 * travels in structured events and the run result.
 */
function resolveFinalReportContent(input: {
  readonly status: AgentTunnelRunResult["status"];
  readonly fallbackSummary: string;
  readonly report: WorkflowFinalReport | null;
  readonly taskRecords: ReadonlyArray<TaskRecord>;
}): ResolvedFinalReport {
  const reportSummary = input.report?.summary.trim() ?? "";
  const resultText = reportSummary.length > 0 ? reportSummary : input.fallbackSummary;
  const nextSteps =
    input.report && input.report.nextSteps.length > 0
      ? input.report.nextSteps
      : fallbackNextSteps(input.status, input.taskRecords);
  const condensed = plainTextPreview(resultText, FINAL_SUMMARY_MAX_CHARS);
  // plainTextPreview appends an ellipsis past the limit; trim so the structured
  // summary never exceeds the documented bound.
  const bounded =
    condensed.length > FINAL_SUMMARY_MAX_CHARS
      ? `${condensed.slice(0, FINAL_SUMMARY_MAX_CHARS - 3)}...`
      : condensed;
  return {
    resultText,
    nextSteps,
    plainSummary: bounded.length > 0 ? bounded : input.fallbackSummary,
  };
}

const trace = (message: string, fields: Record<string, string | number | boolean>) =>
  Effect.log(message, fields).pipe(Effect.annotateLogs({ scope: "agenttunnel.orchestrator" }));

/** Execute AgentTunnel's orchestration loop without depending on a UI store. */
export const runAgentTunnel = Effect.fn("runAgentTunnel")(function* (
  options: AgentTunnelRunOptions,
) {
  const context = new AgentTunnelSharedContext();
  // Rehydrate prior thread context so follow-up turns (e.g. "do the previous task")
  // can resolve. The adapter persists the last workflow's shared context dump;
  // we prime the notebook with it as a synthetic entry.
  if (options.priorSharedContext && options.priorSharedContext.trim().length > 0) {
    try {
      context.set("prior_thread_context", options.priorSharedContext.trim());
    } catch {}
  }
  const pipelines = new Map<string, PipelineState>();
  const pipelineOrder: string[] = [];
  const tasks: DispatchedTask[] = [];
  const taskRecords: TaskRecord[] = [];
  const recordedLearnings: AgentTunnelContextLearning[] = [];
  const recordedLearningKeys = new Set<string>();
  const workflowStartedAt = yield* Clock.currentTimeMillis;
  let nextTaskIndex = 0;
  let orchestratorPasses = 0;
  let orchestratorThinkingOpen = false;
  // `passDoneReport` is the payload from the current pass; `finalReport` only
  // holds a payload from a pass that dispatched no new pipelines.
  let passDoneReport: WorkflowFinalReport | null = null;
  let finalReport: WorkflowFinalReport | null = null;
  let openOutputFences = 0;
  let startedTaskCount = 0;
  let recapScope: Scope.Closeable | null = null;
  // Pipelines run as forked fibers so a steering pass can start while sub-agents
  // keep working; the scheduler only ever blocks on "settled or steered".
  const runningPipelines = new Map<string, Fiber.Fiber<boolean, never>>();
  const startedPipelineIds = new Set<string>();
  // Sub-agent runs still in flight, keyed by agent id. A steering pass reads
  // this roster before dispatching, since a running agent cannot be re-told.
  const activeTasks = new Map<string, RunningTask>();
  // Agents the user interrupted. A killed process must read as stopped, not
  // failed, even though the thread-level cancel flag stays false for a
  // per-agent interrupt; a task whose agent is already here must not spawn.
  const stoppedAgentIds = new Set<string>();
  // Retry commands, held until the round settles: only the settle handler can
  // re-run a task and then recompute the pipeline that task belongs to.
  const retryRequests = new Set<string>();
  // Attempt number per agent id, so every run of an agent shares one id, one
  // record row and one log file.
  const attempts = new Map<string, number>();

  const emit = options.emit;
  const isCancelled = options.isCancelled;
  const orchestratorPassTimeout =
    options.orchestratorPassTimeout ??
    Duration.minutes(options.settings.orchestratorPassTimeoutMinutes);

  yield* trace("workflow started", {
    workflowId: options.workflowId,
    threadId: options.threadId,
    model: options.model ?? "",
    userTask: options.userTask.slice(0, 120),
  });

  const phaseSnapshot = (): ReadonlyArray<AgentTunnelWorkflowPhase> =>
    pipelineOrder.map((pipelineId, index) => {
      const pipeline = pipelines.get(pipelineId);
      return {
        index,
        title: pipelineId,
        mode: pipeline?.mode ?? "parallel",
        dependsOn: pipeline?.dependsOn ?? [],
        status:
          pipeline?.status === "running"
            ? "running"
            : pipeline?.status === "completed"
              ? "completed"
              : pipeline?.status === "failed"
                ? "failed"
                : "pending",
      };
    });

  const emitProgress = (summary?: string) =>
    emit({
      type: "workflow.progress",
      workflowId: options.workflowId,
      phases: phaseSnapshot(),
      ...(summary ? { summary } : {}),
    });

  const finish = (
    status: AgentTunnelRunResult["status"],
    summary: string,
    report: WorkflowFinalReport | null = null,
  ) =>
    Effect.gen(function* () {
      if (recapScope !== null) {
        yield* Scope.close(recapScope, Exit.void).pipe(Effect.ignore);
        recapScope = null;
      }
      const resolved = resolveFinalReportContent({
        status,
        fallbackSummary: summary,
        report,
        taskRecords,
      });
      // Persist the closing report so the next turn can resolve "continue" and
      // "do the previous task" without relying on the collapsed prose.
      setContext(context, "workflow_result", resolved.resultText);
      setContext(context, "next_steps", resolved.nextSteps.map((step) => `- ${step}`).join("\n"));
      yield* trace("workflow finished", {
        workflowId: options.workflowId,
        status,
        summary: resolved.plainSummary.slice(0, 160),
      });
      const finishedAt = yield* Clock.currentTimeMillis;
      yield* emit({
        type: "orchestrator.output",
        text: formatWorkflowFinishMarkdown({
          status,
          summary,
          resultText: resolved.resultText,
          nextSteps: resolved.nextSteps,
          pipelineOrder,
          pipelines,
          taskRecords,
          learnings: recordedLearnings,
          durationMs: finishedAt - workflowStartedAt,
        }),
      });
      yield* emit({
        type: "workflow.completed",
        workflowId: options.workflowId,
        status,
        summary: resolved.plainSummary,
      });
      return {
        status,
        summary: resolved.plainSummary,
        contextDump: context.renderForPrompt(),
        ...(recordedLearnings.length > 0 ? { learnings: [...recordedLearnings] } : {}),
      } satisfies AgentTunnelRunResult;
    });

  const processOutput = Effect.fn("runAgentTunnel.processOutput")(function* (
    handle: AgentTunnelProcessHandle,
    onLine: (line: string) => Effect.Effect<void>,
  ) {
    let lineCount = 0;
    const linesExit = yield* Effect.exit(
      handle.lines.pipe(
        Stream.tap(() => Effect.sync(() => (lineCount += 1))),
        Stream.runForEach(onLine),
      ),
    );
    const code = yield* handle.exitCode.pipe(Effect.orElseSucceed(() => 1));
    yield* trace("process stream ended", {
      pid: handle.pid,
      exitCode: code,
      lines: lineCount,
    });
    if (linesExit._tag === "Failure") {
      return {
        code,
        error: Cause.pretty(linesExit.cause),
      };
    }
    return { code };
  });

  const runProcess = Effect.fn("runAgentTunnel.runProcess")(function* (
    spec: AgentTunnelProcessSpec,
    onLine: (line: string) => Effect.Effect<void>,
    timeout?: Duration.Input,
  ) {
    yield* trace("spawning harness process", {
      id: spec.id,
      harness: spec.harness,
      ...(spec.model ? { model: spec.model } : {}),
      cwd: spec.cwd,
      ...(timeout ? { timeoutMs: Duration.toMillis(timeout) } : {}),
      promptChars: spec.prompt.length,
      skipPermissions: spec.dangerouslySkipPermissions,
    });
    const inner = Effect.scoped(
      options.process.spawn(spec).pipe(Effect.flatMap((handle) => processOutput(handle, onLine))),
    );
    const wrapped = timeout
      ? inner.pipe(Effect.timeoutOption(timeout))
      : inner.pipe(Effect.map(Option.some));
    const result = yield* Effect.exit(wrapped);
    if (result._tag === "Failure") {
      yield* trace("harness process failed", { id: spec.id, error: Cause.pretty(result.cause) });
      return {
        code: null,
        error: Cause.pretty(result.cause),
      };
    }
    const value = result.value;
    if (Option.isNone(value)) {
      yield* trace("harness process timed out", {
        id: spec.id,
        ...(timeout ? { timeoutMs: Duration.toMillis(timeout) } : {}),
      });
      return {
        code: null,
        error:
          "orchestrator pass timed out — raise the pass timeout in AgentTunnel settings if the planner needs longer",
      };
    }
    yield* trace("harness process finished", {
      id: spec.id,
      code: value.value.code,
      ...(value.value.error ? { error: value.value.error } : {}),
    });
    return value.value;
  });

  const handleOrchestratorToolCall = Effect.fn("runAgentTunnel.handleOrchestratorToolCall")(
    function* (call: AgentTunnelToolCall) {
      if (call.name === "update_context") {
        const key = stringValue(call.arguments.key);
        const value = stringValue(call.arguments.value);
        const error = setContext(context, key, value);
        if (!error) {
          const preview = plainTextPreview(value, 200);
          yield* emit({
            type: "orchestrator.output",
            text: `\n📝 **Context updated:** \`${key}\`${preview.length > 0 ? ` — ${preview}` : ""}\n`,
          });
        } else {
          yield* emit({
            type: "orchestrator.output",
            text: `\n> ⚠️ **Context update failed:** ${error}\n`,
          });
        }
        return;
      }

      if (call.name === "dispatch_task") {
        const role = stringValue(call.arguments.role);
        const task = stringValue(call.arguments.task).trim();
        if (!isAgentTunnelRole(role) || task.length === 0) {
          yield* emit({
            type: "orchestrator.output",
            text: `> ⚠️ Invalid dispatch_task arguments.\n`,
          });
          return;
        }

        const pipelineId =
          stringValue(call.arguments.pipeline).trim() || `pipeline_${nextTaskIndex + 1}`;
        const mode = call.arguments.mode === "sequential" ? "sequential" : "parallel";
        const dependsOn = stringArray(call.arguments.depends_on);
        const contextKeys = stringArray(call.arguments.context);
        const requestedModel = stringValue(call.arguments.model).trim();
        let taskModel: string | undefined;
        if (options.settings.autoMode && requestedModel.length > 0) {
          const availableModels = options.availableModels ?? [];
          if (availableModels.includes(requestedModel)) {
            taskModel = requestedModel;
          } else {
            yield* emit({
              type: "orchestrator.output",
              text: `\n> ⚠️ Model \`${requestedModel}\` is not available for the ${role} task — using the configured model instead.\n`,
            });
          }
        }

        const existing = pipelines.get(pipelineId);
        if (!existing) {
          pipelines.set(pipelineId, {
            id: pipelineId,
            mode,
            dependsOn,
            status: "pending",
          });
          pipelineOrder.push(pipelineId);
        }

        const agentIndex = nextTaskIndex;
        nextTaskIndex += 1;
        tasks.push({
          id: `task_${agentIndex + 1}`,
          role,
          task,
          context: contextKeys,
          pipelineId,
          agentIndex,
          ...(taskModel ? { model: taskModel } : {}),
        });
        const label = humanTaskLabel(task);
        yield* trace("task dispatched", {
          workflowId: options.workflowId,
          role,
          pipeline: pipelineId,
          mode,
          dependsOn: dependsOn.join(","),
          ...(taskModel ? { model: taskModel } : {}),
          task: task.slice(0, 160),
        });
        yield* emitProgress(`Queued ${role} "${label}" in pipeline "${pipelineId}" (${mode})`);
        const dependencyText =
          dependsOn.length > 0 ? ` · after ${dependsOn.map((id) => `\`${id}\``).join(", ")}` : "";
        const contextText =
          contextKeys.length > 0
            ? ` · uses ${contextKeys.map((key) => `\`${key}\``).join(", ")}`
            : "";
        const modelText = taskModel ? ` · ${taskModel}` : "";
        yield* emit({
          type: "orchestrator.output",
          text: `\n- ${roleIcon(role)} Queued **${role} · ${label}** in \`${pipelineId}\` · ${mode}${modelText}${dependencyText}${contextText}\n`,
        });
        return;
      }

      if (call.name === "record_learning") {
        const learning = normalizeContextLearning(
          stringValue(call.arguments.category),
          stringValue(call.arguments.entry),
        );
        if (!learning) {
          yield* emit({
            type: "orchestrator.output",
            text: `> ⚠️ Invalid record_learning arguments — category must be lowercase kebab-case and entry must be a non-empty string.\n`,
          });
          return;
        }
        const key = `${learning.category}\u0000${learning.entry}`;
        if (recordedLearningKeys.has(key)) return;
        recordedLearningKeys.add(key);
        recordedLearnings.push(learning);
        yield* trace("learning recorded", {
          workflowId: options.workflowId,
          category: learning.category,
          chars: learning.entry.length,
        });
        yield* emit({
          type: "orchestrator.output",
          text: `\n🧠 **Learning recorded** \`${learning.category}\` — ${plainTextPreview(learning.entry, 200)}\n`,
        });
        return;
      }

      if (call.name === "done") {
        const reportSummary = stringValue(call.arguments.summary).trim();
        const reportNextSteps = nextStepsArray(call.arguments.next_steps);
        passDoneReport =
          reportSummary.length > 0 || reportNextSteps.length > 0
            ? { summary: reportSummary, nextSteps: reportNextSteps }
            : null;
        yield* trace("orchestrator finished pass", {
          workflowId: options.workflowId,
          reason: stringValue(call.arguments.reason).slice(0, 120),
          hasReport: passDoneReport !== null,
        });
      } else {
        yield* emit({
          type: "orchestrator.output",
          text: `> ⚠️ Unknown orchestrator action: \`${call.name}\`\n`,
        });
      }
    },
  );

  const closeOrchestratorThinking = (): Effect.Effect<void> =>
    orchestratorThinkingOpen
      ? emit({ type: "orchestrator.output", text: `\n</details>\n\n` }).pipe(
          Effect.andThen(
            Effect.sync(() => {
              orchestratorThinkingOpen = false;
            }),
          ),
        )
      : Effect.void;

  const handleOrchestratorLine = (line: string) => {
    const clean = stripAnsi(line).trim();
    if (clean.length === 0) return Effect.void;
    const call = parseAgentTunnelToolCall(clean);
    if (call) {
      return closeOrchestratorThinking().pipe(
        Effect.andThen(
          trace("orchestrator tool call", {
            name: call.name,
            preview: clean.slice(0, 120),
          }),
        ),
        Effect.andThen(handleOrchestratorToolCall(call)),
      );
    }
    if (!orchestratorThinkingOpen) {
      orchestratorThinkingOpen = true;
      return emit({
        type: "orchestrator.output",
        text: `\n<details>\n<summary>🧠 Orchestrator thinking</summary>\n\n${clean}\n`,
      });
    }
    return emit({ type: "orchestrator.output", text: `${clean}\n` });
  };

  const historyBlock = (): string => {
    const history = options.threadHistory ?? [];
    if (history.length === 0) return "";
    const lines = history
      .slice(-5)
      .map(
        (entry, idx) =>
          `${idx + 1}. **User:** ${entry.task}${entry.summary ? ` → *${entry.summary}*` : ""}`,
      )
      .join("\n");
    return `\n\n## Conversation history (prior turns in this thread)\n${lines}\n\nIf the current task references "previous task", "that", or "continue", resolve it from this history.`;
  };

  const availableModelsBlock = (): string => {
    const availableModels = options.availableModels ?? [];
    if (!options.settings.autoMode || availableModels.length === 0) return "";
    return `\n\n## Available models\n\n${availableModels.join("\n")}`;
  };

  const inheritedContextBlock = (): string => {
    const inherited = options.inheritedContext?.trim() ?? "";
    if (inherited.length === 0) return "";
    return `\n\n## Inherited context\n\nDurable cross-project rules recorded by earlier orchestrators. Treat them as binding guidance unless the current task overrides them.\n\n${inherited}`;
  };

  const dispatchOutcomesBlock = (): string => {
    if (taskRecords.length === 0) return "";
    const orchestratorModel = options.settings.orchestratorModel || options.model;
    const rows = taskRecords.map(
      (record) =>
        `| ${record.role} | ${record.harness} | ${record.model ?? "default"} | ${record.status} | ${record.label} |`,
    );
    return `\n\n## Dispatch outcomes this workflow\n\nUse these actual harness/model/status results when recording model-fit and model-quality learnings before you call done.\n\nOrchestrator: \`${options.settings.orchestratorHarness}\`${orchestratorModel ? ` · \`${orchestratorModel}\`` : ""}\n\n| Role | Harness | Model | Status | Task |\n| --- | --- | --- | --- | --- |\n${rows.join("\n")}`;
  };

  const steeringBlock = (steering: ReadonlyArray<string>): string =>
    steering.length === 0
      ? ""
      : `\n\n## Steering from the user (sent while work was running)\n\n${steering.map((message) => `- ${message}`).join("\n")}\n\nWeigh this before deciding what to do next.`;

  const pipelineSummaryBlock = (): string =>
    pipelineOrder
      .map((id) => {
        const pipeline = pipelines.get(id);
        return `- ${id}: ${pipeline?.status ?? "pending"}`;
      })
      .join("\n") || "(none)";

  /**
   * In-flight sub-agent runs. Harness runs are one-shot CLI processes with no
   * input channel, so steering that touches their work has to be routed as
   * follow-up dispatch rather than handed to the running process.
   */
  const runningAgentsBlock = (): string => {
    if (activeTasks.size === 0) return "";
    const rows = [...activeTasks.values()].map(
      (entry) => `| ${entry.role} | \`${entry.pipelineId}\` | ${entry.label} |`,
    );
    return `\n\n## Agents working right now\n\nThese sub-agents are mid-run and did not see the steering below. If it changes what one of them is doing, dispatch a follow-up task that supersedes their work — name their pipeline in depends_on when the change must land after they finish — instead of assuming they will adjust.\n\n| Role | Pipeline | Task |\n| --- | --- | --- |\n${rows.join("\n")}`;
  };

  const steeringPrompt = (steering: ReadonlyArray<string>, running: boolean): string =>
    `${ORCHESTRATOR_PROMPT}${historyBlock()}${availableModelsBlock()}${steeringBlock(steering)}${inheritedContextBlock()}${dispatchOutcomesBlock()}${runningAgentsBlock()}\n\n## Steering pass\n\nThe user steered while the team ${running ? "is still working" : "was working"}. Decide what changes now${running ? ", leave the in-flight work alone, and dispatch whatever the steering needs" : ""}, then call done.\n\n### Pipelines\n${pipelineSummaryBlock()}\n\n### Shared context\n${context.renderForPrompt()}`;

  const summaryPrompt = (): string =>
    `${ORCHESTRATOR_PROMPT}${historyBlock()}${availableModelsBlock()}${inheritedContextBlock()}${dispatchOutcomesBlock()}\n\n## Summary pass\n\nThe team has completed the following work. Dispatch follow-up tasks when needed, or call done.\n\n### Pipelines\n${pipelineSummaryBlock()}\n\n### Shared context\n${context.renderForPrompt()}`;

  const runOrchestratorPass = Effect.fn("runAgentTunnel.runOrchestratorPass")(function* (input: {
    readonly pass: number;
    readonly steering: ReadonlyArray<string>;
  }) {
    const running = activeTasks.size > 0;
    if (input.pass > 0) {
      yield* emit({
        type: "orchestrator.output",
        text:
          input.steering.length > 0
            ? `\n---\n\n## Steering pass ${input.pass + 1}\n\nThe orchestrator is weighing your steering${running ? " while the team keeps working" : ""}.\n`
            : `\n---\n\n## Summary pass ${input.pass + 1}\n\nThe orchestrator is checking the completed work for follow-up tasks.\n`,
      });
    }
    if (input.steering.length > 0) {
      yield* emit({
        type: "orchestrator.output",
        text: `\n> \ud83d\udce5 **Steering received** \u2014 the orchestrator is weighing it now.\n\n`,
      });
    }
    const result = yield* runProcess(
      {
        id: `orchestrator_${input.pass + 1}`,
        threadId: options.threadId,
        harness: options.settings.orchestratorHarness,
        ...(options.settings.orchestratorModel || options.model
          ? { model: options.settings.orchestratorModel || options.model }
          : {}),
        ...(options.settings.orchestratorVariant
          ? { variant: options.settings.orchestratorVariant }
          : {}),
        cwd: options.cwd,
        prompt:
          input.pass === 0
            ? `${ORCHESTRATOR_PROMPT}${historyBlock()}${availableModelsBlock()}${inheritedContextBlock()}\n\n## Shared context\n${context.renderForPrompt()}\n\n## Current user task\n${options.userTask}`
            : input.steering.length > 0
              ? steeringPrompt(input.steering, running)
              : summaryPrompt(),
        dangerouslySkipPermissions: options.settings.dangerouslySkipPermissions,
      },
      handleOrchestratorLine,
      orchestratorPassTimeout,
    );
    yield* closeOrchestratorThinking();
    return result;
  });

  const handleSubAgentToolCall = Effect.fn("runAgentTunnel.handleSubAgentToolCall")(function* (
    call: AgentTunnelToolCall,
    agentId: string,
  ) {
    if (call.name === "note_finding") {
      const key = stringValue(call.arguments.key);
      const error = appendContext(context, key, stringValue(call.arguments.value), agentId);
      if (!error) {
        yield* emit({
          type: "agent.progress",
          workflowId: options.workflowId,
          agentId,
          summary: `Finding recorded: ${key}`,
        });
      } else {
        yield* emit({
          type: "agent.progress",
          workflowId: options.workflowId,
          agentId,
          summary: `Finding failed: ${error}`,
        });
      }
      return;
    }

    if (call.name === "approve" || call.name === "request_changes") {
      const reason =
        call.name === "approve"
          ? "Reviewer approved the work."
          : `Reviewer requested changes: ${stringValue(call.arguments.reason)}`;
      context.append("review_findings", reason, agentId);
      yield* emit({
        type: "agent.progress",
        workflowId: options.workflowId,
        agentId,
        summary: reason,
      });
    }
  });

  const agentIdOf = (task: DispatchedTask): string =>
    `agenttunnel_${options.workflowId}_${task.id}`;

  /**
   * Applies whatever the user queued for individual agents. Interrupts are
   * recorded at once — the adapter already killed that agent's process, and a
   * killed agent is stopped, not failed, even though the thread-level cancel
   * flag stays false. Retries are only collected here; re-running a task and
   * recomputing its pipeline is the settle handler's job.
   */
  const drainAgentCommands = Effect.fn("runAgentTunnel.drainAgentCommands")(function* () {
    const channel = options.agentControl;
    if (!channel) return;
    for (const command of yield* channel.pending()) {
      if (command.kind === "retry") {
        retryRequests.add(command.agentId);
        continue;
      }
      if (stoppedAgentIds.has(command.agentId)) continue;
      stoppedAgentIds.add(command.agentId);
      yield* trace("agent interrupt received", {
        workflowId: options.workflowId,
        agentId: command.agentId,
      });
      const interrupted = tasks.find((task) => agentIdOf(task) === command.agentId);
      const interruptedLabel = interrupted ? humanTaskLabel(interrupted.task) : command.agentId;
      yield* emit({
        type: "orchestrator.output",
        text: `\n> ⏹️ **Interrupt requested** — ${interruptedLabel} is being stopped.\n\n`,
      });
    }
  });

  /** One record per task id: a retried task replaces its earlier attempt. */
  const recordTask = (record: TaskRecord) => {
    const index = taskRecords.findIndex((entry) => entry.id === record.id);
    if (index >= 0) {
      taskRecords[index] = record;
      return;
    }
    taskRecords.push(record);
  };

  /**
   * Recomputes a pipeline from its records. A retry that succeeded turns the
   * pipeline back to completed, which is what unblocks its dependents.
   */
  const recomputePipelineStatus = (pipelineId: string): void => {
    const pipeline = pipelines.get(pipelineId);
    if (!pipeline) return;
    pipeline.status = tasks
      .filter((task) => task.pipelineId === pipelineId)
      .every((task) => taskRecords.find((record) => record.id === task.id)?.status === "completed")
      ? "completed"
      : "failed";
  };

  /**
   * Records how one attempt ended: the closing narration, this agent's task
   * record (replaced, never stacked) and the shared-context block it wrote,
   * which the next orchestrator pass reads. Both carry only the newest attempt.
   */
  const recordTaskOutcome = Effect.fn("runAgentTunnel.recordTaskOutcome")(function* (input: {
    readonly task: DispatchedTask;
    readonly agentId: string;
    readonly label: string;
    readonly status: AgentTunnelAgentStatus;
    readonly attempt: number;
    readonly durationMs: number;
    readonly outputLines: number;
    readonly harness: string;
    readonly model?: string;
    readonly exitCode?: number;
    readonly errorExcerpt?: string;
    readonly resultText: string;
  }) {
    const { task, agentId, label, status, attempt } = input;
    const duration = formatDuration(input.durationMs);
    context.replaceAuthor(`result_${safeKey(task.pipelineId)}`, input.resultText, agentId);
    const outcomeDetail = input.errorExcerpt
      ? ` — ${input.errorExcerpt}`
      : ` · ${input.outputLines} line${input.outputLines === 1 ? "" : "s"} of output`;
    const attemptDetail = attempt > 1 ? ` · attempt ${attempt}` : "";
    yield* emit({
      type: "orchestrator.output",
      text: `${statusIcon(status)} **${task.role} · ${label}** ${statusLabel(status)} in ${duration}${outcomeDetail}${attemptDetail}\n\n`,
    });
    recordTask({
      id: task.id,
      role: task.role,
      label,
      pipelineId: task.pipelineId,
      agentIndex: task.agentIndex,
      status,
      durationMs: input.durationMs,
      outputLines: input.outputLines,
      harness: input.harness,
      ...(input.model ? { model: input.model } : {}),
      ...(input.errorExcerpt ? { error: input.errorExcerpt } : {}),
      ...(attempt > 1 ? { attempt } : {}),
    });
    yield* emit({
      type: "agent.completed",
      workflowId: options.workflowId,
      agentId,
      role: task.role,
      status,
      summary: `${label} — ${statusLabel(status)} in ${duration}${outcomeDetail}`,
    });
    yield* trace("agent completed", {
      workflowId: options.workflowId,
      agentId,
      role: task.role,
      status,
      pipeline: task.pipelineId,
      attempt,
      ...(input.exitCode === undefined ? {} : { exitCode: input.exitCode }),
    });
  });

  /**
   * Runs one task, re-running it in place while it ends failed and the
   * auto-retry budget allows. Every attempt reuses the agent id, so the client
   * keeps one agent row and one log file. A stopped task is never auto-retried:
   * the user interrupted it on purpose.
   */
  const runTaskWithRetries = Effect.fn("runAgentTunnel.runTaskWithRetries")(function* (
    task: DispatchedTask,
    agentId: string,
    label: string,
  ) {
    const maxRetries = Math.max(0, options.settings.maxAgentRetries ?? 0);
    let attempt = attempts.get(agentId) ?? 1;
    attempts.set(agentId, attempt);
    let status = yield* runTaskBody(task, agentId, label, attempt);
    while (status === "failed" && attempt <= maxRetries) {
      attempt += 1;
      attempts.set(agentId, attempt);
      yield* trace("agent auto-retrying", {
        workflowId: options.workflowId,
        agentId,
        role: task.role,
        pipeline: task.pipelineId,
        attempt,
        maxRetries,
      });
      yield* emit({
        type: "orchestrator.output",
        text: `\n↻ **${task.role} · ${label}** retrying (attempt ${attempt})\n\n`,
      });
      status = yield* runTaskBody(task, agentId, label, attempt);
    }
    return status;
  });

  const runTask = Effect.fn("runAgentTunnel.runTask")(function* (task: DispatchedTask) {
    const agentId = agentIdOf(task);
    const label = humanTaskLabel(task.task);
    // Registered before the process spawns: a steering pass that lands while
    // this agent runs must see it in the roster and route around it.
    activeTasks.set(agentId, { role: task.role, label, pipelineId: task.pipelineId });
    return yield* runTaskWithRetries(task, agentId, label).pipe(
      Effect.ensuring(Effect.sync(() => activeTasks.delete(agentId))),
    );
  });

  /**
   * Re-runs a task the user asked for, ignoring the auto-retry budget, and
   * recomputes its pipeline from the new record so a repaired pipeline can
   * unblock its dependents.
   */
  const retryTask = Effect.fn("runAgentTunnel.retryTask")(function* (task: DispatchedTask) {
    const agentId = agentIdOf(task);
    const label = humanTaskLabel(task.task);
    const attempt = (attempts.get(agentId) ?? 1) + 1;
    attempts.set(agentId, attempt);
    // The user asked for this run explicitly, so the interrupt that stopped the
    // previous attempt does not apply to it.
    stoppedAgentIds.delete(agentId);
    yield* trace("agent retried on request", {
      workflowId: options.workflowId,
      agentId,
      role: task.role,
      pipeline: task.pipelineId,
      attempt,
    });
    yield* emit({
      type: "orchestrator.output",
      text: `\n↻ **${task.role} · ${label}** retrying (attempt ${attempt}) — retried from the Agents panel\n\n`,
    });
    activeTasks.set(agentId, { role: task.role, label, pipelineId: task.pipelineId });
    const status = yield* runTaskWithRetries(task, agentId, label).pipe(
      Effect.ensuring(Effect.sync(() => activeTasks.delete(agentId))),
    );
    recomputePipelineStatus(task.pipelineId);
    return status;
  });

  /**
   * Applies queued retry commands, one task at a time. A retry aimed at an
   * agent that is still running stays queued until the round settles; one aimed
   * at an agent that already completed, or at an id this workflow never ran, is
   * dropped.
   */
  const applyPendingRetries = Effect.fn("runAgentTunnel.applyPendingRetries")(function* () {
    yield* drainAgentCommands();
    if (retryRequests.size === 0) return 0;
    let applied = 0;
    for (const agentId of [...retryRequests]) {
      if (yield* isCancelled) break;
      if (activeTasks.has(agentId)) continue;
      const task = tasks.find((entry) => agentIdOf(entry) === agentId);
      if (!task) {
        retryRequests.delete(agentId);
        continue;
      }
      if (taskRecords.find((record) => record.id === task.id)?.status === "completed") {
        retryRequests.delete(agentId);
        continue;
      }
      retryRequests.delete(agentId);
      yield* retryTask(task);
      applied += 1;
    }
    return applied;
  });

  const runTaskBody = Effect.fn("runAgentTunnel.runTaskBody")(function* (
    task: DispatchedTask,
    agentId: string,
    label: string,
    attempt: number,
  ) {
    startedTaskCount += 1;
    const pipelineIndex = Math.max(0, pipelineOrder.indexOf(task.pipelineId));
    const roleConfig = roleSettings(options.settings, task.role);
    const model =
      task.model ||
      roleConfig.model ||
      options.settings.orchestratorModel ||
      options.model ||
      undefined;
    const harness = roleConfig.harness;
    const variant = roleConfig.variant;
    const output: string[] = [];

    yield* emit({
      type: "agent.started",
      workflowId: options.workflowId,
      agentId,
      role: task.role,
      task: task.task,
      pipelineId: task.pipelineId,
      agentIndex: task.agentIndex,
      phaseIndex: pipelineIndex,
      harness,
      ...(model ? { model } : {}),
      ...(attempt > 1 ? { attempt } : {}),
    });
    const startedAt = yield* Clock.currentTimeMillis;
    // Use a bullet + quoted task line so the previous blockquote is closed
    // and this agent's header starts its own Markdown block. Without the
    // leading blank line and bullet, parallel starts were smashed into one
    // grey blockquote (screenshot: 4 headers inline inside one quote).
    const taskQuote = task.task
      .split("\n")
      .map((line) => `> ${line}`)
      .join("\n");
    yield* emit({
      type: "orchestrator.output",
      text: `\n- ${roleIcon(task.role)} **${task.role} · ${label}** started in \`${task.pipelineId}\`${model ? ` · ${model}` : ""}${attempt > 1 ? ` · attempt ${attempt}` : ""}\n\n${taskQuote}\n\n`,
    });
    yield* emit({
      type: "agent.progress",
      workflowId: options.workflowId,
      agentId,
      summary: `${label} — ${task.role} started in pipeline "${task.pipelineId}"`,
    });
    yield* trace("agent started", {
      workflowId: options.workflowId,
      agentId,
      role: task.role,
      pipeline: task.pipelineId,
      harness,
      model: model ?? "",
    });

    const contextBlock =
      task.context.length > 0
        ? `\n\n## Shared context\n${context.renderForPrompt(task.context)}`
        : "";
    const systemPrompt = roleConfig.prompt;
    const prompt = `${systemPrompt}\n\n## Task\n${task.task}${contextBlock}\n\nCall done when finished.`;
    // An interrupt that arrived before this attempt spawned must leave no
    // process behind: report the same stopped outcome a killed process gives,
    // with no output of its own.
    yield* drainAgentCommands();
    if (stoppedAgentIds.has(agentId)) {
      yield* recordTaskOutcome({
        task,
        agentId,
        label,
        status: "stopped",
        attempt,
        durationMs: (yield* Clock.currentTimeMillis) - startedAt,
        outputLines: 0,
        harness,
        ...(model ? { model } : {}),
        resultText: `## ${task.role} ${agentId} (stopped)\n\nTask: ${task.task}\n\nStopped by the user before it ran.`,
      });
      return "stopped" satisfies AgentTunnelAgentStatus;
    }
    let progressLineCount = 0;
    let firstOutputLine = true;
    const result = yield* runProcess(
      {
        id: agentId,
        threadId: options.threadId,
        harness,
        ...(model ? { model } : {}),
        ...(variant ? { variant } : {}),
        cwd: options.cwd,
        prompt,
        dangerouslySkipPermissions: options.settings.dangerouslySkipPermissions,
      },
      (line) => {
        const clean = stripAnsi(line).trim();
        if (clean.length === 0) return Effect.void;
        const call = parseAgentTunnelToolCall(clean);
        if (call) return handleSubAgentToolCall(call, agentId);

        output.push(clean);
        if (output.length > MAX_AGENT_OUTPUT_LINES) output.shift();
        progressLineCount += 1;
        const prefix = firstOutputLine
          ? `\n<details>\n<summary>📋 Output · ${label}</summary>\n\n\`\`\`\n`
          : "";
        if (firstOutputLine) {
          openOutputFences += 1;
        }
        firstOutputLine = false;
        return emit({ type: "orchestrator.output", text: `${prefix}${clean}\n` }).pipe(
          Effect.andThen(
            emit({
              type: "agent.progress",
              workflowId: options.workflowId,
              agentId,
              summary: plainTextPreview(clean),
            }),
          ),
        );
      },
    );

    if (!firstOutputLine) {
      openOutputFences = Math.max(0, openOutputFences - 1);
      yield* emit({ type: "orchestrator.output", text: "```\n\n</details>\n\n" });
    }

    // The adapter kills the process on an interrupt, so drain the command
    // before reading the outcome: a killed agent is stopped, not failed.
    yield* drainAgentCommands();
    const cancelled = yield* isCancelled;
    const status: AgentTunnelAgentStatus =
      cancelled || stoppedAgentIds.has(agentId)
        ? "stopped"
        : result.code === 0
          ? "completed"
          : "failed";
    const durationMs = (yield* Clock.currentTimeMillis) - startedAt;
    const errorExcerpt = result.error ? plainTextPreview(result.error, 160) : undefined;
    yield* recordTaskOutcome({
      task,
      agentId,
      label,
      status,
      attempt,
      durationMs,
      outputLines: progressLineCount,
      harness,
      ...(model ? { model } : {}),
      ...(result.code === null ? {} : { exitCode: result.code }),
      ...(errorExcerpt ? { errorExcerpt } : {}),
      resultText: `## ${task.role} ${agentId} (${status})\n\nTask: ${task.task}\n\n${output.join("\n")}`,
    });
    return status;
  });

  const runPipeline = Effect.fn("runAgentTunnel.runPipeline")(function* (pipeline: PipelineState) {
    pipeline.status = "running";
    yield* trace("pipeline started", {
      workflowId: options.workflowId,
      pipeline: pipeline.id,
      mode: pipeline.mode,
      dependsOn: pipeline.dependsOn.join(","),
    });
    const pipelineTasks = tasks.filter((task) => task.pipelineId === pipeline.id);
    const modeLabel = pipeline.mode === "parallel" ? "in parallel" : "sequentially";
    const taskCount = pipelineTasks.length;
    const pipelineStartedAt = yield* Clock.currentTimeMillis;
    const dependencyText =
      pipeline.dependsOn.length > 0
        ? ` · after ${pipeline.dependsOn.map((id) => `\`${id}\``).join(", ")}`
        : "";
    yield* emit({
      type: "orchestrator.output",
      text: `\n---\n\n## Pipeline: \`${pipeline.id}\`\n\n**${taskCount} ${taskCount === 1 ? "agent" : "agents"}** ${modeLabel}${dependencyText}\n\n`,
    });
    yield* emitProgress(
      `Pipeline "${pipeline.id}" started — ${taskCount} ${taskCount === 1 ? "agent" : "agents"} running ${modeLabel}`,
    );
    const results: ReadonlyArray<AgentTunnelAgentStatus> =
      pipeline.mode === "parallel"
        ? yield* Effect.forEach(pipelineTasks, runTask, { concurrency: "unbounded" })
        : yield* runSequential(pipelineTasks);
    const ok = results.every((status) => status === "completed");
    pipeline.status = ok ? "completed" : "failed";
    pipeline.durationMs = (yield* Clock.currentTimeMillis) - pipelineStartedAt;
    yield* trace("pipeline finished", {
      workflowId: options.workflowId,
      pipeline: pipeline.id,
      status: pipeline.status,
    });
    const completedCount = results.filter((s) => s === "completed").length;
    const failedCount = results.filter((s) => s === "failed").length;
    const stoppedCount = results.filter((s) => s === "stopped").length;
    const statusParts = [
      `${completedCount}/${results.length} completed`,
      failedCount > 0 ? `${failedCount} failed` : null,
      stoppedCount > 0 ? `${stoppedCount} stopped` : null,
    ].filter((value): value is string => value !== null);
    const breakdown = statusParts.join(" · ");
    // A pipeline whose only losses were user interrupts has not failed: label
    // it stopped so the summary matches the agent rows inside it.
    const pipelineLabel = ok ? "completed" : failedCount > 0 ? "failed" : "stopped";
    const pipelineIcon = ok ? "✅" : failedCount > 0 ? "❌" : "⏹️";
    yield* emit({
      type: "orchestrator.output",
      text: `\n${pipelineIcon} **Pipeline \`${pipeline.id}\` ${pipelineLabel}** · ${breakdown} · ${formatDuration(pipeline.durationMs)}\n\n`,
    });
    yield* emitProgress(`Pipeline "${pipeline.id}" ${pipelineLabel} · ${breakdown}`);
    return ok;
  });

  const runSequential = Effect.fn("runAgentTunnel.runSequential")(function* (
    pipelineTasks: ReadonlyArray<DispatchedTask>,
  ) {
    const results: AgentTunnelAgentStatus[] = [];
    for (const task of pipelineTasks) {
      if (yield* isCancelled) break;
      results.push(yield* runTask(task));
    }
    return results;
  });

  /**
   * Starts every pipeline whose dependencies are already satisfied. Pipelines
   * are forked, not awaited, so the orchestrator can take a steering pass while
   * they run; the scheduler waits for them through `waitForPipelinesToSettle`.
   */
  const startReadyPipelines = Effect.fn("runAgentTunnel.startReadyPipelines")(function* () {
    let started = 0;
    for (;;) {
      const startable = pipelineOrder
        .map((id) => pipelines.get(id))
        .filter(
          (pipeline): pipeline is PipelineState =>
            pipeline !== undefined &&
            !startedPipelineIds.has(pipeline.id) &&
            pipeline.dependsOn.every(
              (dependency) => pipelines.get(dependency)?.status === "completed",
            ),
        );
      if (startable.length === 0) break;
      started += startable.length;
      for (const pipeline of startable) {
        startedPipelineIds.add(pipeline.id);
        // `startImmediately: false` keeps the fork and the map insert atomic: a
        // pipeline that finished first would otherwise delete itself before it
        // was ever tracked, leaving a settled fiber in the running set.
        const fiber = yield* runPipeline(pipeline).pipe(
          Effect.ensuring(Effect.sync(() => runningPipelines.delete(pipeline.id))),
          Effect.forkChild({ startImmediately: false }),
        );
        runningPipelines.set(pipeline.id, fiber);
      }
    }

    if (runningPipelines.size > 0 || pipelineOrder.every((id) => startedPipelineIds.has(id))) {
      return { started, deadlocked: false };
    }
    // Nothing is running and nothing can start: what is left depends on work
    // that never ran or never succeeded.
    for (const id of pipelineOrder) {
      if (startedPipelineIds.has(id)) continue;
      const pipeline = pipelines.get(id);
      if (pipeline) pipeline.status = "failed";
    }
    yield* emitProgress("Pipeline dependency deadlock");
    yield* emit({
      type: "orchestrator.output",
      text: `\n> ⚠️ **Pipeline dependency deadlock** — no pipelines can start because their dependencies never completed.\n`,
    });
    return { started, deadlocked: true };
  });

  const waitForPipelinesToSettle = Effect.fn("runAgentTunnel.waitForPipelinesToSettle")(
    function* () {
      // Cancelling a `Fiber.await` never touches the awaited fiber, so racing
      // this against steering leaves the running agents working.
      yield* Effect.forEach([...runningPipelines.values()], (fiber) => Fiber.await(fiber), {
        concurrency: "unbounded",
        discard: true,
      });
    },
  );

  yield* emit({
    type: "workflow.started",
    workflowId: options.workflowId,
    title: options.userTask,
    harness: options.settings.orchestratorHarness,
    ...(options.model ? { model: options.model } : {}),
  });
  yield* emit({
    type: "orchestrator.output",
    text: `## AgentTunnel workflow\n\n**Task:** ${options.userTask}\n\nThe orchestrator is analyzing the task and will dispatch subagents shortly.\n`,
  });

  const emitRecap = Effect.fn("runAgentTunnel.emitRecap")(function* () {
    const elapsed = formatDuration((yield* Clock.currentTimeMillis) - workflowStartedAt);
    const parts = pipelineOrder.map((id) => {
      const total = tasks.filter((task) => task.pipelineId === id).length;
      const done = taskRecords.filter((record) => record.pipelineId === id).length;
      const pipeline = pipelines.get(id);
      const marker =
        pipeline?.status === "completed" ? "✓" : pipeline?.status === "running" ? "▶" : "…";
      return `${marker} \`${id}\`: ${done}/${total}`;
    });
    const running = Math.max(0, startedTaskCount - taskRecords.length);
    const state =
      parts.length > 0
        ? parts.join(" · ")
        : tasks.length > 0
          ? "starting pipelines"
          : "orchestrator planning";
    const recap = `Progress · ${elapsed}${running > 0 ? ` · ${running} running` : ""} · ${state}`;
    yield* emitProgress(recap);
    // An agent output fence is open: the recap would land inside a code block,
    // so only the activity summary above is updated until the fence closes.
    if (openOutputFences > 0) {
      return;
    }
    yield* emit({ type: "orchestrator.output", text: `\n> ⏱️ **${recap}**\n\n` });
  });

  const scope = yield* Scope.make();
  recapScope = scope;
  // The periodic recap is user-configurable; 0 turns the heartbeat off.
  const recapIntervalSeconds = options.settings.orchestratorRecapIntervalSeconds;
  if (recapIntervalSeconds > 0) {
    yield* Effect.forever(
      Effect.sleep(Duration.seconds(recapIntervalSeconds)).pipe(Effect.andThen(emitRecap())),
    ).pipe(Effect.forkIn(scope));
  }

  /**
   * Ends a workflow that still has unresolved work. A round whose only losses
   * were agents the user interrupted reports "stopped": nothing failed.
   */
  const finishOutstanding = Effect.fn("runAgentTunnel.finishOutstanding")(function* (
    failureSummary: string,
  ) {
    const wasStopped =
      (yield* isCancelled) ||
      (taskRecords.length > 0 && taskRecords.every((record) => record.status !== "failed"));
    return yield* finish(
      wasStopped ? "stopped" : "failed",
      wasStopped ? "The workflow was stopped before all planned work finished." : failureSummary,
    );
  });

  /**
   * Last chance before unresolved work ends the turn: hold the loop open for a
   * retry from the Agents panel, or for steering, whichever lands first. A
   * retry is applied here; steering is handed back so the caller can run a pass
   * with it instead of giving up.
   */
  const awaitRetryOrSteering = Effect.fn("runAgentTunnel.awaitRetryOrSteering")(
    function* (): Effect.fn.Return<RetryWaitOutcome> {
      const channel = options.agentControl;
      const waitMinutes = Math.max(0, options.settings.agentRetryWaitMinutes ?? 0);
      // Without a control surface there is nothing to wait for.
      if (!channel || waitMinutes === 0) return { kind: "timeout" };
      yield* emit({
        type: "orchestrator.output",
        text: `\n> ⏸️ **Waiting for a retry** — retry a failed agent from the Agents panel, or stop the turn. Giving up in ${waitMinutes} minute${waitMinutes === 1 ? "" : "s"}.\n\n`,
      });
      yield* emitRecap();
      const outcome = yield* Effect.raceAll([
        channel
          .takeWhenAvailable()
          .pipe(Effect.map((commands) => ({ kind: "command" as const, commands }))),
        // Steering still wakes the loop: the user may prefer to steer around the
        // failure rather than retry it.
        (options.steering ? options.steering.takeWhenAvailable() : Effect.never).pipe(
          Effect.map((messages) => ({ kind: "message" as const, messages })),
        ),
        Effect.sleep(Duration.minutes(waitMinutes)).pipe(Effect.as({ kind: "deadline" as const })),
      ]);
      if (outcome.kind === "deadline") return { kind: "timeout" };
      if (outcome.kind === "message") return { kind: "steering", messages: outcome.messages };
      for (const command of outcome.commands) {
        if (command.kind === "retry") {
          retryRequests.add(command.agentId);
          continue;
        }
        stoppedAgentIds.add(command.agentId);
      }
      // An interrupt alone changes nothing here — nothing is running — so only a
      // retry that actually re-ran a task resumes the workflow.
      const applied = yield* applyPendingRetries();
      return applied > 0 ? { kind: "retry" } : { kind: "timeout" };
    },
  );

  const runWorkflow = Effect.gen(function* () {
    // Pipelines have run since the last summary pass: the orchestrator owes the
    // round a check-in once that work settles.
    let roundRanPipelines = false;
    let planningPassOwed = true;
    // Set once the orchestrator has been handed the current round's failures, so
    // the same failure is not re-litigated forever; cleared as soon as it
    // dispatches work of its own.
    let failurePassTaken = false;
    while (orchestratorPasses < options.settings.maxOrchestratorPasses) {
      if (yield* isCancelled) {
        yield* waitForPipelinesToSettle();
        return yield* finish(
          "stopped",
          "The workflow was stopped before all planned work finished.",
        );
      }

      // Steering wins over waiting: the user is blocked on it, and pipelines
      // already running keep working while the pass runs. Only a settled round
      // with nothing queued earns a summary pass.
      const planning = planningPassOwed;
      planningPassOwed = false;
      let steering: ReadonlyArray<string> = [];
      if (!planning) {
        steering =
          runningPipelines.size > 0
            ? yield* Effect.race(
                waitForPipelinesToSettle().pipe(Effect.as([] as ReadonlyArray<string>)),
                // No channel means steering can never arrive here, so the wait
                // is only ever the running round settling.
                options.steering ? options.steering.takeWhenAvailable() : Effect.never,
              )
            : yield* options.steering?.pending() ?? Effect.succeed([]);
        if (steering.length === 0) {
          // A retry re-runs its task in place and can repair the round, so it is
          // applied before anything else is concluded from the failures.
          const retried = yield* applyPendingRetries();
          if (retried > 0) {
            roundRanPipelines = true;
            failurePassTaken = false;
            continue;
          }
          if (pipelineOrder.some((id) => pipelines.get(id)?.status === "failed")) {
            if (!failurePassTaken) {
              // Hand the failures to the orchestrator before giving up: the
              // summary pass carries the dispatch outcomes, so it can dispatch a
              // fix-up pipeline instead of the run ending here.
              failurePassTaken = true;
            } else {
              const wait = yield* awaitRetryOrSteering();
              if (wait.kind === "retry") {
                roundRanPipelines = true;
                failurePassTaken = false;
                continue;
              }
              if (wait.kind === "timeout") {
                return yield* finishOutstanding("One or more pipelines failed.");
              }
              steering = wait.messages;
            }
          } else {
            const start = yield* startReadyPipelines();
            if (start.deadlocked) {
              return yield* finish("failed", "One or more pipelines failed.");
            }
            // A dependency chain just advanced: keep the round going instead of
            // checking in between chain links.
            if (start.started > 0) {
              roundRanPipelines = true;
              continue;
            }
            if (!roundRanPipelines) {
              return yield* finish(
                "completed",
                "All planned work finished — the orchestrator found no follow-up tasks.",
                finalReport,
              );
            }
          }
        }
      }

      const pass = orchestratorPasses;
      orchestratorPasses += 1;
      const pipelineCountBefore = pipelineOrder.length;
      passDoneReport = null;
      const orchestratorResult = yield* runOrchestratorPass({ pass, steering });
      if (yield* isCancelled) {
        yield* waitForPipelinesToSettle();
        return yield* finish(
          "stopped",
          "The workflow was stopped before all planned work finished.",
        );
      }
      if (orchestratorResult.code !== 0) {
        // Agents that are already running were not at fault; let them finish
        // before the workflow reports the failure.
        yield* waitForPipelinesToSettle();
        return yield* finish("failed", orchestratorResult.error ?? "the orchestrator failed");
      }
      if (pipelineOrder.length === pipelineCountBefore && passDoneReport !== null) {
        // This pass dispatched no new pipelines, so its done payload — if any —
        // is the workflow's final report.
        finalReport = passDoneReport;
      }
      if (pipelineOrder.length > pipelineCountBefore) {
        // Fresh work is on the way, so the next failure deserves its own pass.
        failurePassTaken = false;
      }
      if (steering.length === 0) {
        roundRanPipelines = false;
      }
      const start = yield* startReadyPipelines();
      if (start.deadlocked) {
        return yield* finish("failed", "One or more pipelines failed.");
      }
      if (start.started > 0) {
        roundRanPipelines = true;
      }
    }

    yield* waitForPipelinesToSettle();
    return yield* finish(
      "completed",
      `Reached the maximum of ${options.settings.maxOrchestratorPasses} orchestrator passes.`,
      finalReport,
    );
  });

  return yield* runWorkflow.pipe(
    Effect.ensuring(Scope.close(scope, Exit.void).pipe(Effect.ignore)),
  );
});

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function setContext(
  context: AgentTunnelSharedContext,
  key: string,
  value: string,
): string | undefined {
  try {
    context.set(key, value);
    return undefined;
  } catch (error) {
    return error instanceof Error ? error.message : String(error);
  }
}

function appendContext(
  context: AgentTunnelSharedContext,
  key: string,
  value: string,
  authorId: string,
): string | undefined {
  try {
    context.append(key, value, authorId);
    return undefined;
  } catch (error) {
    return error instanceof Error ? error.message : String(error);
  }
}

function stringArray(value: unknown): ReadonlyArray<string> {
  return Array.isArray(value)
    ? value.filter((entry): entry is string => typeof entry === "string")
    : [];
}

function nextStepsArray(value: unknown): ReadonlyArray<string> {
  const entries = typeof value === "string" ? value.split("\n") : stringArray(value);
  return entries.map((entry) => entry.trim()).filter((entry) => entry.length > 0);
}

function safeKey(value: string): string {
  const normalized = value
    .toLowerCase()
    .replace(/[^a-z0-9_]+/g, "_")
    .replace(/^_+/, "");
  return /^[a-z]/.test(normalized) ? normalized : `pipeline_${normalized || "default"}`;
}

function stripAnsi(value: string): string {
  return value.replace(/\u001B\[[0-?]*[ -/]*[@-~]/g, "");
}

function formatWorkflowFinishMarkdown(input: {
  readonly status: AgentTunnelRunResult["status"];
  readonly summary: string;
  readonly resultText: string;
  readonly nextSteps: ReadonlyArray<string>;
  readonly pipelineOrder: ReadonlyArray<string>;
  readonly pipelines: Map<string, PipelineState>;
  readonly taskRecords: ReadonlyArray<TaskRecord>;
  readonly learnings: ReadonlyArray<AgentTunnelContextLearning>;
  readonly durationMs: number;
}): string {
  const {
    status,
    summary,
    resultText,
    nextSteps,
    pipelineOrder,
    pipelines,
    taskRecords,
    learnings,
    durationMs,
  } = input;
  const icon = status === "completed" ? "✅" : status === "failed" ? "❌" : "⏹️";
  const orderedRecords = [...taskRecords].sort(
    (left, right) =>
      pipelineOrder.indexOf(left.pipelineId) - pipelineOrder.indexOf(right.pipelineId) ||
      left.agentIndex - right.agentIndex,
  );
  // Visible by default: the workflow's actual outcome and what to do next lead
  // the report, ahead of the machinery tables.
  const lines: string[] = [
    `\n---\n\n## ${icon} Workflow ${status} · ${formatDuration(durationMs)}\n\n${summary}\n`,
    `\n### What happened\n\n${resultText}\n`,
    `\n### Next steps\n\n${nextSteps.map((step) => `- ${step}`).join("\n")}\n`,
  ];

  if (learnings.length > 0) {
    lines.push("\n### Inherited context updates\n");
    for (const learning of learnings) {
      lines.push(`- \`${learning.category}\` · ${learning.entry}`);
    }
    lines.push("");
  }

  if (pipelineOrder.length > 0) {
    lines.push("\n### Pipelines\n");
    lines.push("| Pipeline | Agents | Status | Time |");
    lines.push("| --- | --- | --- | --- |");
    for (const id of pipelineOrder) {
      const pipeline = pipelines.get(id);
      const records = orderedRecords.filter((record) => record.pipelineId === id);
      const completed = records.filter((record) => record.status === "completed").length;
      const pipelineStatus = pipeline?.status ?? "pending";
      // A failed pipeline whose only losses were user interrupts is reported as
      // stopped, matching its agent rows.
      const pipelineCell =
        pipelineStatus === "completed"
          ? "✅ completed"
          : pipelineStatus === "failed"
            ? records.some((record) => record.status === "failed")
              ? "❌ failed"
              : "⏹️ stopped"
            : "⏳ pending";
      const agentsCell = records.length === 0 ? "none" : `${completed}/${records.length}`;
      const timeCell =
        pipeline?.durationMs === undefined ? "—" : formatDuration(pipeline.durationMs);
      lines.push(`| \`${id}\` | ${agentsCell} | ${pipelineCell} | ${timeCell} |`);
    }
    lines.push("");
  }

  if (orderedRecords.length > 0) {
    lines.push("\n### Agents\n");
    lines.push("| Agent | Pipeline | Status | Time | Output |");
    lines.push("| --- | --- | --- | --- | --- |");
    for (const record of orderedRecords) {
      const lineCount = `${record.outputLines} ${record.outputLines === 1 ? "line" : "lines"}`;
      const attemptCount =
        record.attempt && record.attempt > 1 ? ` · ${record.attempt} attempts` : "";
      lines.push(
        `| ${roleIcon(record.role)} ${record.role} · ${record.label} | \`${record.pipelineId}\` | ${statusIcon(record.status)} ${statusLabel(record.status)} | ${formatDuration(record.durationMs)} | ${lineCount}${attemptCount} |`,
      );
    }
    lines.push("");
  }

  // Failed first, then stopped: an interrupted agent is listed too, so the
  // report never hides work the workflow did not finish.
  const failures = orderedRecords
    .filter((record) => record.status !== "completed")
    .sort((left, right) => (left.status === right.status ? 0 : left.status === "failed" ? -1 : 1));
  if (failures.length > 0) {
    lines.push("\n### Failures\n");
    for (const record of failures) {
      lines.push(
        record.status === "failed"
          ? `- ❌ **${record.role} · ${record.label}** — ${record.error ?? "no error detail"}`
          : `- ⏹️ **${record.role} · ${record.label}** — stopped before it finished`,
      );
    }
    lines.push("");
  }

  return lines.join("\n");
}
