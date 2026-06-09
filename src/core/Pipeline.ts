import { spawn } from 'node:child_process';
import { Session } from './Session.js';
import {
  ORCHESTRATOR_PROMPT,
  EXECUTOR_PROMPT,
  REVIEWER_PROMPT,
  ORCHESTRATOR_TOOLS,
  EXECUTOR_TOOLS,
} from './prompts.js';
import { stripAnsi } from './utils/AnsiCleaner.js';
import { parseToolCallLine } from './ToolRegistry.js';
import { SharedContext, type ContextEntry } from './SharedContext.js';
import type {
  AppConfig,
  PipelineInfo,
  RoleName,
  SubAgentInfo,
  TaskExecutionMode,
} from '../types/index.js';
import { useStore } from './state/store.js';
import type { ToolCall } from './tools.js';

export interface PipelineOptions {
  cwd: string;
  config: AppConfig;
  onPhaseChange?: (phase: { type: string; label: string; progress?: { current: number; total: number } }) => void;
  onSubAgentUpdate?: (agents: SubAgentInfo[]) => void;
  onPipelineUpdate?: (pipelines: PipelineInfo[]) => void;
  onDone?: (ok: boolean, msg: string) => void;
  onContextUpdate?: (entries: ContextEntry[]) => void;
  harness?: string;
  dangerouslySkipPermissions?: boolean;
  newAgentId: () => string;
  onNarrative?: (text: string) => void;
  onToolCall?: (call: ToolCall) => void;
}

export type PipelineEvent =
  | { type: 'output'; text: string; kind: 'assistant' | 'system' | 'tool_output' | 'error' }
  | { type: 'phaseChange'; phase: { type: string; label: string; progress?: { current: number; total: number } } }
  | { type: 'subAgentUpdate'; agents: SubAgentInfo[] }
  | { type: 'subAgentOutput'; agentId: string; text: string }
  | { type: 'pipelineUpdate'; pipelines: PipelineInfo[] }
  | { type: 'done'; ok: boolean; msg: string }
  | { type: 'toolCall'; call: ToolCall }
  | { type: 'contextUpdate'; entries: ContextEntry[] };

type Listener = (ev: PipelineEvent) => void;

export interface DispatchedTask {
  role: RoleName;
  task: string;
  context: string[];
  /** Id of the named pipeline this task belongs to. */
  pipelineId: string;
  /** Mode the task's pipeline was declared with. */
  mode: TaskExecutionMode;
  /** Pipeline ids that must complete before this task's pipeline starts. */
  dependsOn: string[];
}

const MAX_AGENT_OUTPUT_LINES = 200;

let nextPipelineIdx = 1;
function freshPipelineId(): string {
  return `pipe-${nextPipelineIdx++}-${Date.now().toString(36)}`;
}

export class Pipeline {
  private orchestrator: Session | null = null;
  private currentSubAgent: Session | null = null;
  private readonly subAgentSessions = new Map<string, Session>();
  private agents: SubAgentInfo[] = [];
  /** Tasks queued in the current orchestrator pass. */
  private pendingTasks: DispatchedTask[] = [];
  /** Pipelines built from the current pass (and earlier passes in the run). */
  private pipelines: PipelineInfo[] = [];
  private running = false;
  private cancelled = false;
  private listeners = new Set<Listener>();
  private orchestratorPasses = 0;
  private maxOrchestratorPasses = 4;

  readonly context = new SharedContext();

  constructor(private readonly options: PipelineOptions) {}

  isRunning(): boolean {
    return this.running;
  }

  on(listener: Listener): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  private emit(ev: PipelineEvent): void {
    for (const l of this.listeners) {
      try {
        l(ev);
      } catch {
        // ignore
      }
    }
  }

  private emitContext(): void {
    this.emit({ type: 'contextUpdate', entries: this.context.list() });
  }

  private emitPipelines(): void {
    this.emit({ type: 'pipelineUpdate', pipelines: this.pipelines.map((p) => ({ ...p })) });
  }

