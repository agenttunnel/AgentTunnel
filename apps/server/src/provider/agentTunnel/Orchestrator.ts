import type { AgentTunnelSettings } from "@t3tools/contracts";
import * as Cause from "effect/Cause";
import * as Clock from "effect/Clock";
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
  readonly contextDump?: string;
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
  readonly threadHistory?: ReadonlyArray<{ readonly task: string; readonly summary?: string }>;
  readonly priorSharedContext?: string;
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
  durationMs?: number;
}

interface ProcessResult {
  readonly code: number | null;
  readonly error?: string;
}

interface TaskRecord {
  readonly id: string;
  readonly role: "executor" | "reviewer";
  readonly label: string;
  readonly pipelineId: string;
  readonly agentIndex: number;
  readonly status: AgentTunnelAgentStatus;
  readonly durationMs: number;
  readonly outputLines: number;
  readonly error?: string;
}

const MAX_AGENT_OUTPUT_LINES = 200;
const MAX_PROGRESS_CHARS = 500;
const TASK_LABEL_MAX_CHARS = 72;
const ORCHESTRATOR_PASS_TIMEOUT: Duration.Input = "3 minutes";

const roleIcon = (role: "executor" | "reviewer"): string => (role === "executor" ? "🛠️" : "🔍");

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
  const workflowStartedAt = yield* Clock.currentTimeMillis;
  let nextTaskIndex = 0;
  let orchestratorPasses = 0;
  let orchestratorThinkingOpen = false;

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
    Effect.gen(function* () {
      yield* trace("workflow finished", {
        workflowId: options.workflowId,
        status,
        summary: summary.slice(0, 160),
      });
      const finishedAt = yield* Clock.currentTimeMillis;
      yield* emit({
        type: "orchestrator.output",
        text: formatWorkflowFinishMarkdown({
          status,
          summary,
          pipelineOrder,
          pipelines,
          taskRecords,
          durationMs: finishedAt - workflowStartedAt,
        }),
      });
      yield* emit({
        type: "workflow.completed",
        workflowId: options.workflowId,
        status,
        summary,
      });
      return {
        status,
        summary,
        contextDump: context.renderForPrompt(),
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
        if ((role !== "executor" && role !== "reviewer") || task.length === 0) {
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
        const label = humanTaskLabel(task);
        yield* trace("task dispatched", {
          workflowId: options.workflowId,
          role,
          pipeline: pipelineId,
          mode,
          dependsOn: dependsOn.join(","),
          task: task.slice(0, 160),
        });
        yield* emitProgress(`Queued ${role} "${label}" in pipeline "${pipelineId}" (${mode})`);
        const dependencyText =
          dependsOn.length > 0 ? ` · after ${dependsOn.map((id) => `\`${id}\``).join(", ")}` : "";
        const contextText =
          contextKeys.length > 0
            ? ` · uses ${contextKeys.map((key) => `\`${key}\``).join(", ")}`
            : "";
        yield* emit({
          type: "orchestrator.output",
          text: `\n- ${roleIcon(role)} Queued **${role} · ${label}** in \`${pipelineId}\` · ${mode}${dependencyText}${contextText}\n`,
        });
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

  const summaryPrompt = (): string => {
    const pipelineSummary = pipelineOrder
      .map((id) => {
        const pipeline = pipelines.get(id);
        return `- ${id}: ${pipeline?.status ?? "pending"}`;
      })
      .join("\n");
    return `${ORCHESTRATOR_PROMPT}${historyBlock()}\n\n## Summary pass\n\nThe team has completed the following work. Dispatch follow-up tasks when needed, or call done.\n\n### Pipelines\n${pipelineSummary || "(none)"}\n\n### Shared context\n${context.renderForPrompt()}`;
  };

  const runOrchestratorPass = Effect.fn("runAgentTunnel.runOrchestratorPass")(function* (
    pass: number,
  ) {
    if (pass > 0) {
      yield* emit({
        type: "orchestrator.output",
        text: `\n---\n\n## Summary pass ${pass + 1}\n\nThe orchestrator is checking the completed work for follow-up tasks.\n`,
      });
    }
    const result = yield* runProcess(
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
            ? `${ORCHESTRATOR_PROMPT}${historyBlock()}\n\n## Shared context\n${context.renderForPrompt()}\n\n## Current user task\n${options.userTask}`
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
    const label = humanTaskLabel(task.task);
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
      text: `\n- ${roleIcon(task.role)} **${task.role} · ${label}** started in \`${task.pipelineId}\`${model ? ` · ${model}` : ""}\n\n${taskQuote}\n\n`,
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
    const systemPrompt = task.role === "executor" ? EXECUTOR_PROMPT : REVIEWER_PROMPT;
    const prompt = `${systemPrompt}\n\n## Task\n${task.task}${contextBlock}\n\nCall done when finished.`;
    let progressLineCount = 0;
    let firstOutputLine = true;
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
        progressLineCount += 1;
        const prefix = firstOutputLine
          ? `\n<details>\n<summary>📋 Output · ${label}</summary>\n\n\`\`\`\n`
          : "";
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
      yield* emit({ type: "orchestrator.output", text: "```\n\n</details>\n\n" });
    }

    const cancelled = yield* isCancelled;
    const status: AgentTunnelAgentStatus = cancelled
      ? "stopped"
      : result.code === 0
        ? "completed"
        : "failed";
    const durationMs = (yield* Clock.currentTimeMillis) - startedAt;
    const duration = formatDuration(durationMs);
    const errorExcerpt = result.error ? plainTextPreview(result.error, 160) : undefined;
    const resultKey = `result_${safeKey(task.pipelineId)}`;
    appendContext(
      context,
      resultKey,
      `## ${task.role} ${agentId} (${status})\n\nTask: ${task.task}\n\n${output.join("\n")}`,
      agentId,
    );
    const outcomeDetail = errorExcerpt
      ? ` — ${errorExcerpt}`
      : ` · ${progressLineCount} line${progressLineCount === 1 ? "" : "s"} of output`;
    const completionMarkdown = `${statusIcon(status)} **${task.role} · ${label}** ${statusLabel(status)} in ${duration}${outcomeDetail}`;
    const completionSummary = `${label} — ${statusLabel(status)} in ${duration}${outcomeDetail}`;
    yield* emit({
      type: "orchestrator.output",
      text: `${completionMarkdown}\n\n`,
    });
    taskRecords.push({
      id: task.id,
      role: task.role,
      label,
      pipelineId: task.pipelineId,
      agentIndex: task.agentIndex,
      status,
      durationMs,
      outputLines: progressLineCount,
      ...(errorExcerpt ? { error: errorExcerpt } : {}),
    });
    yield* emit({
      type: "agent.completed",
      workflowId: options.workflowId,
      agentId,
      role: task.role,
      status,
      summary: completionSummary,
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
    const pipelineIcon = ok ? "✅" : "❌";
    yield* emit({
      type: "orchestrator.output",
      text: `\n${pipelineIcon} **Pipeline \`${pipeline.id}\` ${pipeline.status}** · ${breakdown} · ${formatDuration(pipeline.durationMs)}\n\n`,
    });
    yield* emitProgress(`Pipeline "${pipeline.id}" ${pipeline.status} · ${breakdown}`);
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
        yield* emit({
          type: "orchestrator.output",
          text: `\n> ⚠️ **Pipeline dependency deadlock** — no pipelines can start due to circular dependencies.\n`,
        });
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
  yield* emit({
    type: "orchestrator.output",
    text: `## AgentTunnel workflow\n\n**Task:** ${options.userTask}\n\nThe orchestrator is analyzing the task and will dispatch subagents shortly.\n`,
  });

  while (orchestratorPasses < options.settings.maxOrchestratorPasses) {
    const pipelineCountBefore = pipelineOrder.length;
    const orchestratorResult = yield* runOrchestratorPass(orchestratorPasses);
    if (yield* isCancelled) {
      return yield* finish("stopped", "The workflow was stopped before all planned work finished.");
    }
    if (orchestratorResult.code !== 0) {
      return yield* finish("failed", orchestratorResult.error ?? "the orchestrator failed");
    }
    if (pipelineOrder.length === pipelineCountBefore) {
      return yield* finish(
        "completed",
        "All planned work finished — the orchestrator found no follow-up tasks.",
      );
    }
    if (!(yield* runNewPipelines(pipelineCountBefore))) {
      const wasStopped = yield* isCancelled;
      return yield* finish(
        wasStopped ? "stopped" : "failed",
        wasStopped ? "The workflow was stopped." : "One or more pipelines failed.",
      );
    }
    orchestratorPasses += 1;
  }

  return yield* finish(
    "completed",
    `Reached the maximum of ${options.settings.maxOrchestratorPasses} orchestrator passes.`,
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
  readonly pipelineOrder: ReadonlyArray<string>;
  readonly pipelines: Map<string, PipelineState>;
  readonly taskRecords: ReadonlyArray<TaskRecord>;
  readonly durationMs: number;
}): string {
  const { status, summary, pipelineOrder, pipelines, taskRecords, durationMs } = input;
  const icon = status === "completed" ? "✅" : status === "failed" ? "❌" : "⏹️";
  const orderedRecords = [...taskRecords].sort(
    (left, right) =>
      pipelineOrder.indexOf(left.pipelineId) - pipelineOrder.indexOf(right.pipelineId) ||
      left.agentIndex - right.agentIndex,
  );
  const lines: string[] = [
    `\n---\n\n## ${icon} Workflow ${status} · ${formatDuration(durationMs)}\n\n${summary}\n`,
  ];

  if (pipelineOrder.length > 0) {
    lines.push("\n### Pipelines\n");
    lines.push("| Pipeline | Agents | Status | Time |");
    lines.push("| --- | --- | --- | --- |");
    for (const id of pipelineOrder) {
      const pipeline = pipelines.get(id);
      const records = orderedRecords.filter((record) => record.pipelineId === id);
      const completed = records.filter((record) => record.status === "completed").length;
      const pipelineStatus = pipeline?.status ?? "pending";
      const pipelineCell =
        pipelineStatus === "completed"
          ? "✅ completed"
          : pipelineStatus === "failed"
            ? "❌ failed"
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
      lines.push(
        `| ${roleIcon(record.role)} ${record.role} · ${record.label} | \`${record.pipelineId}\` | ${statusIcon(record.status)} ${statusLabel(record.status)} | ${formatDuration(record.durationMs)} | ${record.outputLines} ${record.outputLines === 1 ? "line" : "lines"} |`,
      );
    }
    lines.push("");
  }

  const failures = orderedRecords.filter((record) => record.status === "failed");
  if (failures.length > 0) {
    lines.push("\n### Failures\n");
    for (const record of failures) {
      lines.push(
        `- ❌ **${record.role} · ${record.label}** — ${record.error ?? "no error detail"}`,
      );
    }
    lines.push("");
  }

  return lines.join("\n");
}
