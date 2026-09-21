// @effect-diagnostics nodeBuiltinImport:off
import {
  EventId,
  MessageId,
  OrchestrationAgentLogEntry,
  ProviderDriverKind,
  ProviderInstanceId,
  RuntimeItemId,
  RuntimeTaskId,
  ThreadId,
  TurnId,
  type AgentTunnelSettings,
  type ProviderInstanceEnvironment,
  type ProviderRuntimeEvent,
  type ProviderSession,
  type TaskWorkflowPhase,
  type ThreadMessageSteeringStatus,
} from "@t3tools/contracts";
import * as Cause from "effect/Cause";
import * as Crypto from "effect/Crypto";
import * as DateTime from "effect/DateTime";
import * as Effect from "effect/Effect";
import * as Exit from "effect/Exit";
import * as FileSystem from "effect/FileSystem";
import * as Path from "effect/Path";
import * as Queue from "effect/Queue";
import * as Ref from "effect/Ref";
import * as Schema from "effect/Schema";
import * as Scope from "effect/Scope";
import * as Stream from "effect/Stream";
import * as NodeFSP from "node:fs/promises";
import * as NodePath from "node:path";
import { ChildProcess, ChildProcessSpawner } from "effect/unstable/process";

import {
  ProviderAdapterRequestError,
  ProviderAdapterSessionClosedError,
  ProviderAdapterSessionNotFoundError,
  type ProviderAdapterError,
} from "../Errors.ts";
import { ServerConfig } from "../../config.ts";
import { type EventNdjsonLogger } from "./EventNdjsonLogger.ts";
import { mergeProviderInstanceEnvironment } from "../ProviderInstanceEnvironment.ts";
import type { ProviderAdapterShape } from "../Services/ProviderAdapter.ts";
import {
  type AgentTunnelAgentCommand,
  type AgentTunnelEvent,
  type AgentTunnelRole,
  AgentTunnelProcessError,
  type AgentTunnelProcessHandle,
  type AgentTunnelProcessRunner,
  runAgentTunnel,
} from "../agentTunnel/Orchestrator.ts";
import {
  buildAgentTunnelSpawnArgs,
  agentTunnelHarnessKind,
  harnessUsageLogEntry,
  parseHarnessJsonLines,
  parseHarnessLogLine,
  parseHarnessUsageStep,
  type AgentLogEntry,
  type AgentTunnelHarnessKind,
  type AgentTunnelHarnessTransportKind,
} from "../agentTunnel/opencodeTransport.ts";
import {
  addHarnessUsageTotals,
  emptyHarnessUsageTotals,
  runtimeTaskUsageFromTotals,
  threadUsageSnapshotFromStep,
  type HarnessUsageTotals,
} from "../agentTunnel/harnessUsage.ts";
import { makeAgentTunnelInheritedContext } from "../agentTunnel/InheritedContextStore.ts";

const PROVIDER = ProviderDriverKind.make("agenttunnel");
const RESUME_VERSION = 1;
const MAX_AGENT_LOG_BYTES = 1024 * 1024;
const encodeLogEntry = Schema.encodeSync(Schema.fromJsonString(OrchestrationAgentLogEntry));

/**
 * One JSONL agent-log line. Usage entries are stamped with the moment the
 * adapter persisted them, so the drill-in can show when each model step ran;
 * the other kinds carry no clock of their own.
 */
export const encodeAgentLogEntry = (entry: OrchestrationAgentLogEntry, at: string): string =>
  `${encodeLogEntry(entry.kind === "usage" ? { ...entry, at } : entry)}\n`;

interface AgentTunnelAgentMetadata {
  readonly role: AgentTunnelRole;
  readonly task: string;
  readonly pipelineId: string;
  readonly agentIndex: number;
  readonly phaseIndex: number;
  readonly model?: string;
  /** CLI that ran this agent (executor/reviewer harness). */
  readonly harness?: string;
  /** Run number this agent is on; omitted means its first attempt. */
  readonly attempt?: number;
}

interface AgentTunnelSteeringEntry {
  readonly messageId?: MessageId;
  readonly text: string;
}

interface AgentTunnelSessionContext {
  session: ProviderSession;
  readonly cwd: string;
  readonly approvalPolicy: string | undefined;
  readonly sandboxMode: string | undefined;
  readonly cancelled: Ref.Ref<boolean>;
  readonly turns: Array<{ readonly id: TurnId; readonly items: Array<unknown> }>;
  readonly agents: Map<string, AgentTunnelAgentMetadata>;
  /** Per-agent cumulative usage for the active turn, keyed by process id. */
  readonly agentUsage: Map<string, HarnessUsageTotals>;
  /** Session-cumulative orchestrator usage; survives across turns. */
  readonly orchestratorUsage: HarnessUsageTotals;
  readonly agentLogBytes: Map<string, number>;
  readonly agentRawLogBytes: Map<string, number>;
  workflowTitle: string | undefined;
  workflowId: string | undefined;
  activeTurnId: TurnId | undefined;
  stopped: boolean;
  readonly threadHistory: Array<{ readonly task: string; readonly summary?: string }>;
  lastSharedContextDump: string | undefined;
  /**
   * Steering messages the user sent mid-turn, waiting for the orchestrator's
   * next pass. A queue, not an array: the orchestrator waits on it so a message
   * landing while no pass is in flight starts one immediately.
   */
  readonly steering: Queue.Queue<AgentTunnelSteeringEntry>;
  /**
   * Per-agent control the user sent while the turn runs: interrupts and retries
   * aimed at one sub-agent. The orchestrator drains it, so an interrupt marks
   * that agent stopped and a retry re-runs its task in place.
   */
  readonly agentControl: Queue.Queue<AgentTunnelAgentCommand>;
}