  async run(userTask: string): Promise<void> {
    if (this.running) {
      this.emit({ type: 'output', text: '[pipeline] already running, ignoring new task', kind: 'system' });
      return;
    }
    this.running = true;
    this.cancelled = false;
    this.agents = [];
    this.pendingTasks = [];
    this.pipelines = [];

    this.emit({ type: 'phaseChange', phase: { type: 'orchestrating', label: 'orchestrator planning' } });
    this.emit({ type: 'output', text: '[pipeline] dispatching orchestrator', kind: 'system' });
    this.emit({ type: 'output', text: `Available tools: ${ORCHESTRATOR_TOOLS.map((t) => t.name).join(', ')}`, kind: 'system' });
    this.emitContext();
    this.emitPipelines();

    try {
      while (this.orchestratorPasses < this.maxOrchestratorPasses) {
        const pipelinesBefore = this.pipelines.length;
        const exits = await this.runOrchestratorPass(userTask, this.orchestratorPasses);
        if (this.cancelled) {
          this.finish(false, 'cancelled');
          return;
        }
        if (!exits.ok) {
          this.finish(false, exits.msg);
          return;
        }
        if (this.pipelines.length === pipelinesBefore) {
          // Orchestrator did not dispatch anything new — finish.
          this.finish(true, this.context.list().length > 0 ? 'orchestrator finished' : 'orchestrator: no tasks');
          return;
        }
        // Run the new pipelines concurrently.
        const ok = await this.runNewPipelines(pipelinesBefore);
        if (this.cancelled) {
          this.finish(false, 'cancelled');
          return;
        }
        if (!ok) {
          this.finish(false, 'one or more sub-agents failed');
          return;
        }
        this.orchestratorPasses += 1;
      }
      this.finish(true, 'reached max orchestrator passes');
    } catch (err) {
      this.emit({ type: 'output', text: `[pipeline] fatal: ${(err as Error).message}`, kind: 'error' });
      this.finish(false, (err as Error).message);
    }
  }

  private runOrchestratorPass(userTask: string, pass: number): Promise<{ ok: boolean; msg: string }> {
    return new Promise((resolve) => {
      const session = new Session({
        cwd: this.options.cwd,
        harness: this.options.harness ?? 'opencode',
        model: this.options.config.orchestrator.model || undefined,
        dangerouslySkipPermissions: this.options.dangerouslySkipPermissions ?? true,
      });
      this.orchestrator = session;

      const isFirst = pass === 0;
      const prompt = isFirst
        ? `${ORCHESTRATOR_PROMPT}\n\nUser task:\n${userTask}`
        : this.buildSummaryPassPrompt();

      if (!isFirst) {
        this.emit({
          type: 'output',
          text: `[pipeline] orchestrator pass #${pass + 1} (reviewing ${this.pipelines.reduce((n, p) => n + p.taskIds.length, 0)} task(s))`,
          kind: 'system',
        });
      }

      session.onOutput((line) => this.handleAgentLine(stripAnsi(line), 'orchestrator', undefined));
      session.onExit(({ code, signal }) => {
        this.orchestrator = null;
        if (this.cancelled) {
          resolve({ ok: false, msg: 'cancelled' });
          return;
        }
        if (code !== 0) {
          this.emit({
            type: 'output',
            text: `[orchestrator] exited code=${code} signal=${signal ?? ''}`,
            kind: 'error',
          });
          resolve({ ok: false, msg: `orchestrator exited with code ${code}` });
          return;
        }
        resolve({ ok: true, msg: 'orchestrator ok' });
      });

      session.run({ prompt }).catch((err) => {
        this.emit({
          type: 'output',
          text: `[orchestrator] failed to start: ${(err as Error).message}`,
          kind: 'error',
        });
        resolve({ ok: false, msg: (err as Error).message });
      });
    });
  }

  private buildSummaryPassPrompt(): string {
    const summary = this.context.renderSummary();
    const recent = this.pipelines
      .slice(-6)
      .map((p) => `- [${p.mode}] ${p.name} (${p.taskIds.length} task(s), ${p.status})`)
      .join('\n');
    return [
      `${ORCHESTRATOR_PROMPT}`,
      ``,
      `## Summary pass`,
      ``,
      `This is not the first pass. Below is what the team has done.`,
      `Use it to decide whether to dispatch follow-up tasks (e.g. a reviewer)`,
      `or to call \`done\`.`,
      ``,
      `### Recent pipelines`,
      recent,
      ``,
      `### Shared context`,
      '```',
      summary,
      '```',
    ].join('\n');
  }

