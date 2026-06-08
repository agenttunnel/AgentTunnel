import { spawn, type ChildProcess } from 'node:child_process';

export type SessionEvent =
  | { kind: 'stdout'; line: string }
  | { kind: 'stderr'; line: string }
  | { kind: 'exit'; code: number | null; signal: NodeJS.Signals | null }
  | { kind: 'spawn'; pid: number }
  | { kind: 'error'; err: Error };

type Listener = (ev: SessionEvent) => void;
type OutputListener = (line: string, stream: 'stdout' | 'stderr') => void;
type ExitListener = (info: { code: number | null; signal: NodeJS.Signals | null }) => void;

export interface SessionOptions {
  cwd?: string;
  model?: string;
  extraArgs?: string[];
  dangerouslySkipPermissions?: boolean;
  env?: NodeJS.ProcessEnv;
  harness?: string;
  onStderr?: 'merge' | 'separate' | 'ignore';
}

export interface RunOptions {
  prompt: string;
  cwd?: string;
  model?: string;
  dangerouslySkipPermissions?: boolean;
  extraArgs?: string[];
}

export class Session {
  private child: ChildProcess | null = null;
  private stdoutBuf = '';
  private stderrBuf = '';
  private killTimer: NodeJS.Timeout | null = null;
  private readonly eventListeners = new Set<Listener>();
  private readonly outputListeners = new Set<OutputListener>();
  private readonly exitListeners = new Set<ExitListener>();

  constructor(private readonly options: SessionOptions = {}) {}

  isRunning(): boolean {
    return this.child !== null && this.child.exitCode === null;
  }

  pid(): number | undefined {
    return this.child?.pid;
  }

  onEvent(listener: Listener): () => void {
    this.eventListeners.add(listener);
    return () => this.eventListeners.delete(listener);
  }

  onOutput(cb: OutputListener): () => void {
    this.outputListeners.add(cb);
    return () => this.outputListeners.delete(cb);
  }

  onExit(cb: ExitListener): () => void {
    this.exitListeners.add(cb);
    return () => this.exitListeners.delete(cb);
  }

  private emit(ev: SessionEvent): void {
    for (const l of this.eventListeners) {
      try {
        l(ev);
      } catch {
        // never let a bad listener kill the session
      }
    }
    if (ev.kind === 'stdout' || ev.kind === 'stderr') {
      for (const l of this.outputListeners) {
        try {
          l(ev.line, ev.kind);
        } catch {
          // ignore
        }
      }
    } else if (ev.kind === 'exit') {
      for (const l of this.exitListeners) {
        try {
          l({ code: ev.code, signal: ev.signal });
        } catch {
          // ignore
        }
      }
    }
  }

  async run(opts: RunOptions): Promise<{ code: number | null; signal: NodeJS.Signals | null }> {
    if (this.isRunning()) {
      throw new Error('Session is already running');
    }

    const harness = this.options.harness ?? 'opencode';
    const args: string[] = ['run'];

    const model = opts.model ?? this.options.model;
    if (model && model.length > 0) {
      args.push('-m', model);
    }

    const skipPerms = opts.dangerouslySkipPermissions ?? this.options.dangerouslySkipPermissions ?? true;
    if (skipPerms) {
      args.push('--dangerously-skip-permissions');
    }

    if (opts.extraArgs && opts.extraArgs.length > 0) {
      args.push(...opts.extraArgs);
    }

    args.push(opts.prompt);

    const cwd = opts.cwd ?? this.options.cwd ?? process.cwd();
    const env = { ...process.env, ...(this.options.env ?? {}) };

    return new Promise((resolve) => {
      const child = spawn(harness, args, {
        cwd,
        env,
        stdio: ['ignore', 'pipe', 'pipe'],
      });
      this.child = child;
      this.stdoutBuf = '';
      this.stderrBuf = '';

      this.emit({ kind: 'spawn', pid: child.pid ?? -1 });

      child.stdout?.setEncoding('utf8');
      child.stderr?.setEncoding('utf8');

      child.stdout?.on('data', (chunk: string) => this.handleChunk(chunk, 'stdout'));
      child.stderr?.on('data', (chunk: string) => this.handleChunk(chunk, 'stderr'));

      child.on('error', (err) => {
        this.emit({ kind: 'error', err });
      });

      child.on('exit', (code, signal) => {
        this.flushBuffers();
        this.emit({ kind: 'exit', code, signal });
        this.child = null;
        if (this.killTimer) {
          clearTimeout(this.killTimer);
          this.killTimer = null;
        }
        resolve({ code, signal });
      });
    });
  }

  private handleChunk(chunk: string, stream: 'stdout' | 'stderr'): void {
    const bufKey = stream === 'stdout' ? 'stdoutBuf' : 'stderrBuf';
    const combined = (this as unknown as Record<string, string>)[bufKey] + chunk;
    const parts = combined.split(/\r?\n/);
    const tail = parts.pop() ?? '';
    (this as unknown as Record<string, string>)[bufKey] = tail;

    const stderrMode = this.options.onStderr ?? 'separate';
    for (const line of parts) {
      if (line.length === 0) continue;
      if (stream === 'stderr' && stderrMode === 'merge') {
        this.emit({ kind: 'stdout', line });
      } else if (stream === 'stderr' && stderrMode === 'ignore') {
        // drop
      } else {
        this.emit({ kind: stream, line });
      }
    }
  }

  private flushBuffers(): void {
    if (this.stdoutBuf.length > 0) {
      this.emit({ kind: 'stdout', line: this.stdoutBuf });
      this.stdoutBuf = '';
    }
    if (this.stderrBuf.length > 0) {
      this.emit({ kind: 'stderr', line: this.stderrBuf });
      this.stderrBuf = '';
    }
  }

  cancel(): void {
    if (!this.child || this.child.exitCode !== null) return;
    try {
      this.child.kill('SIGTERM');
    } catch {
      // ignore
    }
    if (this.killTimer) clearTimeout(this.killTimer);
    this.killTimer = setTimeout(() => {
      if (this.child && this.child.exitCode === null) {
        try {
          this.child.kill('SIGKILL');
        } catch {
          // ignore
        }
      }
    }, 5000);
  }
}
