/**
 * `t3-tui` — AgentTunnel in a terminal.
 *
 * The presentation is a pure function: `renderFrame(state, size)` returns the
 * screen as lines, and the painter writes only the lines that changed. Input
 * comes straight from stdin, translated by `parseKeys` into the keys the
 * reducer speaks. Between those two ends, everything is the same data layer the
 * web client uses — the shell and thread subscriptions, `server.getConfig` for
 * providers and settings, and `thread.turn.start` with `bootstrap.createThread`
 * to start a thread.
 *
 *   bun src/index.ts --url http://localhost:13773 [--token <session token>]
 *   bun src/index.ts --demo
 */
import type { OrchestrationThreadActivity } from "@t3tools/contracts";
import * as DateTime from "effect/DateTime";
import * as Effect from "effect/Effect";
import * as Fiber from "effect/Fiber";
import * as Random from "effect/Random";
import * as Scope from "effect/Scope";
import * as Stream from "effect/Stream";
import { remoteHttpClientLayer } from "@t3tools/client-runtime/rpc";

import {
  agentTunnelCatalog,
  projectRows,
  providerRows,
  serverSettingRows,
  threadRows,
  threadView,
} from "./format.ts";
import { buildAgentTunnelPatch, resolveAgentTunnelConfig } from "./agentTunnel.ts";
import { renderFrame } from "./frame.ts";
import { parseKeys } from "./keys.ts";
import { createPainter } from "./paint.ts";
import { loadPreferences, savePreferences, type TuiPreferences } from "./preferences.ts";
import {
  addProject,
  connect,
  getAgentOutput,
  interruptTurn,
  loadServerConfig,
  loadShell,
  resolveConnection,
  resolveSocketTicket,
  sendTurn,
  shellStream,
  startThread,
  threadStream,
  updateServerSettings,
  TuiSessionError,
  type TuiSession,
} from "./session.ts";
import {
  initialTuiState,
  isThreadRunning,
  reduceKey,
  resolveModelSelection,
  selectedThread,
  withAgentLog,
  withCatalogs,
  withOpenThread,
  withPreferences,
  withRunningState,
  withSpinner,
  withShellItem,
  withStatus,
  withThreadEvent,
  withThreadSnapshot,
  withThreads,
  type TuiEffect,
  type TuiState,
} from "./state.ts";
import { foldAgentLogEntries } from "@t3tools/client-runtime/state/agentLog";
import type { ServerSettings } from "@t3tools/contracts";

/** Attempts the first connection makes while the server finishes starting. */
const ERROR_RETRIES = 6;

const USAGE = `t3-tui — AgentTunnel in a terminal

Usage: bun src/index.ts [--url <http://host:port>] [--token <session token>]
       bun src/index.ts --demo

  --url    AgentTunnel server origin (default: $T3CODE_URL)
  --token  session credential, exchanged for a WebSocket ticket (default: $T3CODE_TOKEN)
  --demo   render canned data without connecting
  --help   this message
`;

export function parseArgs(argv: ReadonlyArray<string>): {
  readonly url: string | null;
  readonly token: string | undefined;
  readonly demo: boolean;
  readonly help: boolean;
} {
  let url: string | null = process.env.T3CODE_URL?.trim() ?? null;
  let token = process.env.T3CODE_TOKEN?.trim() || undefined;
  let demo = false;
  let help = false;
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === "--help" || arg === "-h") help = true;
    else if (arg === "--demo") demo = true;
    else if (arg === "--url") url = argv[index + 1]?.trim() ?? null;
    else if (arg === "--token") token = argv[index + 1]?.trim() || undefined;
  }
  return { url: url === "" ? null : url, token, demo, help };
}

/** A canned screen for `--demo`, so the rendering can be seen without a server. */
function demoState(): TuiState {
  return withThreads(withStatus(initialTuiState, "demo — not connected"), [
    {
      id: "demo-1",
      title: "Router state design doc",
      projectTitle: "AgentTunnel",
      branch: "main",
      fleet: null,
    },
    {
      id: "demo-2",
      title: "Subagent manager polish",
      projectTitle: "AgentTunnel",
      branch: "feat/agents",
      fleet: null,
    },
  ]);
}

/** One log read, flattened: markdown stays text, tools become one line. */
async function readAgentLogLines(
  session: TuiSession,
  outputPath: string,
): Promise<ReadonlyArray<string>> {
  const result = await Effect.runPromise(getAgentOutput(session, outputPath));
  return foldAgentLogEntries(result.entries).map((block) => {
    if (block.kind === "tool") {
      return `▸ ${block.title} · ${block.status}${block.tool === "" ? "" : ` (${block.tool})`}`;
    }
    if (block.kind === "raw") return `# raw\n${block.text}`;
    if (block.kind === "error") return `! ${block.text}`;
    if (block.kind === "reasoning") return `# reasoning\n${block.text}`;
    return block.text;
  });
}