  private handleAgentLine(clean: string, role: 'orchestrator' | 'sub-agent', agentId: string | undefined): void {
    const call = parseToolCallLine(clean);
    if (call) {
      this.emit({ type: 'toolCall', call });
      if (role === 'orchestrator') {
        this.handleOrchestratorToolCall(call);
      } else if (agentId) {
        this.handleSubAgentToolCall(call, agentId);
      }
      return;
    }
    if (clean.trim().length === 0) return;
    if (role === 'orchestrator') {
      this.emit({ type: 'output', text: clean, kind: 'assistant' });
    } else if (agentId) {
      this.emit({ type: 'subAgentOutput', agentId, text: clean });
    }
  }

  /** Test-only: drive the orchestrator's tool-call handler without spawning a process. */
  _testHandleOrchestratorCall(call: ToolCall): void {
    this.handleOrchestratorToolCall(call);
  }

  /** Test-only: drive the sub-agent's tool-call handler without spawning a process. */
  _testHandleSubAgentCall(call: ToolCall, agentId: string): void {
    this.handleSubAgentToolCall(call, agentId);
  }

  /** Test-only: current pipelines snapshot. */
  _testPipelines(): PipelineInfo[] {
    return this.pipelines.map((p) => ({ ...p }));
  }

  private handleOrchestratorToolCall(call: ToolCall): void {
    if (call.name === 'dispatch_task') {
      this.acceptDispatch(call);
    } else if (call.name === 'update_context') {
      const key = String(call.arguments.key ?? '');
      const value = String(call.arguments.value ?? '');
      try {
        this.context.set(key, value, 'orchestrator');
        this.emit({
          type: 'output',
          text: `[orchestrator] context: ${key} = ${truncate(value.replace(/\n/g, ' '), 80)}`,
          kind: 'system',
        });
        this.emitContext();
      } catch (err) {
        this.emit({
          type: 'output',
          text: `[orchestrator] update_context failed: ${(err as Error).message}`,
          kind: 'error',
        });
      }
    } else if (call.name === 'done') {
      this.emit({
        type: 'output',
        text: `[orchestrator] done (${String(call.arguments.reason ?? 'complete')})`,
        kind: 'system',
      });
    } else {
      this.emit({
        type: 'output',
        text: `[orchestrator] unknown tool: ${call.name}`,
        kind: 'error',
      });
    }
  }

  private acceptDispatch(call: ToolCall): void {
    const role = String(call.arguments.role ?? '');
    const task = String(call.arguments.task ?? '');
    const pipelineId = String(call.arguments.pipeline ?? '') || freshPipelineId();
    const mode = (call.arguments.mode === 'sequential' ? 'sequential' : 'parallel') as TaskExecutionMode;
    const dependsOnRaw = call.arguments.depends_on;
    const dependsOn = Array.isArray(dependsOnRaw)
      ? dependsOnRaw.filter((d): d is string => typeof d === 'string')
      : [];
    const contextRaw = call.arguments.context;
    const context = Array.isArray(contextRaw)
      ? contextRaw.filter((k): k is string => typeof k === 'string')
      : [];

    if ((role !== 'executor' && role !== 'reviewer') || task.length === 0) {
      this.emit({
        type: 'output',
        text: `[orchestrator] invalid dispatch_task arguments: ${JSON.stringify(call.arguments)}`,
        kind: 'error',
      });
      return;
    }

    // Find or create the pipeline.
    let pipeline = this.pipelines.find((p) => p.id === pipelineId);
    if (!pipeline) {
      pipeline = {
        id: pipelineId,
        name: pipelineId,
        mode,
        dependsOn,
        taskIds: [],
        status: 'idle',
      };
      this.pipelines.push(pipeline);
    } else {
      // First-declared mode/dependsOn wins. Subsequent dispatches into the
      // same pipeline id only contribute their task.
      if (pipeline.mode !== mode || JSON.stringify(pipeline.dependsOn) !== JSON.stringify(dependsOn)) {
        this.emit({
          type: 'output',
          text: `[orchestrator] pipeline "${pipelineId}" already exists; ignoring redeclaration of mode/depends_on`,
          kind: 'system',
        });
      }
    }

    const taskEntry: DispatchedTask = {
      role: role as RoleName,
      task,
      context,
      pipelineId,
      mode,
      dependsOn,
    };
    this.pendingTasks.push(taskEntry);
    pipeline.taskIds.push(`t-${this.pendingTasks.length - 1}`);
    this.emitPipelines();
    this.emit({
      type: 'output',
      text: `[orchestrator] → ${role}@${pipelineId}${mode === 'sequential' ? ' (seq)' : ''}${dependsOn.length > 0 ? ` after [${dependsOn.join(', ')}]` : ''}: ${truncate(task, 60)}`,
      kind: 'system',
    });
  }

