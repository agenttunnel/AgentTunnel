import type { AgentTunnelSettings } from "@t3tools/contracts";
import * as Cause from "effect/Cause";
import * as Duration from "effect/Duration";
import * as Effect from "effect/Effect";
import * as Option from "effect/Option";
import * as Schema from "effect/Schema";
import * as Scope from "effect/Scope";
import * as Stream from "effect/Stream";

import { AgentTunnelSharedContext } from "./SharedContext.ts";
import { parseAgentTunnelToolCall, type AgentTunnelToolCall } from "./protocol.ts";
import { EXECUTOR_PROMPT, ORCHESTRATOR_PROMPT, REVIEWER_PROMPT } from "./prompts.ts";

export interface AgentTunnelProcessSpec {
  readonly id: string;
  readonly threadId: string;
  readonly harness: string;
  readonly model?: string;
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
      readonly role: "executor" | "reviewer";
      readonly task: string;
      readonly pipelineId: string;
      readonly agentIndex: number;
      readonly phaseIndex: number;
      readonly model?: string;
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
      readonly role: "executor" | "reviewer";
      readonly status: AgentTunnelAgentStatus;
      readonly summary?: string;
    };

export interface AgentTunnelRunResult {
  readonly status: "completed" | "failed" | "stopped";
  readonly summary: string;
}

export interface AgentTunnelRunOptions {
  readonly threadId: string;
  readonly workflowId: string;
  readonly userTask: string;
  readonly cwd: string;
  readonly model?: string;
  readonly settings: AgentTunnelSettings;
  readonly process: AgentTunnelProcessRunner;
  readonly emit: (event: AgentTunnelEvent) => Effect.Effect<void>;
  readonly isCancelled: Effect.Effect<boolean>;
  readonly orchestratorPassTimeout?: Duration.Input;
}

interface DispatchedTask {
  readonly id: string;
  readonly role: "executor" | "reviewer";
  readonly task: string;
  readonly context: ReadonlyArray<string>;
  readonly pipelineId: string;
  readonly agentIndex: number;
}

interface PipelineState {
  readonly id: string;
  readonly mode: "parallel" | "sequential";
  readonly dependsOn: ReadonlyArray<string>;
  status: "pending" | "running" | "completed" | "failed";
}

interface ProcessResult {
  readonly code: number | null;
  readonly error?: string;
}

const MAX_AGENT_OUTPUT_LINES = 100;
const MAX_PROGRESS_CHARS = 240;
const ORCHESTRATOR_PASS_TIMEOUT: Duration.Input = "3 minutes";

const trace = (message: string, fields: Record<string, string | number | boolean>) =>
  Effect.log(message, fields).pipe(Effect.annotateLogs({ scope: "agenttunnel.orchestrator" }));