export async function run(argv: ReadonlyArray<string> = process.argv.slice(2)): Promise<void> {
  const args = parseArgs(argv);
  if (args.help) {
    process.stdout.write(USAGE);
    return;
  }
  if (args.url === null && !args.demo) {
    process.stderr.write(`${USAGE}\n--url is required (or set T3CODE_URL), or use --demo.\n`);
    process.exitCode = 2;
    return;
  }

  let state: TuiState = args.demo ? demoState() : withStatus(initialTuiState, "connecting…");
  let session: TuiSession | null = null;
  let settings: ServerSettings | null = null;
  let threadFiber: Fiber.Fiber<unknown, unknown> | null = null;
  let shellFiber: Fiber.Fiber<unknown, unknown> | null = null;
  let logFiber: Fiber.Fiber<unknown, unknown> | null = null;

  const painter = createPainter({
    write: (chunk) => process.stdout.write(chunk),
    size: () => ({ rows: process.stdout.rows ?? 24, cols: process.stdout.columns ?? 80 }),
    onResize: (handler) => {
      process.stdout.on("resize", handler);
      return () => {
        process.stdout.off("resize", handler);
      };
    },
  });

  let scheduled = false;
  const requestPaint = (): void => {
    if (scheduled) return;
    scheduled = true;
    queueMicrotask(() => {
      scheduled = false;
      painter.paint(renderFrame(state, painter.size));
    });
  };

  const apply = (next: TuiState): void => {
    if (next === state) return;
    state = next;
    requestPaint();
    syncTicker();
  };

  /**
   * Elapsed time and the spinner have to move while work runs, and must cost
   * nothing when it does not: the ticker is forked only while the thread runs.
   */
  let ticker: Fiber.Fiber<unknown, unknown> | null = null;
  const syncTicker = (): void => {
    const running = isThreadRunning(state);
    if (running && ticker === null) {
      ticker = Effect.runFork(
        Effect.gen(function* () {
          while (isThreadRunning(state)) {
            apply(withSpinner(state, state.spinner + 1));
            yield* Effect.sleep("1 second");
          }
        }),
      );
    } else if (!running && ticker !== null) {
      const fiber = ticker;
      ticker = null;
      void Effect.runPromise(Fiber.interrupt(fiber));
    }
  };

  const stopFiber = async (fiber: Fiber.Fiber<unknown, unknown> | null): Promise<void> => {
    if (fiber !== null) await Effect.runPromise(Fiber.interrupt(fiber));
  };

  const stopThreadStream = async (): Promise<void> => {
    const fiber = threadFiber;
    threadFiber = null;
    await stopFiber(fiber);
  };

  const stopAgentLog = async (): Promise<void> => {
    const fiber = logFiber;
    logFiber = null;
    await stopFiber(fiber);
  };

  const guard = async (label: string, run: () => Promise<void>): Promise<void> => {
    try {
      await run();
    } catch (error) {
      apply(withStatus(state, `${label} failed: ${String(error)}`));
    }
  };

  const refreshThreads = (): Promise<void> =>
    guard("refresh", async () => {
      if (session === null) return;
      const active = session;
      const [snapshot, config] = await Promise.all([
        Effect.runPromise(loadShell(active)),
        Effect.runPromise(loadServerConfig(active)),
      ]);
      settings = config.settings;
      apply(
        withCatalogs(
          withThreads(
            withStatus(state, `${snapshot.threads.length} threads`),
            threadRows(snapshot),
          ),
          {
            projects: projectRows(snapshot),
            providers: providerRows(config),
            serverSettings: serverSettingRows(config.settings),
            agentTunnel: resolveAgentTunnelConfig(config.settings),
            agentTunnelCatalog: agentTunnelCatalog(config),
          },
        ),
      );
    });

  const openThread = (threadId: string): Promise<void> =>
    guard("open", async () => {
      if (session === null) return;
      const active = session;
      apply(withOpenThread(withStatus(state, "loading thread…"), threadId));
      await stopThreadStream();
      threadFiber = Effect.runFork(
        threadStream(active, { threadId, afterSequence: state.lastThreadSequence }).pipe(
          Stream.runForEach((item) =>
            Effect.gen(function* () {
              // Effect's clock, so the elapsed time the strip shows is the
              // server's timeline rather than a stray Date.
              const at = DateTime.formatIso(yield* DateTime.now);
              if (item.kind === "snapshot") apply(withThreadSnapshot(state, item.snapshot));
              else if (item.kind === "event") apply(withThreadEvent(state, item.event));
              apply(withRunningState(state, at));
            }),
          ),
          Effect.catch((error: TuiSessionError) =>
            Effect.sync(() => {
              apply(withStatus(state, `live updates stopped: ${error.message}`));
              void reconnect();
            }),
          ),
        ),
      );
    });

  const startShellStream = async (): Promise<void> => {
    if (session === null) return;
    const active = session;
    await stopFiber(shellFiber);
    shellFiber = Effect.runFork(
      shellStream(active, { afterSequence: state.lastShellSequence }).pipe(
        Stream.runForEach((item) => Effect.sync(() => apply(withShellItem(state, item)))),
        Effect.catch((error: TuiSessionError) =>
          Effect.sync(() => apply(withStatus(state, `list updates stopped: ${error.message}`))),
        ),
      ),
    );
  };

  const send = async (threadId: string, text: string): Promise<void> =>
    guard("send", async () => {
      if (session === null) return;
      const selection = state.modelSelection;
      await Effect.runPromise(
        sendTurn(session, {
          threadId,
          text,
          ...(selection === null ? {} : { modelSelection: selection }),
        }),
      );
      apply(withStatus(state, "sent"));
      await openThread(threadId);
    });

  const createThread = async (
    previous: TuiState,
    projectId: string,
    title: string,
    text: string,
  ): Promise<void> =>
    guard("create", async () => {
      if (session === null) return;
      const project = previous.projects.find((row) => row.id === projectId) ?? null;
      const modelSelection = resolveModelSelection(previous, project);
      if (modelSelection === null) {
        apply(withStatus(state, "no provider configured — run /provider"));
        return;
      }
      const threadId = await Effect.runPromise(
        Random.nextInt.pipe(Effect.map((seed) => `tui-${seed.toString(36)}`)),
      );
      await Effect.runPromise(
        startThread(session, { threadId, projectId, title, text, modelSelection }),
      );
      apply(withStatus(state, "created"));
      await openThread(threadId);
    });

  const pollAgentLog = (agentId: string, label: string, outputPath: string): void => {
    void stopAgentLog();
    const active = session;
    if (active === null) return;
    logFiber = Effect.runFork(
      Effect.gen(function* () {
        while (state.view === "agentLog") {
          yield* Effect.tryPromise({
            try: async () => {
              const lines = await readAgentLogLines(active, outputPath);
              apply(withAgentLog(state, { agentId, label, lines, error: null }));
            },
            catch: (error) => new TuiSessionError({ message: String(error) }),
          }).pipe(
            Effect.catch((error) =>
              Effect.sync(() =>
                apply(withAgentLog(state, { agentId, label, lines: [], error: error.message })),
              ),
            ),
          );
          yield* Effect.sleep("1 second");
        }
      }),
    );
  };

  /**
   * Open a session, retrying while the server finishes starting: its port
   * accepts connections before auth is wired, and the ticket exchange then
   * times out. Nothing here is fatal — ctrl-r retries too.
   */
  const openSession = async (attempts: number): Promise<void> => {
    const url = args.url;
    if (url === null) return;
    let lastError: unknown = null;
    for (let attempt = 0; attempt < attempts; attempt += 1) {
      try {
        const ticket = await Effect.runPromise(
          resolveSocketTicket({
            httpBaseUrl: url.replace(/\/+$/u, ""),
            token: args.token,
          }).pipe(Effect.provide(remoteHttpClientLayer(fetch))),
        );
        const scope = await Effect.runPromise(Scope.make());
        session = await Effect.runPromise(
          connect(resolveConnection({ url, ticket: ticket ?? undefined })).pipe(
            Effect.provideService(Scope.Scope, scope),
          ),
        );
        return;
      } catch (error) {
        lastError = error;
        session = null;
        if (attempt < attempts - 1) {
          apply(withStatus(state, `connecting… (attempt ${attempt + 2} of ${attempts})`));
          await Effect.runPromise(Effect.sleep("1 second"));
        }
      }
    }
    throw lastError;
  };

  const reconnect = async (): Promise<void> =>
    guard("reconnect", async () => {
      if (args.url === null) return;
      await stopThreadStream();
      await openSession(3);
      apply(withStatus(state, "reconnected"));
      await startShellStream();
      if (state.openThreadId !== null) await openThread(state.openThreadId);
    });

  const persistPreferences = (patch: Partial<TuiPreferences>): Promise<void> =>
    guard("settings", async () => {
      const next = { ...state.preferences, ...patch };
      await savePreferences(next);
      apply(withStatus(state, "settings saved"));
    });

  const persistServerSetting = (key: string, value: boolean | string): Promise<void> =>
    guard("settings", async () => {
      if (session === null) return;
      await Effect.runPromise(updateServerSettings(session, { [key]: value }));
      apply(withStatus(state, "server setting saved"));
      await refreshThreads();
    });

  const persistAgentTunnelSetting = (
    field: string,
    value: string | number | boolean,
  ): Promise<void> =>
    guard("settings", async () => {
      if (session === null) return;
      const patch =
        settings === null
          ? { providers: { agenttunnel: { [field]: value } } }
          : buildAgentTunnelPatch(settings, field, value);
      await Effect.runPromise(updateServerSettings(session, patch));
      apply(withStatus(state, "workflow settings saved"));
      await refreshThreads();
    });

  const registerProject = (workspaceRoot: string): Promise<void> =>
    guard("add project", async () => {
      if (session === null) return;
      await Effect.runPromise(addProject(session, { workspaceRoot }));
      apply(withStatus(state, `added ${workspaceRoot}`));
      await refreshThreads();
    });

  const perform = (effect: TuiEffect, previous: TuiState): void => {
    const threadId = previous.openThreadId;
    switch (effect?.kind) {
      case "quit":
        shutdown(0);
        return;
      case "refresh":
        // With no session there is nothing to refresh: this is a retry.
        if (session === null) {
          void reconnect();
          return;
        }
        void (previous.view === "thread" && threadId !== null
          ? openThread(threadId)
          : refreshThreads());
        return;
      case "open": {
        const row = selectedThread(previous);
        if (row !== null) void openThread(row.id);
        return;
      }
      case "send":
        if (threadId === null || args.demo) return;
        void send(threadId, effect.text);
        return;
      case "createThread":
        if (args.demo) return;
        void createThread(previous, effect.projectId, effect.title, effect.text);
        return;
      case "interrupt":
        if (threadId === null || args.demo) return;
        void guard("interrupt", async () => {
          if (session === null) return;
          await Effect.runPromise(interruptTurn(session, threadId));
          apply(withStatus(state, "interrupt requested"));
        });
        return;
      case "savePreferences":
        if (args.demo) return;
        void persistPreferences(effect.patch);
        return;
      case "saveServerSetting":
        if (args.demo) return;
        void persistServerSetting(effect.key, effect.value);
        return;
      case "saveAgentTunnelSetting":
        if (args.demo) return;
        void persistAgentTunnelSetting(effect.field, effect.value);
        return;
      case "addProject":
        if (args.demo) return;
        void registerProject(effect.workspaceRoot);
        return;
      case "openAgentLog":
        if (args.demo) return;
        pollAgentLog(effect.agentId, effect.label, effect.outputPath);
        return;
      default:
        return;
    }
  };

  let shuttingDown = false;
  const shutdown = (code: number): void => {
    if (shuttingDown) return;
    shuttingDown = true;
    if (ticker !== null) void Effect.runPromise(Fiber.interrupt(ticker));
    painter.exit();
    process.stdin.setRawMode?.(false);
    process.stdin.pause();
    void stopThreadStream();
    void stopFiber(shellFiber);
    void stopAgentLog();
    process.exit(code);
  };

  process.on("SIGINT", () => shutdown(0));
  process.on("SIGTERM", () => shutdown(0));

  painter.enter();
  process.stdin.setRawMode?.(true);
  process.stdin.resume();
  process.stdin.on("data", (chunk: Buffer) => {
    for (const key of parseKeys(chunk.toString("utf8"))) {
      const before = state;
      const { state: next, effect } = reduceKey(before, key);
      apply(next);
      perform(effect, before);
    }
    // The reducer can leave the view without touching state (for example when a
    // picker opens); repaint anyway so the frame always matches the state.
    requestPaint();
  });

  if (args.demo) {
    requestPaint();
    return;
  }

  try {
    const preferences = await loadPreferences();
    apply(withPreferences(state, preferences));
  } catch (error) {
    apply(withStatus(state, `settings not loaded: ${String(error)}`));
  }

  try {
    await openSession(ERROR_RETRIES);
    await refreshThreads();
    await startShellStream();
  } catch (error) {
    // Not fatal: ctrl-r retries. Say so rather than leaving a dead screen.
    apply(withStatus(state, `${String(error)} — press ctrl-r to retry`));
  }
}