  private handleSubAgentToolCall(call: ToolCall, agentId: string): void {
    if (call.name === 'note_finding') {
      const key = String(call.arguments.key ?? '');
      const value = String(call.arguments.value ?? '');
      try {
        this.context.append(key, value, agentId);
        this.emit({
          type: 'output',
          text: `[${agentId}] finding → ${key}: ${truncate(value.replace(/\n/g, ' '), 80)}`,
          kind: 'system',
        });
        this.emitContext();
      } catch (err) {
        this.emit({
          type: 'output',
          text: `[${agentId}] note_finding failed: ${(err as Error).message}`,
          kind: 'error',
        });
      }
    } else if (call.name === 'done') {
      this.emit({ type: 'output', text: `[${agentId}] done`, kind: 'system' });
    }
  }

  /**
   * Run the pipelines added since `startIdx`, respecting dependencies and
   * parallel/sequential mode. Returns true iff all pipelines ended in
   * `completed` status.
   */
  private async runNewPipelines(startIdx: number): Promise<boolean> {
    const targets = this.pipelines.slice(startIdx);
    if (targets.length === 0) return true;

    // Index for fast lookup
    const byId = new Map<string, PipelineInfo>();
    for (const p of this.pipelines) byId.set(p.id, p);

    // State: which pipelines are done, which are running.
    const completed = new Set<string>();
    const running = new Set<string>();
    let allOk = true;

    // Build a closure that runs a single pipeline and resolves when done.
    const runPipeline = (p: PipelineInfo): Promise<void> =>
      new Promise<void>((resolve) => {
        p.status = 'running';
        p.startedAt = Date.now();
        this.emitPipelines();
        this.runPipelineInternal(p).then(
          (ok) => {
            p.status = ok ? 'completed' : 'failed';
            p.endedAt = Date.now();
            completed.add(p.id);
            running.delete(p.id);
            this.emitPipelines();
            resolve();
          },
          (err) => {
            this.emit({ type: 'output', text: `[pipeline ${p.id}] error: ${(err as Error).message}`, kind: 'error' });
            p.status = 'failed';
            p.endedAt = Date.now();
            completed.add(p.id);
            running.delete(p.id);
            this.emitPipelines();
            resolve();
          },
        );
      });

    // Loop: at each step, find pipelines that are ready (deps satisfied, not
    // yet started, not running), launch them all, then wait for any to
    // finish before re-checking.
    const totalCount = targets.length;
    let doneCount = 0;

    while (doneCount < totalCount) {
      if (this.cancelled) {
        allOk = false;
        break;
      }
      const ready: PipelineInfo[] = [];
      for (const p of targets) {
        if (running.has(p.id) || completed.has(p.id)) continue;
        const depsMet = p.dependsOn.every((dep) => completed.has(dep));
        if (depsMet) ready.push(p);
      }
      if (ready.length === 0 && running.size === 0) {
        // No progress possible — remaining pipelines have unsatisfiable deps.
        this.emit({
          type: 'output',
          text: `[pipeline] deadlock: some pipelines have unsatisfiable dependencies`,
          kind: 'error',
        });
        for (const p of targets) {
          if (!completed.has(p.id)) {
            p.status = 'failed';
            p.endedAt = Date.now();
          }
        }
        this.emitPipelines();
        allOk = false;
        break;
      }
      // Launch all ready pipelines concurrently.
      for (const p of ready) {
        running.add(p.id);
        void runPipeline(p);
      }
      // Update progress label.
      this.emit({
        type: 'phaseChange',
        phase: {
          type: 'executing',
          label: `pipelines (${doneCount}/${totalCount})`,
          progress: { current: doneCount, total: totalCount },
        },
      });
      // Wait for one to finish (or all to finish).
      // We use Promise.race by building a promise per running pipeline.
      if (running.size > 0) {
        await new Promise<void>((resolve) => {
          const check = setInterval(() => {
            if (cancelledOrDone(this.cancelled, running.size)) {
              clearInterval(check);
              resolve();
            }
          }, 50);
        });
        // Recount done
        doneCount = targets.filter((p) => completed.has(p.id)).length;
      } else {
        break;
      }
    }

    if (allOk) {
      for (const p of targets) {
        if (p.status === 'failed') {
          allOk = false;
          break;
        }
      }
    }
    return allOk;
  }

