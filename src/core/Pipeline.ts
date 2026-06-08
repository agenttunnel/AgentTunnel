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
import type { AppConfig, RoleName, SubAgentInfo } from '../types/index.js';
import { useStore } from './state/store.js';
import type { ToolCall } from './tools.js';

export interface PipelineOptions {
  cwd: string;
  config: AppConfig;
  onPhaseChange?: (phase: { type: string; label: string; progress?: { current: number; total: number } }) => void;
  onSubAgentUpdate?: (agents: SubAgentInfo[]) => void;
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
  | { type: 'done'; ok: boolean; msg: string }
  | { type: 'toolCall'; call: ToolCall }
  | { type: 'contextUpdate'; entries: ContextEntry[] };

type Listener = (ev: PipelineEvent) => void;

export interface DispatchedTask {
  role: RoleName;
  task: string;
  context: string[];
  /** ID of the sub-agent that produced this task (undefined for first-pass dispatches). */
  fromAgentId?: string;
}

const MAX_AGENT_OUTPUT_LINES = 200;

export class Pipeline {
  private orchestrator: Session | null = null;
  private currentSubAgent: Session | null = null;
  private agents: SubAgentInfo[] = [];
  private dispatched: DispatchedTask[] = [];
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

  async run(userTask: string): Promise<void> {
    if (this.running) {
      this.emit({ type: 'output', text: '[pipeline] already running, ignoring new task', kind: 'system' });
      return;
    }
    this.running = true;
    this.cancelled = false;
    this.agents = [];
    this.dispatched = [];

    this.emit({ type: 'phaseChange', phase: { type: 'orchestrating', label: 'orchestrator planning' } });
    this.emit({ type: 'output', text: '[pipeline] dispatching orchestrator', kind: 'system' });
    this.emit({ type: 'output', text: `Available tools: ${ORCHESTRATOR_TOOLS.map((t) => t.name).join(', ')}`, kind: 'system' });
    this.emitContext();

    try {
      while (this.orchestratorPasses < this.maxOrchestratorPasses) {
        const dispatchedBefore = this.dispatched.length;
        const exits = await this.runOrchestratorPass(userTask, this.orchestratorPasses);
        if (this.cancelled || !exits.ok) {
          this.finish(false, exits.msg);
          return;
        }
        if (this.dispatched.length === dispatchedBefore) {
          // orchestrator did not dispatch anything new — finish
          this.finish(true, this.context.list().length > 0 ? 'orchestrator finished' : 'orchestrator: no tasks');
          return;
        }
        const ok = await this.runAllExecutors();
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

  /**
   * Run the orchestrator LLM once. Returns ok=true if it exited cleanly.
   * The orchestrator can either dispatch tasks (which are queued) or call `done`
   * to indicate it has no more work.
   */
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
          text: `[pipeline] orchestrator pass #${pass + 1} (reviewing ${this.dispatched.length} task(s))`,
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
    const recent = this.dispatched
      .slice(-5)
      .map((d) => `- [${d.role}] ${truncate(d.task, 100)}`)
      .join('\n');
    return [
      `${ORCHESTRATOR_PROMPT}`,
      ``,
      `## Summary pass`,
      ``,
      `This is not the first pass. Below is what sub-agents have reported.`,
      `Use it to decide whether to dispatch follow-up tasks (e.g. a reviewer)`,
      `or to call \`done\`.`,
      ``,
      `### Recent tasks`,
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
    if (clean.trim().length > 0) {
      this.emit({
        type: 'output',
        text: clean,
        kind: role === 'orchestrator' ? 'assistant' : 'tool_output',
      });
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

  private handleOrchestratorToolCall(call: ToolCall): void {
    if (call.name === 'dispatch_task') {
      const role = String(call.arguments.role ?? '');
      const task = String(call.arguments.task ?? '');
      const contextRaw = call.arguments.context;
      const context = Array.isArray(contextRaw)
        ? contextRaw.filter((k): k is string => typeof k === 'string')
        : [];
      if ((role === 'executor' || role === 'reviewer') && task.length > 0) {
        this.dispatched.push({ role: role as RoleName, task, context });
        this.emit({
          type: 'output',
          text: `[orchestrator] → ${role}: ${truncate(task, 80)}${context.length > 0 ? ` (context: ${context.join(', ')})` : ''}`,
          kind: 'system',
        });
      } else {
        this.emit({
          type: 'output',
          text: `[orchestrator] invalid dispatch_task arguments: ${JSON.stringify(call.arguments)}`,
          kind: 'error',
        });
      }
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
      // done is implicit — empty dispatched array after this pass will end the pipeline
    } else {
      this.emit({
        type: 'output',
        text: `[orchestrator] unknown tool: ${call.name}`,
        kind: 'error',
      });
    }
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
    } else if (call.name === 'update_context') {
      // sub-agents shouldn't write to the orchestrator's notebook directly,
      // but if they try, treat it as a note_finding for safety.
      const key = String(call.arguments.key ?? '');
      const value = String(call.arguments.value ?? '');
      try {
        this.context.append(key, value, agentId);
        this.emitContext();
      } catch {
        // ignore
      }
    } else if (call.name === 'done') {
      this.emit({
        type: 'output',
        text: `[${agentId}] done`,
        kind: 'system',
      });
    }
    // other tools from sub-agents are ignored — they have no handlers in this pipeline
  }

  private async runAllExecutors(): Promise<boolean> {
    const startIdx = this.dispatched.length === 0 ? 0 : Math.max(0, this.dispatched.length - this.dispatched.length);
    const newTasks = this.dispatched.slice(startIdx);

    this.emit({
      type: 'phaseChange',
      phase: {
        type: 'executing',
        label: `executors (0/${newTasks.length})`,
        progress: { current: 0, total: newTasks.length },
      },
    });

    let okCount = 0;
    for (let i = 0; i < newTasks.length; i++) {
      if (this.cancelled) break;
      const task = newTasks[i];
      if (!task) continue;
      this.emit({
        type: 'phaseChange',
        phase: {
          type: 'executing',
          label: `executors (${i + 1}/${newTasks.length})`,
          progress: { current: i + 1, total: newTasks.length },
        },
      });
      const ok = await this.runOne(task);
      if (ok) okCount++;
    }

    if (this.cancelled) return false;
    return okCount === newTasks.length;
  }

  private async runOne(task: DispatchedTask): Promise<boolean> {
    const agent: SubAgentInfo = {
      id: this.options.newAgentId(),
      role: task.role,
      status: 'running',
      task: task.task,
      output: [],
      startedAt: Date.now(),
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
        // Cap each agent's buffer to MAX_AGENT_OUTPUT_LINES
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
        if (this.currentSubAgent === session) this.currentSubAgent = null;
        resolve(code === 0);
      });

      session.run({ prompt }).catch((err) => {
        agent.endedAt = Date.now();
        agent.status = 'failed';
        this.agents = [...this.agents];
        this.emit({ type: 'subAgentUpdate', agents: this.agents });
        this.emit({ type: 'output', text: `[${task.role}] failed: ${(err as Error).message}`, kind: 'error' });
        if (this.currentSubAgent === session) this.currentSubAgent = null;
        resolve(false);
      });
    });
  }

  cancel(): void {
    this.cancelled = true;
    this.emit({ type: 'output', text: '[pipeline] cancel requested', kind: 'system' });
    this.orchestrator?.cancel();
    this.currentSubAgent?.cancel();
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
