import { spawn } from 'node:child_process';
import { Session } from './Session.js';
import {
  ORCHESTRATOR_PROMPT,
  EXECUTOR_PROMPT,
  REVIEWER_PROMPT,
  ORCHESTRATOR_TOOLS,
} from './prompts.js';
import { stripAnsi } from './utils/AnsiCleaner.js';
import { parseToolCallLine } from './ToolRegistry.js';
import type { AppConfig, RoleName, SubAgentInfo } from '../types/index.js';
import { useStore } from './state/store.js';
import type { ToolCall } from './tools.js';

export interface PipelineOptions {
  cwd: string;
  config: AppConfig;
  onPhaseChange?: (phase: { type: string; label: string; progress?: { current: number; total: number } }) => void;
  onSubAgentUpdate?: (agents: SubAgentInfo[]) => void;
  onDone?: (ok: boolean, msg: string) => void;
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
  | { type: 'toolCall'; call: ToolCall };

type Listener = (ev: PipelineEvent) => void;

export class Pipeline {
  private orchestrator: Session | null = null;
  private executor: Session | null = null;
  private agents: SubAgentInfo[] = [];
  private dispatched: { role: RoleName; task: string }[] = [];
  private running = false;
  private cancelled = false;
  private listeners = new Set<Listener>();
  private orchestratorDone = false;

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

  async run(userTask: string): Promise<void> {
    if (this.running) {
      this.emit({ type: 'output', text: '[pipeline] already running, ignoring new task', kind: 'system' });
      return;
    }
    this.running = true;
    this.cancelled = false;
    this.agents = [];
    this.dispatched = [];
    this.orchestratorDone = false;

    this.emit({ type: 'phaseChange', phase: { type: 'orchestrating', label: 'orchestrator planning' } });
    this.emit({ type: 'output', text: '[pipeline] dispatching orchestrator', kind: 'system' });
    this.emit({ type: 'output', text: `Available tools: ${ORCHESTRATOR_TOOLS.map((t) => t.name).join(', ')}`, kind: 'system' });

    const orchestrator = new Session({
      cwd: this.options.cwd,
      harness: this.options.harness ?? 'opencode',
      model: this.options.config.orchestrator.model || undefined,
      dangerouslySkipPermissions: this.options.dangerouslySkipPermissions ?? true,
    });
    this.orchestrator = orchestrator;

    orchestrator.onOutput((line) => this.handleOrchestratorLine(line));
    orchestrator.onExit(({ code, signal }) => {
      if (this.cancelled) {
        this.finish(false, 'cancelled');
        return;
      }
      if (code !== 0) {
        this.emit({ type: 'output', text: `[orchestrator] exited code=${code} signal=${signal ?? ''}`, kind: 'error' });
        this.finish(false, `orchestrator exited with code ${code}`);
        return;
      }
      void this.runExecutors();
    });

    const prompt = `${ORCHESTRATOR_PROMPT}\n\nUser task:\n${userTask}`;
    try {
      await orchestrator.run({ prompt });
    } catch (err) {
      this.emit({ type: 'output', text: `[orchestrator] failed to start: ${(err as Error).message}`, kind: 'error' });
      this.finish(false, (err as Error).message);
    }
  }

  private handleOrchestratorLine(line: string): void {
    const clean = stripAnsi(line);

    const call = parseToolCallLine(clean);
    if (call) {
      this.emit({ type: 'toolCall', call });
      this.handleOrchestratorToolCall(call);
      return;
    }

    if (clean.trim().length > 0) {
      this.emit({ type: 'output', text: clean, kind: 'assistant' });
    }
  }

  private handleOrchestratorToolCall(call: ToolCall): void {
    if (call.name === 'dispatch_task') {
      const role = String(call.arguments.role ?? '');
      const task = String(call.arguments.task ?? '');
      if ((role === 'executor' || role === 'reviewer') && task.length > 0) {
        this.dispatched.push({ role: role as RoleName, task });
        this.emit({
          type: 'output',
          text: `[orchestrator] → ${role}: ${truncate(task, 80)}`,
          kind: 'system',
        });
      } else {
        this.emit({
          type: 'output',
          text: `[orchestrator] invalid dispatch_task arguments: ${JSON.stringify(call.arguments)}`,
          kind: 'error',
        });
      }
    } else if (call.name === 'done') {
      this.orchestratorDone = true;
      const reason = String(call.arguments.reason ?? 'complete');
      this.emit({ type: 'output', text: `[orchestrator] done (${reason})`, kind: 'system' });
    } else {
      this.emit({ type: 'output', text: `[orchestrator] unknown tool: ${call.name}`, kind: 'error' });
    }
  }

  private async runExecutors(): Promise<void> {
    if (this.dispatched.length === 0) {
      this.finish(true, this.orchestratorDone ? 'orchestrator: no tasks' : 'orchestrator produced no tasks');
      return;
    }

    this.emit({
      type: 'phaseChange',
      phase: {
        type: 'executing',
        label: `executors (0/${this.dispatched.length})`,
        progress: { current: 0, total: this.dispatched.length },
      },
    });

    let okCount = 0;
    for (let i = 0; i < this.dispatched.length; i++) {
      if (this.cancelled) break;
      const task = this.dispatched[i];
      if (!task) continue;
      this.emit({
        type: 'phaseChange',
        phase: {
          type: 'executing',
          label: `executors (${i}/${this.dispatched.length})`,
          progress: { current: i, total: this.dispatched.length },
        },
      });
      const ok = await this.runOne(task, i);
      if (ok) okCount++;
    }

    if (this.cancelled) {
      this.finish(false, 'cancelled');
      return;
    }
    this.finish(okCount === this.dispatched.length, `${okCount}/${this.dispatched.length} tasks ok`);
  }

  private async runOne(task: { role: RoleName; task: string }, index: number): Promise<boolean> {
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
    this.executor = session;

    const sysPrompt = task.role === 'executor' ? EXECUTOR_PROMPT : REVIEWER_PROMPT;
    const prompt = `${sysPrompt}\n\nTask #${index + 1}:\n${task.task}`;

    return new Promise<boolean>((resolve) => {
      session.onOutput((line) => {
        const clean = stripAnsi(line);
        if (agent.output.length < 200) {
          agent.output = [...agent.output, clean];
        } else {
          agent.output = [...agent.output.slice(agent.output.length - 199), clean];
        }
        this.agents = [...this.agents];
        this.emit({ type: 'subAgentUpdate', agents: this.agents });
        this.emit({ type: 'output', text: clean, kind: 'tool_output' });
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
        resolve(code === 0);
      });

      session.run({ prompt }).catch((err) => {
        agent.endedAt = Date.now();
        agent.status = 'failed';
        this.agents = [...this.agents];
        this.emit({ type: 'subAgentUpdate', agents: this.agents });
        this.emit({ type: 'output', text: `[${task.role}] failed: ${(err as Error).message}`, kind: 'error' });
        resolve(false);
      });
    });
  }

  cancel(): void {
    this.cancelled = true;
    this.emit({ type: 'output', text: '[pipeline] cancel requested', kind: 'system' });
    this.orchestrator?.cancel();
    this.executor?.cancel();
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