/** Execute AgentTunnel's orchestration loop without depending on a UI store. */
export const runAgentTunnel = Effect.fn("runAgentTunnel")(function* (
  options: AgentTunnelRunOptions,
) {
  const context = new AgentTunnelSharedContext();
  const pipelines = new Map<string, PipelineState>();
  const pipelineOrder: string[] = [];
  const tasks: DispatchedTask[] = [];
  let nextTaskIndex = 0;
  let orchestratorPasses = 0;

  const emit = options.emit;
  const isCancelled = options.isCancelled;
  const orchestratorPassTimeout = options.orchestratorPassTimeout ?? ORCHESTRATOR_PASS_TIMEOUT;

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

  const finish = (status: AgentTunnelRunResult["status"], summary: string) =>
    trace("workflow finished", {
      workflowId: options.workflowId,
      status,
      summary: summary.slice(0, 160),
    }).pipe(
      Effect.andThen(
        emit({
          type: "workflow.completed",
          workflowId: options.workflowId,
          status,
          summary,
        }),
      ),
      Effect.as({ status, summary } satisfies AgentTunnelRunResult),
    );

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
      return { code: null, error: "orchestrator pass timed out" };
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
        const error = setContext(
          context,
          stringValue(call.arguments.key),
          stringValue(call.arguments.value),
        );
        if (!error) {
          yield* emit({
            type: "orchestrator.output",
            text: `Context updated: ${stringValue(call.arguments.key)}`,
          });
        } else {
          yield* emit({
            type: "orchestrator.output",
            text: `Context update failed: ${error}`,
          });
        }
        return;
      }

      if (call.name === "dispatch_task") {
        const role = stringValue(call.arguments.role);
        const task = stringValue(call.arguments.task).trim();
        if ((role !== "executor" && role !== "reviewer") || task.length === 0) {
          yield* emit({ type: "orchestrator.output", text: "Invalid dispatch_task arguments." });
          return;
        }

        const pipelineId =
          stringValue(call.arguments.pipeline).trim() || `pipeline_${nextTaskIndex + 1}`;
        const mode = call.arguments.mode === "sequential" ? "sequential" : "parallel";
        const dependsOn = stringArray(call.arguments.depends_on);
        const contextKeys = stringArray(call.arguments.context);

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
        });
        yield* trace("task dispatched", {
          workflowId: options.workflowId,
          role,
          pipeline: pipelineId,
          mode,
          dependsOn: dependsOn.join(","),
          task: task.slice(0, 160),
        });
        yield* emitProgress(`Dispatched ${role} task in ${pipelineId}`);
        return;
      }

      if (call.name === "done") {
        yield* trace("orchestrator finished pass", {
          workflowId: options.workflowId,
          reason: stringValue(call.arguments.reason).slice(0, 120),
        });
      } else {
        yield* emit({
          type: "orchestrator.output",
          text: `Unknown orchestrator action: ${call.name}`,
        });
      }
    },
  );

  const handleOrchestratorLine = (line: string) => {
    const clean = stripAnsi(line).trim();
    if (clean.length === 0) return Effect.void;
    const call = parseAgentTunnelToolCall(clean);
    if (call) {
      return trace("orchestrator tool call", {
        name: call.name,
        preview: clean.slice(0, 120),
      }).pipe(Effect.andThen(handleOrchestratorToolCall(call)));
    }
    return trace("orchestrator output line", { preview: clean.slice(0, 120) }).pipe(
      Effect.andThen(emit({ type: "orchestrator.output", text: clean })),
    );
  };

  const summaryPrompt = (): string => {
    const pipelineSummary = pipelineOrder
      .map((id) => {
        const pipeline = pipelines.get(id);
        return `- ${id}: ${pipeline?.status ?? "pending"}`;
      })
      .join("\n");
    return `${ORCHESTRATOR_PROMPT}\n\n## Summary pass\n\nThe team has completed the following work. Dispatch follow-up tasks when needed, or call done.\n\n### Pipelines\n${pipelineSummary || "(none)"}\n\n### Shared context\n${context.renderForPrompt()}`;
  };

  const runOrchestratorPass = Effect.fn("runAgentTunnel.runOrchestratorPass")(function* (
    pass: number,
  ) {
    if (pass > 0) {
      yield* emit({
        type: "orchestrator.output",
        text: `Orchestrator summary pass ${pass + 1}`,
      });
    }
    return yield* runProcess(
      {
        id: `orchestrator_${pass + 1}`,
        threadId: options.threadId,
        harness: options.settings.orchestratorHarness,
        ...(options.settings.orchestratorModel || options.model
          ? { model: options.settings.orchestratorModel || options.model }
          : {}),
        cwd: options.cwd,
        prompt:
          pass === 0
            ? `${ORCHESTRATOR_PROMPT}\n\n## User task\n${options.userTask}`
            : summaryPrompt(),
        dangerouslySkipPermissions: options.settings.dangerouslySkipPermissions,
      },
      handleOrchestratorLine,
      orchestratorPassTimeout,
    );
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

  const runTask = Effect.fn("runAgentTunnel.runTask")(function* (task: DispatchedTask) {
    const pipelineIndex = Math.max(0, pipelineOrder.indexOf(task.pipelineId));
    const agentId = `agenttunnel_${options.workflowId}_${task.id}`;
    const model =
      task.role === "executor"
        ? options.settings.executorModel ||
          options.settings.orchestratorModel ||
          options.model ||
          undefined
        : options.settings.reviewerModel ||
          options.settings.orchestratorModel ||
          options.model ||
          undefined;
    const harness =
      task.role === "executor"
        ? options.settings.executorHarness
        : options.settings.reviewerHarness;
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
      ...(model ? { model } : {}),
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
    const systemPrompt = task.role === "executor" ? EXECUTOR_PROMPT : REVIEWER_PROMPT;
    const prompt = `${systemPrompt}\n\n## Task\n${task.task}${contextBlock}\n\nCall done when finished.`;
    const result = yield* runProcess(
      {
        id: agentId,
        threadId: options.threadId,
        harness,
        ...(model ? { model } : {}),
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
        return emit({
          type: "agent.progress",
          workflowId: options.workflowId,
          agentId,
          summary: truncate(clean),
        });
      },
    );

    const cancelled = yield* isCancelled;
    const status: AgentTunnelAgentStatus = cancelled
      ? "stopped"
      : result.code === 0
        ? "completed"
        : "failed";
    const summary = result.error ?? output.at(-1);
    const resultKey = `result_${safeKey(task.pipelineId)}`;
    appendContext(
      context,
      resultKey,
      `## ${task.role} ${agentId} (${status})\n\nTask: ${task.task}\n\n${output.join("\n")}`,
      agentId,
    );
    yield* emit({
      type: "agent.completed",
      workflowId: options.workflowId,
      agentId,
      role: task.role,
      status,
      ...(summary ? { summary: truncate(summary) } : {}),
    });
    yield* trace("agent completed", {
      workflowId: options.workflowId,
      agentId,
      role: task.role,
      status,
      pipeline: task.pipelineId,
      exitCode: result.code ?? -1,
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
    yield* emitProgress(`Starting pipeline ${pipeline.id}`);
    const pipelineTasks = tasks.filter((task) => task.pipelineId === pipeline.id);
    const results: ReadonlyArray<AgentTunnelAgentStatus> =
      pipeline.mode === "parallel"
        ? yield* Effect.forEach(pipelineTasks, runTask, { concurrency: "unbounded" })
        : yield* runSequential(pipelineTasks);
    const ok = results.every((status) => status === "completed");
    pipeline.status = ok ? "completed" : "failed";
    yield* trace("pipeline finished", {
      workflowId: options.workflowId,
      pipeline: pipeline.id,
      status: pipeline.status,
    });
    yield* emitProgress(`${pipeline.id} ${pipeline.status}`);
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

  const runNewPipelines = Effect.fn("runAgentTunnel.runNewPipelines")(function* (
    startIndex: number,
  ) {
    const targetIds = pipelineOrder.slice(startIndex);
    const completed = new Set(
      pipelineOrder.filter((id) => pipelines.get(id)?.status === "completed"),
    );
    let remaining = new Set(targetIds);

    while (remaining.size > 0) {
      if (yield* isCancelled) return false;
      const ready = [...remaining]
        .map((id) => pipelines.get(id))
        .filter(
          (pipeline): pipeline is PipelineState =>
            pipeline !== undefined &&
            pipeline.dependsOn.every((dependency) => completed.has(dependency)),
        );

      if (ready.length === 0) {
        for (const id of remaining) {
          const pipeline = pipelines.get(id);
          if (pipeline) pipeline.status = "failed";
        }
        yield* emitProgress("Pipeline dependency deadlock");
        return false;
      }

      const results = yield* Effect.forEach(ready, runPipeline, { concurrency: "unbounded" });
      ready.forEach((pipeline) => remaining.delete(pipeline.id));
      ready.forEach((pipeline) => {
        if (pipeline.status === "completed") completed.add(pipeline.id);
      });
      if (results.some((ok) => !ok)) return false;
    }

    return true;
  });

  yield* emit({
    type: "workflow.started",
    workflowId: options.workflowId,
    title: options.userTask,
    ...(options.model ? { model: options.model } : {}),
  });

  while (orchestratorPasses < options.settings.maxOrchestratorPasses) {
    const pipelineCountBefore = pipelineOrder.length;
    const orchestratorResult = yield* runOrchestratorPass(orchestratorPasses);
    if (yield* isCancelled) return yield* finish("stopped", "workflow stopped");
    if (orchestratorResult.code !== 0) {
      return yield* finish("failed", orchestratorResult.error ?? "orchestrator failed");
    }
    if (pipelineOrder.length === pipelineCountBefore) {
      return yield* finish("completed", "orchestrator finished");
    }
    if (!(yield* runNewPipelines(pipelineCountBefore))) {
      return yield* finish(
        (yield* isCancelled) ? "stopped" : "failed",
        (yield* isCancelled) ? "workflow stopped" : "one or more pipelines failed",
      );
    }
    orchestratorPasses += 1;
  }

  return yield* finish("completed", "reached maximum orchestrator passes");
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

function safeKey(value: string): string {
  const normalized = value
    .toLowerCase()
    .replace(/[^a-z0-9_]+/g, "_")
    .replace(/^_+/, "");
  return /^[a-z]/.test(normalized) ? normalized : `pipeline_${normalized || "default"}`;
}

function truncate(value: string): string {
  return value.length <= MAX_PROGRESS_CHARS
    ? value
    : `${value.slice(0, MAX_PROGRESS_CHARS - 1)}...`;
}

function stripAnsi(value: string): string {
  return value.replace(/\u001B\[[0-?]*[ -/]*[@-~]/g, "");
}