  /** Run all tasks within a single named pipeline, parallel or sequential. */
  private async runPipelineInternal(p: PipelineInfo): Promise<boolean> {
    // Find this pipeline's tasks (in dispatch order).
    const tasks = this.pendingTasks.filter((t) => t.pipelineId === p.id);

    this.emit({
      type: 'output',
      text: `[pipeline ${p.id}] starting (${p.mode}, ${tasks.length} task(s))`,
      kind: 'system',
    });

    if (p.mode === 'sequential') {
      let ok = true;
      for (const t of tasks) {
        if (this.cancelled) return false;
        const taskOk = await this.runOne(t);
        if (!taskOk) ok = false;
      }
      return ok;
    }
    // Parallel — run all at once.
    const results = await Promise.all(tasks.map((t) => this.runOne(t)));
    return results.every(Boolean);
  }

  private async runOne(task: DispatchedTask): Promise<boolean> {
    const agent: SubAgentInfo = {
      id: this.options.newAgentId(),
      role: task.role,
      status: 'running',
      task: task.task,
      output: [],
      startedAt: Date.now(),
      pipelineId: task.pipelineId,
    };
    this.agents = [...this.agents, agent];
    this.emit({ type: 'subAgentUpdate', agents: this.agents });

    const session = new Session({
      cwd: this.options.cwd,
      harness: this.options.harness ?? 'opencode',
      model:
        task.role === 'executor'
          ? this.options.config.executor.model || undefined
          : this.options.config.reviewer.model || undefined,
      dangerouslySkipPermissions: this.options.dangerouslySkipPermissions ?? true,
    });
    this.currentSubAgent = session;
    this.subAgentSessions.set(agent.id, session);

    const sysPrompt = task.role === 'executor' ? EXECUTOR_PROMPT : REVIEWER_PROMPT;
    const contextBlock = task.context.length > 0
      ? `\n\n## Shared context from orchestrator\n\n${this.context.renderForPrompt(task.context)}\n`
      : '';
    const toolsBlock = task.role === 'executor'
      ? `\n\n## Your tools\n\n${EXECUTOR_TOOLS.map((t) => `- ${t.name}: ${t.description}`).join('\n')}\n`
      : '';
    const prompt =
      `${sysPrompt}${toolsBlock}\n\n` +
      `## Your task\n${task.task}${contextBlock}\n\n` +
      `When you are finished, call the \`done\` tool. ` +
      `Call \`note_finding\` for any discovery the next agent or the orchestrator should know.`;

    return new Promise<boolean>((resolve) => {
      session.onOutput((line) => {
        const clean = stripAnsi(line);
        if (agent.output.length < MAX_AGENT_OUTPUT_LINES) {
          agent.output = [...agent.output, clean];
        } else {
          agent.output = [...agent.output.slice(agent.output.length - (MAX_AGENT_OUTPUT_LINES - 1)), clean];
        }
        this.agents = [...this.agents];
        this.emit({ type: 'subAgentUpdate', agents: this.agents });
        this.handleAgentLine(clean, 'sub-agent', agent.id);
      });
      session.onExit(({ code, signal }) => {
        agent.endedAt = Date.now();
        agent.status = code === 0 ? 'completed' : 'failed';
        this.agents = [...this.agents];
        this.emit({ type: 'subAgentUpdate', agents: this.agents });
        if (code !== 0 && !this.cancelled) {
          this.emit({
            type: 'output',
            text: `[${task.role}] exit code=${code} signal=${signal ?? ''}`,
            kind: 'error',
          });
        }
        this.subAgentSessions.delete(agent.id);
        if (this.currentSubAgent === session) this.currentSubAgent = null;
        resolve(code === 0);
      });

      session.run({ prompt }).catch((err) => {
        agent.endedAt = Date.now();
        agent.status = 'failed';
        this.agents = [...this.agents];
        this.emit({ type: 'subAgentUpdate', agents: this.agents });
        this.emit({ type: 'output', text: `[${task.role}] failed: ${(err as Error).message}`, kind: 'error' });
        this.subAgentSessions.delete(agent.id);
        if (this.currentSubAgent === session) this.currentSubAgent = null;
        resolve(false);
      });
    });
  }