export interface AgentTunnelAdapterOptions {
  readonly instanceId: ProviderInstanceId;
  readonly environment?: ProviderInstanceEnvironment;
  readonly availableModels?: ReadonlyArray<string>;
  /** Optional per-thread NDJSON trace writer (provider-native event log). */
  readonly nativeEventLogger?: EventNdjsonLogger;
}

const nowIso = Effect.map(DateTime.now, DateTime.formatIso);

/** Adapter that exposes AgentTunnel's multi-process workflow as one AgentTunnel thread. */
export const makeAgentTunnelAdapter = Effect.fn("makeAgentTunnelAdapter")(function* (
  settings: AgentTunnelSettings,
  options: AgentTunnelAdapterOptions,
): Effect.fn.Return<
  ProviderAdapterShape<ProviderAdapterError>,
  never,
  | ChildProcessSpawner.ChildProcessSpawner
  | Crypto.Crypto
  | FileSystem.FileSystem
  | Path.Path
  | ServerConfig
  | Scope.Scope
> {
  const spawner = yield* ChildProcessSpawner.ChildProcessSpawner;
  const crypto = yield* Crypto.Crypto;
  const serverConfig = yield* ServerConfig;
  const inheritedContext = yield* makeAgentTunnelInheritedContext(
    serverConfig.orchestratorContextDir,
  );
  const runtimeScope = yield* Scope.make();
  const runtimeEvents = yield* Queue.unbounded<ProviderRuntimeEvent>();
  const sessions = new Map<ThreadId, AgentTunnelSessionContext>();
  const activeProcesses = new Map<ThreadId, Map<string, AgentTunnelProcessHandle["kill"]>>();
  const environment = mergeProviderInstanceEnvironment(options.environment);
  const agentOutputRoot = NodePath.join(serverConfig.providerLogsDir, "agent-output");
  const agentOutputDir = (threadId: string) =>
    NodePath.join(agentOutputRoot, threadId.replace(/[^a-zA-Z0-9_-]/g, "_"));
  const agentLogPath = (threadId: string, agentId: string, suffix: string) =>
    NodePath.join(
      agentOutputDir(threadId),
      `${agentId.replace(/[^a-zA-Z0-9_-]/g, "_")}${suffix}.log`,
    );
  const agentOutputPath = (threadId: string, agentId: string) =>
    agentLogPath(threadId, agentId, "");
  const appendLogFile = (threadId: ThreadId, agentId: string, filePath: string, text: string) =>
    Effect.tryPromise(async () => {
      await NodeFSP.mkdir(NodePath.dirname(filePath), { recursive: true });
      await NodeFSP.appendFile(filePath, text, "utf8");
    }).pipe(
      Effect.catch((cause) =>
        Effect.logWarning("AgentTunnel agent log write failed", {
          scope: "agenttunnel.adapter",
          threadId,
          agentId,
          cause,
        }),
      ),
    );
  /** Appends parsed entries to an agent's log, honoring its byte budget. */
  const appendParsedEntries = (
    threadId: ThreadId,
    agentId: string,
    entries: ReadonlyArray<AgentLogEntry>,
  ) =>
    Effect.gen(function* () {
      const context = sessions.get(threadId);
      const outputPath = agentOutputPath(threadId, agentId);
      // One clock read per call, so every entry it carries shares its stamp.
      const at = yield* nowIso;
      for (const entry of entries) {
        const line = encodeAgentLogEntry(entry, at);
        const written = context?.agentLogBytes.get(agentId) ?? 0;
        const bytes = Buffer.byteLength(line, "utf8");
        if (written + bytes > MAX_AGENT_LOG_BYTES) {
          return;
        }
        context?.agentLogBytes.set(agentId, written + bytes);
        yield* appendLogFile(threadId, agentId, outputPath, line);
      }
    });
  const appendAgentLogEntries = (
    threadId: ThreadId,
    agentId: string,
    rawLine: string,
    harnessKind: AgentTunnelHarnessKind,
  ) =>
    Effect.gen(function* () {
      const context = sessions.get(threadId);
      yield* appendParsedEntries(threadId, agentId, parseHarnessLogLine(rawLine, harnessKind));
      if (harnessKind === "other") {
        return;
      }
      // The raw stdout/stderr stream, verbatim, for the "Raw" view.
      const rawPath = agentLogPath(threadId, agentId, ".raw");
      const writtenRaw = context?.agentRawLogBytes.get(agentId) ?? 0;
      const rawText = `${rawLine}\n`;
      const rawBytes = Buffer.byteLength(rawText, "utf8");
      if (writtenRaw + rawBytes > MAX_AGENT_LOG_BYTES) {
        return;
      }
      context?.agentRawLogBytes.set(agentId, writtenRaw + rawBytes);
      yield* appendLogFile(threadId, agentId, rawPath, rawText);
    });
  const clearAgentOutput = (threadId: ThreadId) =>
    Effect.tryPromise(() =>
      NodeFSP.rm(agentOutputDir(threadId), { recursive: true, force: true }),
    ).pipe(Effect.ignore);

  const randomUuid = crypto.randomUUIDv4.pipe(
    Effect.mapError(
      (cause) =>
        new ProviderAdapterRequestError({
          provider: PROVIDER,
          method: "crypto.randomUUIDv4",
          detail: String(cause),
          cause,
        }),
    ),
  );
  const eventId = randomUuid.pipe(Effect.map(EventId.make));
  const buildEventBase = Effect.fn("AgentTunnelAdapter.buildEventBase")(function* (input: {
    readonly threadId: ThreadId;
    readonly turnId?: TurnId | undefined;
    readonly itemId?: string | undefined;
  }) {
    return {
      eventId: yield* eventId,
      provider: PROVIDER,
      providerInstanceId: options.instanceId,
      threadId: input.threadId,
      createdAt: yield* nowIso,
      ...(input.turnId ? { turnId: input.turnId } : {}),
      ...(input.itemId ? { itemId: RuntimeItemId.make(input.itemId) } : {}),
    };
  });

  const emit = (event: ProviderRuntimeEvent) =>
    Queue.offer(runtimeEvents, event).pipe(Effect.asVoid);

  // Steering lifecycle: "queued" when a mid-run message lands in the queue,
  // "injected" when the orchestrator takes it into a pass, "cancelled" when the
  // turn ends first. The projected user message carries this so clients label it.
  const emitSteeringUpdate = (input: {
    readonly threadId: ThreadId;
    readonly turnId?: TurnId | undefined;
    readonly messageId: MessageId;
    readonly status: ThreadMessageSteeringStatus;
  }) =>
    buildEventBase({
      threadId: input.threadId,
      ...(input.turnId ? { turnId: input.turnId } : {}),
    }).pipe(
      Effect.flatMap((base) =>
        emit({
          ...base,
          type: "steering.updated",
          payload: { messageId: input.messageId, status: input.status },
        }),
      ),
      Effect.orDie,
    );

  /** Marks drained entries with their delivery status, then returns their text. */
  const resolveSteering = (
    context: AgentTunnelSessionContext,
    threadId: ThreadId,
    entries: ReadonlyArray<AgentTunnelSteeringEntry>,
    status: ThreadMessageSteeringStatus,
  ) =>
    Effect.forEach(
      entries,
      (entry) =>
        entry.messageId
          ? emitSteeringUpdate({
              threadId,
              ...(context.activeTurnId ? { turnId: context.activeTurnId } : {}),
              messageId: entry.messageId,
              status,
            })
          : Effect.void,
      { concurrency: 1, discard: true },
    ).pipe(Effect.as(entries.map((entry) => entry.text)));

  /**
   * Drains whatever is queued without waiting. `Queue.takeAll` blocks for at
   * least one message, so the size check is what makes this non-blocking — and
   * the orchestrator is the only drainer, so the pair stays atomic in practice.
   */
  const drainQueuedSteering = Effect.fn("AgentTunnelAdapter.drainQueuedSteering")(function* (
    context: AgentTunnelSessionContext,
  ) {
    if ((yield* Queue.size(context.steering)) === 0) return [];
    return Array.from(yield* Queue.takeAll(context.steering));
  });

  const cancelPendingSteering = Effect.fn("AgentTunnelAdapter.cancelPendingSteering")(function* (
    context: AgentTunnelSessionContext,
    threadId: ThreadId,
  ) {
    yield* resolveSteering(context, threadId, yield* drainQueuedSteering(context), "cancelled");
  });

  /**
   * Drains queued agent controls without waiting, mirroring the steering drain:
   * `Queue.takeAll` blocks for at least one entry, so the size check is what
   * makes this non-blocking.
   */
  const drainQueuedAgentCommands = (context: AgentTunnelSessionContext) =>
    Queue.size(context.agentControl).pipe(
      Effect.flatMap((size) =>
        size === 0
          ? Effect.succeed<ReadonlyArray<AgentTunnelAgentCommand>>([])
          : Queue.takeAll(context.agentControl).pipe(Effect.map((entries) => Array.from(entries))),
      ),
    );

  const emitEvent = Effect.fn("AgentTunnelAdapter.emitEvent")(function* (
    threadId: ThreadId,
    turnId: TurnId | undefined,
    event: AgentTunnelEvent,
  ) {
    const context = sessions.get(threadId);
    if (!context) return;

    switch (event.type) {
      case "workflow.started": {
        context.workflowTitle = event.title;
        yield* emit({
          ...(yield* buildEventBase({ threadId, turnId, itemId: event.workflowId })),
          type: "task.started",
          payload: {
            taskId: RuntimeTaskId.make(event.workflowId),
            description: event.title,
            taskType: "local_workflow",
            title: event.title,
            role: "orchestrator",
            workflowName: "AgentTunnel",
            harness: event.harness,
            ...(event.model ? { model: event.model } : {}),
          },
        });
        return;
      }
      case "workflow.progress": {
        const phases: ReadonlyArray<TaskWorkflowPhase> = event.phases.map((phase) => ({
          index: phase.index,
          title: phase.title,
        }));
        yield* emit({
          ...(yield* buildEventBase({ threadId, turnId, itemId: event.workflowId })),
          type: "task.progress",
          payload: {
            taskId: RuntimeTaskId.make(event.workflowId),
            description: context.workflowTitle ?? "AgentTunnel workflow",
            ...(event.summary ? { summary: event.summary } : {}),
            status: "running",
            phases,
            taskType: "local_workflow",
            title: context.workflowTitle ?? "AgentTunnel workflow",
            workflowName: "AgentTunnel",
          },
        });
        return;
      }
      case "workflow.completed": {
        yield* emit({
          ...(yield* buildEventBase({ threadId, turnId, itemId: event.workflowId })),
          type: "task.completed",
          payload: {
            taskId: RuntimeTaskId.make(event.workflowId),
            status: event.status === "stopped" ? "stopped" : event.status,
            summary: event.summary,
            taskType: "local_workflow",
            title: context.workflowTitle ?? "AgentTunnel workflow",
            workflowName: "AgentTunnel",
          },
        });
        return;
      }
      case "orchestrator.output": {
        if (event.text.length === 0) return;
        const workflowId = context.workflowId ?? "agenttunnel";
        const delta = event.text.endsWith("\n") ? event.text : `${event.text}\n`;
        yield* emit({
          ...(yield* buildEventBase({
            threadId,
            turnId,
            itemId: `agenttunnel:${workflowId}:assistant`,
          })),
          type: "content.delta",
          payload: {
            streamKind: "assistant_text",
            delta,
          },
        });
        return;
      }
      case "agent.started": {
        context.agents.set(event.agentId, {
          role: event.role,
          task: event.task,
          pipelineId: event.pipelineId,
          agentIndex: event.agentIndex,
          phaseIndex: event.phaseIndex,
          harness: event.harness,
          ...(event.model ? { model: event.model } : {}),
          // A retried agent keeps its id; the attempt is what tells the client
          // this is the same row running again, not a second agent.
          ...(event.attempt ? { attempt: event.attempt } : {}),
        });
        yield* emit({
          ...(yield* buildEventBase({ threadId, turnId, itemId: event.agentId })),
          type: "task.started",
          payload: {
            taskId: RuntimeTaskId.make(event.agentId),
            description: event.task,
            taskType: "local_agent",
            title: event.task,
            role: event.role,
            parentAgentId: event.workflowId,
            workflowName: "AgentTunnel",
            harness: event.harness,
            agentIndex: event.agentIndex,
            phaseIndex: event.phaseIndex,
            phaseTitle: event.pipelineId,
            // The structured log file exists from the agent's first frame, so
            // the drill-in can stream it live instead of showing only the
            // progress tail until completion.
            outputFile: agentOutputPath(threadId, event.agentId),
            ...(event.model ? { model: event.model } : {}),
            ...(event.attempt ? { attempt: event.attempt } : {}),
            timelineBypass: true,
          },
        });
        return;
      }
      case "agent.progress": {
        const metadata = context.agents.get(event.agentId);
        if (!metadata) return;
        yield* emit({
          ...(yield* buildEventBase({ threadId, turnId, itemId: event.agentId })),
          type: "task.progress",
          payload: {
            taskId: RuntimeTaskId.make(event.agentId),
            description: metadata.task,
            summary: event.summary,
            taskType: "local_agent",
            title: metadata.task,
            role: metadata.role,
            parentAgentId: event.workflowId,
            workflowName: "AgentTunnel",
            ...(metadata.harness ? { harness: metadata.harness } : {}),
            agentIndex: metadata.agentIndex,
            phaseIndex: metadata.phaseIndex,
            phaseTitle: metadata.pipelineId,
            ...(metadata.model ? { model: metadata.model } : {}),
            ...(metadata.attempt ? { attempt: metadata.attempt } : {}),
            timelineBypass: true,
          },
        });
        return;
      }
      case "agent.completed": {
        const metadata = context.agents.get(event.agentId);
        if (!metadata) return;
        yield* emit({
          ...(yield* buildEventBase({ threadId, turnId, itemId: event.agentId })),
          type: "task.completed",
          payload: {
            taskId: RuntimeTaskId.make(event.agentId),
            status: event.status === "stopped" ? "stopped" : event.status,
            ...(event.summary ? { summary: event.summary } : {}),
            outputFile: agentOutputPath(threadId, event.agentId),
            taskType: "local_agent",
            title: metadata.task,
            role: metadata.role,
            parentAgentId: event.workflowId,
            workflowName: "AgentTunnel",
            ...(metadata.harness ? { harness: metadata.harness } : {}),
            agentIndex: metadata.agentIndex,
            phaseIndex: metadata.phaseIndex,
            phaseTitle: metadata.pipelineId,
            ...(metadata.model ? { model: metadata.model } : {}),
            ...(metadata.attempt ? { attempt: metadata.attempt } : {}),
            timelineBypass: true,
          },
        });
        return;
      }
    }
  });

  /**
   * Token frames from the harness stream. Every transport reports per-step
   * usage — OpenCode on `step_finish`, pi on `message_end`, Claude Code per
   * assistant message with the run's cost on `result`, Codex on `turn.completed`
   * — so whichever CLI a role runs gives the same numbers here: orchestrator
   * steps feed the thread usage snapshot, agent steps feed that agent's
   * cumulative typed-usage rollup (tokens and any harness-computed cost).
   * Harnesses that stream no comparable frames contribute nothing rather than a
   * guess.
   *
   * This fold is also the only place a step's usage reaches the agent log, so a
   * step a harness repeats across frames (Claude's per-content-block events)
   * counts once in the rollup and in the drill-in's per-turn table alike.
   */
  const trackHarnessUsage = Effect.fn("AgentTunnelAdapter.trackHarnessUsage")(function* (
    threadId: ThreadId,
    processId: string,
    line: string,
    harnessKind: AgentTunnelHarnessTransportKind,
  ) {
    const context = sessions.get(threadId);
    if (!context) return;
    const step = parseHarnessUsageStep(line, harnessKind);
    if (!step) return;
    const turnId = context.activeTurnId;
    const eventBase = (itemId?: string) =>
      buildEventBase({
        threadId,
        ...(turnId ? { turnId } : {}),
        ...(itemId ? { itemId } : {}),
      });

    if (processId.startsWith("orchestrator")) {
      const cumulative = addHarnessUsageTotals(context.orchestratorUsage, step);
      if (cumulative === undefined) return;
      if (step.totalTokens > 0) {
        yield* appendParsedEntries(threadId, processId, [harnessUsageLogEntry(step)]);
      }
      yield* emit({
        ...(yield* eventBase()),
        type: "thread.token-usage.updated",
        payload: {
          usage: threadUsageSnapshotFromStep(step, cumulative),
        },
      });
      return;
    }

    let cumulative = context.agentUsage.get(processId);
    if (cumulative === undefined) {
      cumulative = emptyHarnessUsageTotals();
      context.agentUsage.set(processId, cumulative);
    }
    if (addHarnessUsageTotals(cumulative, step) === undefined) return;
    if (step.totalTokens > 0) {
      yield* appendParsedEntries(threadId, processId, [harnessUsageLogEntry(step)]);
    }
    const metadata = context.agents.get(processId);
    yield* emit({
      ...(yield* eventBase(processId)),
      type: "task.progress",
      payload: {
        taskId: RuntimeTaskId.make(processId),
        description: metadata?.task ?? processId,
        typedUsage: runtimeTaskUsageFromTotals(cumulative),
        taskType: "local_agent",
        ...(metadata?.role ? { role: metadata.role } : {}),
        ...(metadata?.harness ? { harness: metadata.harness } : {}),
        ...(metadata?.attempt ? { attempt: metadata.attempt } : {}),
        timelineBypass: true,
      },
    });
  });

  const killThreadProcesses = Effect.fn("AgentTunnelAdapter.killThreadProcesses")(function* (
    threadId: ThreadId,
  ) {
    const processes = activeProcesses.get(threadId);
    if (!processes) return;
    yield* Effect.forEach([...processes.values()], (process) => process.pipe(Effect.ignore), {
      concurrency: "unbounded",
      discard: true,
    });
  });

  const processError = (cause: unknown) =>
    new AgentTunnelProcessError({
      detail: String(cause),
      cause,
    });

  const nativeEventLogger = options.nativeEventLogger;
  const writeNativeLine = (threadId: ThreadId, processId: string, line: string) =>
    nativeEventLogger
      ? Effect.gen(function* () {
          const observedAt = yield* nowIso;
          yield* nativeEventLogger.write(
            {
              observedAt,
              event: {
                provider: PROVIDER,
                providerInstanceId: options.instanceId,
                threadId,
                processId,
                line,
              },
            },
            threadId,
          );
        })
      : Effect.void;

  const processRunner: AgentTunnelProcessRunner = {
    spawn: (spec) =>
      Effect.gen(function* () {
        const args = buildAgentTunnelSpawnArgs(spec);
        yield* Effect.logInfo("AgentTunnel spawning harness", {
          scope: "agenttunnel.adapter",
          processId: spec.id,
          threadId: spec.threadId,
          harness: spec.harness,
          args: args.join(" "),
          cwd: spec.cwd,
        });
        const child = yield* spawner
          .spawn(
            ChildProcess.make(spec.harness, args, {
              cwd: spec.cwd,
              env: environment,
              extendEnv: true,
              detached: false,
              stdin: "ignore",
            }),
          )
          .pipe(Effect.mapError(processError));
        yield* Effect.logInfo("AgentTunnel harness started", {
          scope: "agenttunnel.adapter",
          processId: spec.id,
          pid: Number(child.pid),
          harness: spec.harness,
        });
        const processes = activeProcesses.get(ThreadId.make(spec.threadId)) ?? new Map();
        activeProcesses.set(ThreadId.make(spec.threadId), processes);
        const kill = child
          .kill({ killSignal: "SIGTERM", forceKillAfter: "5 seconds" })
          .pipe(Effect.mapError(processError), Effect.asVoid);
        processes.set(spec.id, kill);
        const exitCode = child.exitCode.pipe(
          Effect.mapError(processError),
          Effect.map(Number),
          Effect.tap(() =>
            Effect.sync(() => {
              processes.delete(spec.id);
              if (processes.size === 0) activeProcesses.delete(ThreadId.make(spec.threadId));
            }),
          ),
        );
        const threadId = ThreadId.make(spec.threadId);
        const harnessKind = agentTunnelHarnessKind(spec.harness);
        const observedLines = child.all.pipe(
          Stream.decodeText(),
          Stream.splitLines,
          Stream.tap((line) => writeNativeLine(threadId, spec.id, line).pipe(Effect.orDie)),
          Stream.tap((line) =>
            appendAgentLogEntries(threadId, spec.id, line, harnessKind).pipe(Effect.orDie),
          ),
        );
        const includeReasoning = spec.id.startsWith("orchestrator");
        const usageLines =
          harnessKind === "other"
            ? observedLines
            : observedLines.pipe(
                Stream.tap((line) =>
                  trackHarnessUsage(threadId, spec.id, line, harnessKind).pipe(Effect.orDie),
                ),
              );
        const lines =
          harnessKind === "other"
            ? usageLines.pipe(Stream.mapError(processError))
            : usageLines.pipe(
                Stream.flatMap((line) =>
                  Stream.fromIterable(
                    parseHarnessJsonLines(line, harnessKind, { includeReasoning }),
                  ),
                ),
                Stream.mapError(processError),
              );
        return {
          pid: Number(child.pid),
          lines,
          exitCode,
          kill,
        } satisfies AgentTunnelProcessHandle;
      }),
  };

  const ensureSession = Effect.fn("AgentTunnelAdapter.ensureSession")(function* (
    threadId: ThreadId,
  ) {
    const context = sessions.get(threadId);
    if (!context) {
      return yield* new ProviderAdapterSessionNotFoundError({
        provider: PROVIDER,
        threadId,
      });
    }
    if (context.stopped) {
      return yield* new ProviderAdapterSessionClosedError({
        provider: PROVIDER,
        threadId,
      });
    }
    return context;
  });

  const completeTurn = Effect.fn("AgentTunnelAdapter.completeTurn")(function* (
    context: AgentTunnelSessionContext,
    threadId: ThreadId,
    turnId: TurnId,
    status: "completed" | "failed" | "stopped",
    summary: string,
  ) {
    if (context.activeTurnId !== turnId) return;
    // Steering still in the queue was never handed to a pass; the turn is
    // ending, so report it as cancelled instead of leaving it queued.
    yield* cancelPendingSteering(context, threadId);
    context.activeTurnId = undefined;
    const { activeTurnId: _activeTurnId, lastError: _lastError, ...sessionBase } = context.session;
    context.session = {
      ...sessionBase,
      status: "ready",
      updatedAt: yield* nowIso,
      ...(status === "failed" ? { lastError: summary } : {}),
    };
    yield* emit({
      ...(yield* buildEventBase({ threadId, turnId })),
      type: "turn.completed",
      payload: {
        state: status === "stopped" ? "cancelled" : status,
        ...(status === "failed" ? { errorMessage: summary } : {}),
      },
    });
  });

  const startSession: ProviderAdapterShape<ProviderAdapterError>["startSession"] = Effect.fn(
    "AgentTunnelAdapter.startSession",
  )(function* (input) {
    const existing = sessions.get(input.threadId);
    if (existing && !existing.stopped) return existing.session;

    const now = yield* nowIso;
    const model =
      input.modelSelection?.instanceId === options.instanceId
        ? input.modelSelection.model
        : undefined;
    const session: ProviderSession = {
      provider: PROVIDER,
      providerInstanceId: options.instanceId,
      status: "ready",
      runtimeMode: input.runtimeMode,
      ...(input.cwd ? { cwd: input.cwd } : {}),
      ...(model ? { model } : {}),
      threadId: input.threadId,
      resumeCursor: input.resumeCursor ?? { schemaVersion: RESUME_VERSION },
      createdAt: now,
      updatedAt: now,
    };
    const context: AgentTunnelSessionContext = {
      session,
      cwd: input.cwd ?? serverConfig.cwd,
      approvalPolicy: input.approvalPolicy,
      sandboxMode: input.sandboxMode,
      cancelled: yield* Ref.make(false),
      turns: [],
      agents: new Map(),
      agentUsage: new Map(),
      orchestratorUsage: emptyHarnessUsageTotals(),
      agentLogBytes: new Map(),
      agentRawLogBytes: new Map(),
      workflowTitle: undefined,
      workflowId: undefined,
      activeTurnId: undefined,
      stopped: false,
      threadHistory: [],
      lastSharedContextDump: undefined,
      steering: yield* Queue.unbounded<AgentTunnelSteeringEntry>(),
      agentControl: yield* Queue.unbounded<AgentTunnelAgentCommand>(),
    };
    sessions.set(input.threadId, context);
    yield* emit({
      ...(yield* buildEventBase({ threadId: input.threadId })),
      type: "session.started",
      payload: { message: "AgentTunnel session ready" },
    });
    yield* emit({
      ...(yield* buildEventBase({ threadId: input.threadId })),
      type: "thread.started",
      payload: {},
    });
    return session;
  });

  const sendTurn: ProviderAdapterShape<ProviderAdapterError>["sendTurn"] = Effect.fn(
    "AgentTunnelAdapter.sendTurn",
  )(function* (input) {
    const context = yield* ensureSession(input.threadId);
    if (context.activeTurnId) {
      // Steering: a message sent mid-run is queued for the orchestrator, which
      // takes a pass as soon as one is queued and no other pass is in flight.
      const steering = (input.input ?? "").trim();
      if (steering.length > 0) {
        yield* Queue.offer(context.steering, {
          ...(input.messageId ? { messageId: input.messageId } : {}),
          text: steering,
        });
        if (input.messageId) {
          yield* emitSteeringUpdate({
            threadId: input.threadId,
            turnId: context.activeTurnId,
            messageId: input.messageId,
            status: "queued",
          });
        }
        yield* emit({
          ...(yield* buildEventBase({
            threadId: input.threadId,
            turnId: context.activeTurnId,
          })),
          type: "content.delta",
          payload: {
            streamKind: "assistant_text",
            delta: `\n> \ud83d\udce5 **Steering queued** \u2014 the orchestrator weighs this at its next pass, without waiting for the running subagents: ${steering}\n`,
          },
        });
      }
      return { threadId: input.threadId, turnId: context.activeTurnId };
    }

    const turnId = TurnId.make(yield* randomUuid);
    const workflowId = `agenttunnel_${input.threadId}_${turnId}`;
    const model =
      input.modelSelection?.instanceId === options.instanceId
        ? input.modelSelection.model
        : context.session.model;
    const effectiveSettings: AgentTunnelSettings = {
      ...settings,
      dangerouslySkipPermissions:
        settings.dangerouslySkipPermissions ||
        context.approvalPolicy === "never" ||
        context.sandboxMode === "danger-full-access",
    };
    context.activeTurnId = turnId;
    context.workflowTitle = input.input ?? "AgentTunnel workflow";
    context.workflowId = workflowId;
    context.agents.clear();
    context.agentUsage.clear();
    context.agentLogBytes.clear();
    context.agentRawLogBytes.clear();
    // Steering is per-turn: a prior turn's queue must never leak into this one.
    yield* cancelPendingSteering(context, input.threadId);
    // Same for agent controls: a retry aimed at a finished turn must not re-run
    // anything in this one.
    yield* drainQueuedAgentCommands(context);
    yield* clearAgentOutput(input.threadId);
    context.session = {
      ...context.session,
      status: "running",
      ...(model ? { model } : {}),
      activeTurnId: turnId,
      updatedAt: yield* nowIso,
    };
    context.turns.push({ id: turnId, items: [] });
    yield* Ref.set(context.cancelled, false);
    yield* emit({
      ...(yield* buildEventBase({ threadId: input.threadId, turnId })),
      type: "turn.started",
      payload: model ? { model } : {},
    });

    const run = Effect.gen(function* () {
      const inheritedContextPrompt = yield* inheritedContext.renderForPrompt();
      const result = yield* Effect.exit(
        runAgentTunnel({
          threadId: input.threadId,
          workflowId,
          userTask: input.input ?? "",
          cwd: context.cwd,
          ...(model ? { model } : {}),
          availableModels: options.availableModels ?? [],
          settings: effectiveSettings,
          process: processRunner,
          emit: (event) => emitEvent(input.threadId, turnId, event).pipe(Effect.orDie),
          isCancelled: Ref.get(context.cancelled),
          ...(context.threadHistory.length > 0
            ? { threadHistory: [...context.threadHistory] }
            : {}),
          ...(context.lastSharedContextDump
            ? { priorSharedContext: context.lastSharedContextDump }
            : {}),
          ...(inheritedContextPrompt.trim().length > 0
            ? { inheritedContext: inheritedContextPrompt }
            : {}),
          steering: {
            // Non-blocking: the scheduler asks what is queued before deciding
            // which pass to run.
            pending: () =>
              Effect.gen(function* () {
                const queued = yield* drainQueuedSteering(context);
                return yield* resolveSteering(context, input.threadId, queued, "injected");
              }),
            // Blocks until a message lands, so a steer sent while subagents are
            // working starts an orchestrator pass instead of waiting for them.
            takeWhenAvailable: () =>
              Effect.gen(function* () {
                const first = yield* Queue.take(context.steering);
                const rest = yield* drainQueuedSteering(context);
                return yield* resolveSteering(
                  context,
                  input.threadId,
                  [first, ...rest],
                  "injected",
                );
              }),
          },
          agentControl: {
            // Non-blocking: the settle handler asks whether a retry is queued.
            pending: () => drainQueuedAgentCommands(context),
            // Blocks until a command lands, so a retry sent while the turn waits
            // for one resumes the workflow instead of the wait timing out.
            takeWhenAvailable: () =>
              Effect.gen(function* () {
                const first = yield* Queue.take(context.agentControl);
                const rest = yield* drainQueuedAgentCommands(context);
                return [first, ...rest];
              }),
          },
        }),
      );
      if (result._tag === "Failure") {
        const detail = Cause.pretty(result.cause);
        yield* emit({
          ...(yield* buildEventBase({ threadId: input.threadId, turnId })),
          type: "runtime.error",
          payload: { message: detail, class: "provider_error" },
        });
        context.threadHistory.push({
          task: input.input ?? "",
          summary: `failed: ${detail.slice(0, 200)}`,
        });
        if (context.threadHistory.length > 10) context.threadHistory.shift();
        yield* completeTurn(context, input.threadId, turnId, "failed", detail);
        return;
      }
      const workflowResult = result.value;
      context.threadHistory.push({
        task: input.input ?? "",
        summary: workflowResult.summary.slice(0, 300),
      });
      if (context.threadHistory.length > 10) context.threadHistory.shift();
      if (workflowResult.contextDump) {
        context.lastSharedContextDump = workflowResult.contextDump;
      }
      yield* inheritedContext.record({
        learnings: workflowResult.learnings ?? [],
        attribution: {
          threadId: input.threadId,
          workflowId,
          projectCwd: context.cwd,
          status: workflowResult.status,
        },
      });
      yield* completeTurn(
        context,
        input.threadId,
        turnId,
        workflowResult.status,
        workflowResult.summary,
      );
    }).pipe(Effect.provideService(Scope.Scope, runtimeScope));
    yield* Effect.forkIn(run, runtimeScope);

    return { threadId: input.threadId, turnId };
  });

  const interruptTurn: ProviderAdapterShape<ProviderAdapterError>["interruptTurn"] = Effect.fn(
    "AgentTunnelAdapter.interruptTurn",
  )(function* (threadId) {
    const context = yield* ensureSession(threadId);
    yield* Ref.set(context.cancelled, true);
    yield* killThreadProcesses(threadId);
  });

  /**
   * Per-agent control only means something while a workflow run is in flight:
   * the task table that can be stopped or re-run lives inside the run, so a
   * command with no run to receive it would be dropped silently. The panel is
   * allowed to be a turn behind, so this is the only rejection an unknown agent
   * id gets — an id the run does not know is a no-op inside the orchestrator.
   */
  const requireActiveRun = (
    context: AgentTunnelSessionContext,
    threadId: ThreadId,
    agentId: string,
    operation: "interruptAgent" | "retryAgent",
  ) =>
    context.activeTurnId === undefined
      ? Effect.fail(
          new ProviderAdapterRequestError({
            provider: PROVIDER,
            method: operation,
            detail: `No AgentTunnel workflow is running on thread '${threadId}', so agent '${agentId}' cannot be ${operation === "retryAgent" ? "retried" : "stopped"}. Send a message to start one.`,
          }),
        )
      : Effect.void;

  /**
   * Interrupt one sub-agent: kill only its process and tell the orchestrator, so
   * the workflow records that agent stopped and keeps going. The turn-level
   * cancel flag stays false — the rest of the round is not being stopped.
   */
  const interruptAgent: ProviderAdapterShape<ProviderAdapterError>["interruptAgent"] = Effect.fn(
    "AgentTunnelAdapter.interruptAgent",
  )(function* (threadId, agentId) {
    const context = yield* ensureSession(threadId);
    yield* requireActiveRun(context, threadId, agentId, "interruptAgent");
    // Queued before the kill: even if the process exits from the signal, the
    // orchestrator has already been told this agent was stopped on purpose.
    yield* Queue.offer(context.agentControl, { kind: "interrupt", agentId });
    const kill = activeProcesses.get(threadId)?.get(agentId);
    // An unknown agent id is not an error — the panel may be a turn behind.
    if (!kill) return;
    yield* kill.pipe(Effect.ignore);
  });

  /**
   * Re-run one sub-agent's task in place. The orchestrator owns the re-run: the
   * command is queued and applied once the round settles or while it waits for
   * a retry.
   */
  const retryAgent: ProviderAdapterShape<ProviderAdapterError>["retryAgent"] = Effect.fn(
    "AgentTunnelAdapter.retryAgent",
  )(function* (threadId, agentId) {
    const context = yield* ensureSession(threadId);
    yield* requireActiveRun(context, threadId, agentId, "retryAgent");
    yield* Queue.offer(context.agentControl, { kind: "retry", agentId });
  });

  const stopSession: ProviderAdapterShape<ProviderAdapterError>["stopSession"] = Effect.fn(
    "AgentTunnelAdapter.stopSession",
  )(function* (threadId) {
    const context = yield* ensureSession(threadId);
    context.stopped = true;
    yield* Ref.set(context.cancelled, true);
    yield* killThreadProcesses(threadId);
    sessions.delete(threadId);
    yield* emit({
      ...(yield* buildEventBase({ threadId })),
      type: "session.exited",
      payload: { reason: "AgentTunnel session stopped.", recoverable: false, exitKind: "graceful" },
    });
  });

  const unsupportedRequest = (operation: string) =>
    Effect.fail(
      new ProviderAdapterRequestError({
        provider: PROVIDER,
        method: operation,
        detail:
          "AgentTunnel runs non-interactive child sessions; interactive requests are not supported yet.",
      }),
    );

  const respondToRequest: ProviderAdapterShape<ProviderAdapterError>["respondToRequest"] = () =>
    unsupportedRequest("respondToRequest");
  const respondToUserInput: ProviderAdapterShape<ProviderAdapterError>["respondToUserInput"] = () =>
    unsupportedRequest("respondToUserInput");

  const listSessions: ProviderAdapterShape<ProviderAdapterError>["listSessions"] = () =>
    Effect.succeed([...sessions.values()].map((context) => context.session));
  const hasSession: ProviderAdapterShape<ProviderAdapterError>["hasSession"] = (threadId) =>
    Effect.succeed(sessions.has(threadId));

  const readThread: ProviderAdapterShape<ProviderAdapterError>["readThread"] = Effect.fn(
    "AgentTunnelAdapter.readThread",
  )(function* (threadId) {
    const context = yield* ensureSession(threadId);
    return { threadId, turns: context.turns };
  });

  const rollbackThread: ProviderAdapterShape<ProviderAdapterError>["rollbackThread"] = Effect.fn(
    "AgentTunnelAdapter.rollbackThread",
  )(function* (threadId, numTurns) {
    const context = yield* ensureSession(threadId);
    if (numTurns > 0) context.turns.splice(Math.max(0, context.turns.length - numTurns));
    return { threadId, turns: context.turns };
  });

  const stopAll: ProviderAdapterShape<ProviderAdapterError>["stopAll"] = () =>
    Effect.forEach([...sessions.keys()], (threadId) => stopSession(threadId).pipe(Effect.ignore), {
      concurrency: "unbounded",
      discard: true,
    });

  yield* Effect.addFinalizer(() =>
    Effect.gen(function* () {
      yield* Effect.forEach([...sessions.keys()], (threadId) => killThreadProcesses(threadId), {
        concurrency: "unbounded",
        discard: true,
      });
      yield* Scope.close(runtimeScope, Exit.void).pipe(Effect.ignore);
      yield* Queue.shutdown(runtimeEvents).pipe(Effect.ignore);
    }),
  );

  return {
    provider: PROVIDER,
    capabilities: { sessionModelSwitch: "in-session" },
    startSession,
    sendTurn,
    interruptTurn,
    interruptAgent,
    retryAgent,
    respondToRequest,
    respondToUserInput,
    stopSession,
    listSessions,
    hasSession,
    readThread,
    rollbackThread,
    stopAll,
    get streamEvents() {
      return Stream.fromQueue(runtimeEvents);
    },
  } satisfies ProviderAdapterShape<ProviderAdapterError>;
});

export { PROVIDER as AGENT_TUNNEL_PROVIDER };