  cancel(): void {
    this.cancelled = true;
    this.emit({ type: 'output', text: '[pipeline] cancel requested', kind: 'system' });
    this.orchestrator?.cancel();
    for (const s of this.subAgentSessions.values()) s.cancel();
    this.currentSubAgent = null;
  }

  /**
   * Kill a single running sub-agent by id. Sends SIGTERM (then SIGKILL after 5s).
   * The pipeline continues running any other in-flight sub-agents.
   */
  killSubAgent(agentId: string): boolean {
    const session = this.subAgentSessions.get(agentId);
    if (!session) return false;
    const agent = this.agents.find((a) => a.id === agentId);
    if (!agent || agent.status !== 'running') return false;
    this.emit({
      type: 'output',
      text: `[ui] killing sub-agent ${agentId} (${agent.role})`,
      kind: 'system',
    });
    session.cancel();
    return true;
  }

  private finish(ok: boolean, msg: string): void {
    this.running = false;
    this.emit({
      type: 'phaseChange',
      phase: {
        type: ok ? 'completed' : 'failed',
        label: ok ? 'completed' : msg,
      },
    });
    this.emit({ type: 'done', ok, msg });
  }
}

function cancelledOrDone(cancelled: boolean, running: number): boolean {
  return cancelled || running === 0;
}

export async function listModels(harness = 'opencode', cwd = process.cwd()): Promise<string[]> {
  return new Promise((resolve) => {
    const child = spawn(harness, ['models'], {
      cwd,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let out = '';
    child.stdout?.setEncoding('utf8');
    child.stdout?.on('data', (c: string) => (out += c));
    child.on('error', () => resolve([]));
    child.on('exit', () => {
      const models = out
        .split('\n')
        .map((l) => l.trim())
        .filter((l) => l.length > 0);
      resolve(models);
    });
  });
}

export function attachStoreEvents(pipeline: Pipeline): () => void {
  const off = pipeline.on((ev) => {
    if (ev.type === 'output') {
      const t =
        ev.kind === 'error'
          ? 'error'
          : ev.kind === 'system'
            ? 'system'
            : ev.kind === 'tool_output'
              ? 'tool_output'
              : 'assistant';
      useStore.getState().addOutput(ev.text, t);
    } else if (ev.type === 'subAgentOutput') {
      useStore.getState().addSubAgentLog(ev.agentId, ev.text, 'tool_output');
    } else if (ev.type === 'phaseChange') {
      useStore.getState().setPipelinePhase({
        type: ev.phase.type as never,
        label: ev.phase.label,
        progress: ev.phase.progress,
      });
      if (ev.phase.type === 'completed') useStore.getState().setStatus('completed');
      else if (ev.phase.type === 'failed') useStore.getState().setStatus('failed');
      else useStore.getState().setStatus('running');
    } else if (ev.type === 'subAgentUpdate') {
      useStore.getState().setSubAgents(ev.agents);
    } else if (ev.type === 'pipelineUpdate') {
      useStore.getState().setPipelines(ev.pipelines);
    } else if (ev.type === 'contextUpdate') {
      useStore.getState().setContextEntries(ev.entries);
    } else if (ev.type === 'done') {
      useStore.getState().setStatus(ev.ok ? 'completed' : 'failed');
      useStore.getState().addOutput(`[pipeline] ${ev.ok ? '✓' : '✗'} ${ev.msg}`, 'system');
    }
  });
  return off;
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s;
  return s.slice(0, n - 1) + '…';
}
