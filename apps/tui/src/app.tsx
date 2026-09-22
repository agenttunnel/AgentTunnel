/** @jsxImportSource @opentui/solid */
/**
 * `t3-tui` — AgentTunnel in a terminal.
 *
 * The UI follows opencode's terminal client: OpenTUI + Solid rendered through
 * `@opentui/solid`'s JSX runtime (registered in `bunfig.toml`), composed as a
 * header row, a scrolling body, the composer, and a footer of key hints. The
 * data layer is the one the web clients already use — the shell and thread
 * subscriptions, `server.getConfig` for providers and settings, and the same
 * `thread.turn.start` command with `bootstrap.createThread` for a new thread.
 *
 *   bun src/index.tsx --url http://localhost:13773 [--token <session token>]
 *   bun src/index.tsx --demo
 */
import type { ServerSettings } from "@t3tools/contracts";
import * as DateTime from "effect/DateTime";
import * as Effect from "effect/Effect";
import * as Fiber from "effect/Fiber";
import * as Stream from "effect/Stream";
import * as Random from "effect/Random";
import * as Scope from "effect/Scope";
import { remoteHttpClientLayer } from "@t3tools/client-runtime/rpc";
import { foldAgentLogEntries } from "@t3tools/client-runtime/state/agentLog";
import { render, useKeyboard, useTerminalDimensions } from "@opentui/solid";
import { For, Index, Show, createMemo, createSignal, onMount } from "solid-js";

import {
  agentTunnelCatalog,
  projectRows,
  providerRows,
  serverSettingRows,
  threadRows,
  threadView,
  type TuiLine,
  type TuiProjectRow,
  type TuiThreadRow,
} from "./format.ts";
import { buildAgentTunnelPatch, resolveAgentTunnelConfig } from "./agentTunnel.ts";
import { loadPreferences, savePreferences, type TuiPreferences } from "./preferences.ts";
import {
  connect,
  getAgentOutput,
  interruptTurn,
  shellStream,
  threadStream,
  loadServerConfig,
  loadShell,
  loadThread,
  resolveConnection,
  resolveSocketTicket,
  sendTurn,
  startThread,
  updateServerSettings,
  TuiSessionError,
  type TuiSession,
} from "./session.ts";
import {
  activeProvider,
  commandSuggestions,
  highlightedSuggestion,
  initialTuiState,
  reduceKey,
  resolveModelSelection,
  selectedThread,
  settingsRows,
  agentTunnelScreenRows,
  withAgentLog,
  withCatalogs,
  withPreferences,
  withShellItem,
  withThreadEvent,
  withThreadSnapshot,
  withStatus,
  withThread,
  withThreads,
  type TuiEffect,
  type TuiState,
} from "./state.ts";

/**
 * Rows are rendered by position, so each one is padded to the longest line:
 * a shorter line would otherwise leave the previous frame's characters behind.
 * The cursor row is marked here, which is also what colours it.
 */
/**
 * A window of `limit` rows that always contains `index`. Lists longer than the
 * terminal are clipped to it: an overflowing box paints over the palette and
 * the composer instead of scrolling.
 */
function windowRows<A>(rows: ReadonlyArray<A>, index: number, limit: number): ReadonlyArray<A> {
  if (rows.length <= limit) return rows;
  const start = Math.min(Math.max(0, index - Math.floor(limit / 2)), rows.length - limit);
  return rows.slice(start, start + limit);
}

/**
 * Rows are written over the previous frame, so every one is padded to the full
 * terminal width and lists always emit the same number of rows. Anything less
 * and the renderer leaves the old characters behind — the palette ends up with
 * two frames' text interleaved on one line.
 */
function padRows(
  lines: ReadonlyArray<string>,
  count: number,
  width: number,
): ReadonlyArray<string> {
  // Truncate as well as pad: a row wider than the box wraps onto the next line,
  // which pushes every row below it and paints over them.
  return Array.from({ length: Math.max(count, lines.length) }, (_, index) => {
    const line = lines[index] ?? "";
    const clipped = line.length <= width ? line : `${line.slice(0, width - 1)}…`;
    return clipped.padEnd(width);
  });
}

/** How many picker rows are on screen before the list scrolls around the cursor. */
const PICKER_ROWS = 12;

/** Every command fits; the palette reserves this many rows so redraws are clean. */
const MAX_COMMANDS = 14;

/** OpenTUI takes hex colors; these mirror the app's palette. */
const theme = {
  background: "#0b0b0f",
  text: "#e6e6ef",
  textMuted: "#8b8b9e",
  textFaint: "#5a5a6b",
  primary: "#7aa2f7",
  surface: "#14141c",
  border: "#2a2a38",
} as const;

const USAGE = `t3-tui — AgentTunnel in a terminal

Usage: bun src/index.tsx --url <http://host:port> [--token <session token>]
       bun src/index.tsx --demo

  --url    AgentTunnel server origin (default: $T3CODE_URL)
  --token  session credential, exchanged for the WebSocket ticket (default: $T3CODE_TOKEN)
  --demo   render canned data without connecting
  --help   this message

Keys:  ↑/↓ move · enter open or send · / commands · ctrl-p palette
       ctrl-i interrupt · ctrl-r reload · esc back · ctrl-c quit
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

const DEMO_THREADS: ReadonlyArray<TuiThreadRow> = [
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
  {
    id: "demo-3",
    title: "Depot CI migration",
    projectTitle: "AgentTunnel",
    branch: null,
    fleet: null,
  },
];

const DEMO_PROJECTS: ReadonlyArray<TuiProjectRow> = [
  {
    id: "demo-project",
    title: "AgentTunnel",
    workspaceRoot: "~/projects/agenttunnel",
    defaultModelSelection: null,
  },
];

const DEMO_THREAD = {
  id: "demo-1",
  title: "Router state design doc",
  recap: "Goal: finish the design doc  ·  2 agents working · executor: wiring the row",
  lines: [
    { kind: "user" as const, text: "Write the design doc for router state." },
    { kind: "assistant" as const, text: "Splitting into recon, draft, and review pipelines." },
    { kind: "agent" as const, text: "planner · completed — outline recorded in the notebook" },
    { kind: "agent" as const, text: "executor · running — wiring the row" },
  ],
};

/**
 * One log read, flattened for a terminal: markdown keeps its text, tool calls
 * become a single line, and the raw/error blocks are labelled.
 */
const readAgentLogLines = async (
  session: TuiSession,
  outputPath: string,
): Promise<ReadonlyArray<string>> => {
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
};

/** Thread ids are opaque to the server and unique per process. */
const newThreadId = (): Promise<string> =>
  Effect.runPromise(
    Effect.all([Random.nextInt, Random.nextInt]).pipe(
      Effect.map(([seed, salt]) => `tui-${seed.toString(36)}-${salt.toString(36)}`),
    ),
  );

const lineColor = (kind: string): string =>
  kind === "user" ? theme.primary : kind === "agent" ? theme.textFaint : theme.text;

function App(props: { readonly args: ReturnType<typeof parseArgs> }) {
  const [state, setState] = createSignal<TuiState>(
    props.args.demo
      ? withCatalogs(
          withThread(
            withThreads(withStatus(initialTuiState, "demo — not connected"), DEMO_THREADS),
            DEMO_THREAD,
          ),
          { projects: DEMO_PROJECTS },
        )
      : withStatus(initialTuiState, `connecting to ${props.args.url ?? ""}…`),
  );
  let session: TuiSession | null = null;
  let scope: Scope.Scope | null = null;
  let threadFiber: Fiber.Fiber<unknown, unknown> | null = null;
  let shellFiber: Fiber.Fiber<unknown, unknown> | null = null;
  let agentLogFiber: Fiber.Fiber<unknown, unknown> | null = null;
  // The last settings snapshot: patches replace whole maps, so they are built
  // from what the server last told us, not from a guess.
  let settings: ServerSettings | null = null;

  // Solid's setter returns the stored value; the app treats it as a void write.
  const apply = (next: TuiState): void => {
    setState(next);
  };

  /** A load that fails has to say so in the status line, not stall the view. */
  const guard = async (label: string, run: () => Promise<void>): Promise<void> => {
    try {
      await run();
    } catch (error) {
      apply(withStatus(state(), `${label} failed: ${String(error)}`));
    }
  };

  /** The thread list plus the catalogs the palette and settings read from. */
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
            withStatus(state(), `${snapshot.threads.length} threads`),
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

  const stopThreadStream = async (): Promise<void> => {
    const fiber = threadFiber;
    threadFiber = null;
    if (fiber !== null) await Effect.runPromise(Fiber.interrupt(fiber));
  };

  /** Follow one thread: the snapshot arrives first, then every change to it. */
  const openThread = (threadId: string): Promise<void> =>
    guard("open", async () => {
      if (session === null || scope === null) return;
      apply(withStatus(state(), "loading thread…"));
      const active = session;
      await stopThreadStream();
      // Forked, not scoped: the subscription outlives this call and is stopped
      // by interrupting its fiber.
      threadFiber = Effect.runFork(
        threadStream(active, { threadId, afterSequence: state().lastThreadSequence }).pipe(
          Stream.runForEach((item) =>
            Effect.sync(() => {
              if (item.kind === "snapshot") apply(withThreadSnapshot(state(), item.snapshot));
              else if (item.kind === "event") apply(withThreadEvent(state(), item.event));
            }),
          ),
          Effect.catch((error) =>
            Effect.sync(() => {
              apply(withStatus(state(), `live updates stopped: ${error.message}`));
              void reconnect();
            }),
          ),
        ),
      );
    });

  /** Keep the thread list live, and refresh the catalogs whenever it settles. */
  const startShellStream = async (): Promise<void> => {
    if (session === null) return;
    const active = session;
    if (shellFiber !== null) await Effect.runPromise(Fiber.interrupt(shellFiber));
    shellFiber = Effect.runFork(
      shellStream(active, { afterSequence: state().lastShellSequence }).pipe(
        Stream.runForEach((item) => Effect.sync(() => apply(withShellItem(state(), item)))),
        Effect.catch((error) =>
          Effect.sync(() => apply(withStatus(state(), `list updates stopped: ${error.message}`))),
        ),
      ),
    );
  };

  const send = async (threadId: string, text: string): Promise<void> => {
    if (session === null) return;
    const selection = state().modelSelection;
    await guard("send", async () => {
      await Effect.runPromise(
        sendTurn(session as TuiSession, {
          threadId,
          text,
          ...(selection === null ? {} : { modelSelection: selection }),
        }),
      );
      apply(withStatus(state(), "sent — refreshing"));
      await openThread(threadId);
    });
  };

  /** A new thread is created by the first turn: one command, thread included. */
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
        apply(withStatus(state(), "no provider configured — run /provider"));
        return;
      }
      const threadId = await newThreadId();
      await Effect.runPromise(
        startThread(session, { threadId, projectId, title, text, modelSelection }),
      );
      apply(withStatus(state(), "created — loading"));
      await openThread(threadId);
    });

  const stopTurn = async (threadId: string): Promise<void> => {
    if (session === null) return;
    await guard("interrupt", async () => {
      await Effect.runPromise(interruptTurn(session as TuiSession, threadId));
      apply(withStatus(state(), "interrupt requested"));
    });
  };

  /** The log is a unary read; the desktop polls it, and so does the TUI. */
  const pollAgentLog = (agentId: string, label: string, outputPath: string): void => {
    void stopAgentLog();
    const active = session;
    if (active === null) return;
    const polling = Effect.gen(function* () {
      while (state().view === "agentLog") {
        yield* Effect.tryPromise({
          try: async () => {
            const lines = await readAgentLogLines(active, outputPath);
            apply(withAgentLog(state(), { agentId, label, lines, error: null }));
          },
          catch: (error) => new TuiSessionError({ message: String(error) }),
        }).pipe(
          Effect.catch((error) =>
            Effect.sync(() =>
              apply(withAgentLog(state(), { agentId, label, lines: [], error: String(error) })),
            ),
          ),
        );
        yield* Effect.sleep("1 second");
      }
    });
    agentLogFiber = Effect.runFork(polling);
  };

  const stopAgentLog = async (): Promise<void> => {
    const fiber = agentLogFiber;
    agentLogFiber = null;
    if (fiber !== null) await Effect.runPromise(Fiber.interrupt(fiber));
  };

  const persistPreferences = async (patch: Partial<TuiPreferences>): Promise<void> => {
    const next = { ...state().preferences, ...patch };
    await guard("settings", async () => {
      await savePreferences(next);
      apply(withStatus(state(), "settings saved"));
    });
  };

  /** One field of the workflow provider's settings, patched the way the web does. */
  const persistAgentTunnelSetting = async (
    field: string,
    value: string | number | boolean,
  ): Promise<void> => {
    if (session === null) return;
    await guard("settings", async () => {
      const patch =
        settings === null
          ? { providers: { agenttunnel: { [field]: value } } }
          : buildAgentTunnelPatch(settings, field, value);
      await Effect.runPromise(updateServerSettings(session as TuiSession, patch));
      apply(withStatus(state(), "workflow settings saved"));
      await refreshThreads();
    });
  };

  const persistServerSetting = async (key: string, value: boolean | string): Promise<void> => {
    if (session === null) return;
    await guard("settings", async () => {
      await Effect.runPromise(updateServerSettings(session as TuiSession, { [key]: value }));
      apply(withStatus(state(), "server setting saved"));
      await refreshThreads();
    });
  };

  /** A dead socket is rebuilt, then everything resubscribes from its cursor. */
  const reconnect = async (): Promise<void> => {
    const url = props.args.url;
    if (url === null || props.args.demo) return;
    await guard("reconnect", async () => {
      await stopThreadStream();
      session = null;
      scope = await Effect.runPromise(Scope.make());
      const ticket = await Effect.runPromise(
        resolveSocketTicket({
          httpBaseUrl: url.replace(/\/+$/u, ""),
          token: props.args.token,
        }).pipe(Effect.provide(remoteHttpClientLayer(fetch))),
      );
      session = await Effect.runPromise(
        connect(resolveConnection({ url, ticket: ticket ?? undefined })).pipe(
          Effect.provideService(Scope.Scope, scope),
        ),
      );
      apply(withStatus(state(), "reconnected"));
      await startShellStream();
      const openId = state().openThreadId;
      if (openId !== null) await openThread(openId);
    });
  };

  const perform = (effect: TuiEffect, previous: TuiState): void => {
    const threadId = previous.openThreadId;
    switch (effect?.kind) {
      case "quit":
        process.exit(0);
        return;
      case "refresh":
        void (previous.view === "threads" || previous.view === "settings"
          ? refreshThreads()
          : threadId === null
            ? Promise.resolve()
            : openThread(threadId));
        return;
      case "open": {
        const row = selectedThread(previous);
        if (row !== null) void openThread(row.id);
        return;
      }
      case "send":
        if (threadId === null || props.args.demo) return;
        void send(threadId, effect.text);
        return;
      case "createThread":
        if (props.args.demo) return;
        void createThread(previous, effect.projectId, effect.title, effect.text);
        return;
      case "interrupt":
        if (threadId === null || props.args.demo) return;
        void stopTurn(threadId);
        return;
      case "savePreferences":
        if (props.args.demo) return;
        void persistPreferences(effect.patch);
        return;
      case "saveServerSetting":
        if (props.args.demo) return;
        void persistServerSetting(effect.key, effect.value);
        return;
      case "saveAgentTunnelSetting":
        if (props.args.demo) return;
        void persistAgentTunnelSetting(effect.field, effect.value);
        return;
      case "openAgentLog":
        if (props.args.demo) return;
        pollAgentLog(effect.agentId, effect.label, effect.outputPath);
        return;
      default:
        return;
    }
  };

  useKeyboard((event) => {
    const key = event as { readonly name?: string; readonly sequence?: string };
    const before = state();
    const { state: next, effect } = reduceKey(before, {
      ...(key.name === undefined ? {} : { name: key.name }),
      ...(key.sequence === undefined ? {} : { text: key.sequence }),
    });
    if (next !== before) apply(next);
    perform(effect, before);
  });

  onMount(() => {
    if (props.args.demo) return;
    const url = props.args.url;
    if (url === null) {
      apply(withStatus(state(), "--url is required (or set T3CODE_URL); try --demo"));
      return;
    }
    void (async () => {
      try {
        const preferences = await loadPreferences();
        apply(withPreferences(state(), preferences));
      } catch (error) {
        apply(withStatus(state(), `settings not loaded: ${String(error)}`));
      }
      try {
        // The upgrade wants a ticket minted for it, not the session credential.
        const httpBaseUrl = url.replace(/\/+$/u, "");
        apply(withStatus(state(), "authenticating…"));
        const ticket = await Effect.runPromise(
          resolveSocketTicket({ httpBaseUrl, token: props.args.token }).pipe(
            Effect.provide(remoteHttpClientLayer(fetch)),
          ),
        );
        // One session for the process: the socket scope lives until the TUI exits.
        scope = await Effect.runPromise(Scope.make());
        session = await Effect.runPromise(
          connect(resolveConnection({ url, ticket: ticket ?? undefined })).pipe(
            Effect.provideService(Scope.Scope, scope),
          ),
        );
        await refreshThreads();
        await startShellStream();
      } catch (error) {
        apply(withStatus(state(), `could not connect: ${String(error)}`));
      }
    })();
  });

  const header = createMemo(() => {
    const current = state();
    if (current.view === "threads") return `AgentTunnel · threads (${current.threads.length})`;
    if (current.view === "settings") return "AgentTunnel · settings";
    if (current.view === "agenttunnel") return "AgentTunnel · workflow roles";
    if (current.view === "agents") return `AgentTunnel · agents (${current.agents.length})`;
    if (current.view === "agentLog") {
      return `AgentTunnel · ${current.agentLog?.label ?? "agent"} · log`;
    }
    if (current.view === "usage") return "AgentTunnel · token usage";
    return `AgentTunnel · ${current.title}`;
  });

  const hints = createMemo(() => {
    const current = state();
    if (current.view === "settings") return "↑/↓ move · enter change · esc back · ctrl-c quit";
    if (current.view === "agenttunnel") {
      return "↑/↓ move · enter change · esc back · ctrl-r reload · ctrl-c quit";
    }
    if (current.view === "agents") {
      return "↑/↓ move · enter open log · x stop turn · esc back · ctrl-r reload";
    }
    if (current.view === "agentLog") return "esc back to agents · ctrl-r refresh · ctrl-c quit";
    if (current.view === "usage") return "esc back · ctrl-c quit";
    if (current.view === "threads") {
      return "↑/↓ move · enter open · / new thread · ctrl-p commands · ctrl-r reload · ctrl-c quit";
    }
    return "enter send · / commands · ctrl-i interrupt · esc back · ctrl-r reload · ctrl-c quit";
  });

  const dimensions = useTerminalDimensions();
  /**
   * Row width, in two sizes: a plain list sits inside the body's padding, while
   * the palette and the picker add their own padding and a border. A row wider
   * than its box wraps, and a wrapped row paints over the one below it.
   */
  const rowWidth = createMemo(() => Math.max(20, dimensions().width - 5));
  const insetRowWidth = createMemo(() => Math.max(20, dimensions().width - 9));
  /** The palette's own rows, when it is showing. */
  const paletteRows = createMemo(() => {
    const count = commandSuggestions(state()).length;
    return count === 0 ? 0 : Math.min(count, MAX_COMMANDS);
  });

  /**
   * Rows left for a list. The header, composer, hints, and palette all draw
   * outside it, and a list that overflows its box paints over them instead of
   * being clipped — which is what the overlapping rows were.
   */
  const listRows = createMemo(() => Math.max(4, dimensions().height - 6 - paletteRows()));

  // Every command is offered; the transcript above gives up the rows for it.
  const suggestions = createMemo(() => {
    const rows = commandSuggestions(state());
    const highlighted = highlightedSuggestion(state())?.id;
    return padRows(
      rows.map((row) => `${row.id === highlighted ? "▸ " : "  "}${row.label}  ${row.detail ?? ""}`),
      MAX_COMMANDS,
      insetRowWidth(),
    );
  });
  const picker = createMemo(() => state().picker);
  /** The row the picker has highlighted, by id, so a windowed list still marks it. */
  const pickerSelection = createMemo(() => {
    const current = picker();
    if (current === null) return null;
    const query = current.query.trim().toLowerCase();
    const matches =
      query.length === 0
        ? current.items
        : current.items.filter((item) => item.label.toLowerCase().includes(query));
    return matches[current.index] ?? null;
  });
  /** Picker rows, windowed around the cursor so the selection is always on screen. */
  const pickerRows = createMemo(() => {
    const current = picker();
    if (current === null) return [];
    const query = current.query.trim().toLowerCase();
    const matches =
      query.length === 0
        ? current.items
        : current.items.filter((item) => item.label.toLowerCase().includes(query));
    if (matches.length <= PICKER_ROWS) return matches;
    const start = Math.min(
      Math.max(0, current.index - Math.floor(PICKER_ROWS / 2)),
      matches.length - PICKER_ROWS,
    );
    return matches.slice(start, start + PICKER_ROWS);
  });
  /** Agent rows, two lines each, windowed to the terminal. */
  const agentLines = createMemo(() => {
    const limit = Math.max(1, Math.floor(listRows() / 2));
    const rows = windowRows(state().agents, state().agentIndex, limit);
    const offset =
      state().agents.length <= limit
        ? 0
        : Math.max(
            0,
            Math.min(state().agentIndex - Math.floor(limit / 2), state().agents.length - limit),
          );
    const lines = rows.flatMap((agent, index) => {
      const selected = offset + index === state().agentIndex;
      const meta = [
        agent.model,
        agent.tokens === null ? null : `${agent.tokens} tokens`,
        agent.tools === null ? null : `${agent.tools} tools`,
        agent.elapsed,
        agent.attempt === null ? null : `run ${agent.attempt}`,
      ]
        .filter((part): part is string => part !== null && part.length > 0)
        .join(" · ");
      return [
        `${selected ? "▸ " : "  "}${agent.label} · ${agent.status}${
          agent.activity.length === 0 ? "" : ` — ${agent.activity}`
        }`,
        `  ${meta}`,
      ];
    });
    return padRows(lines, limit * 2, rowWidth());
  });

  /** Thread rows, windowed to the terminal and padded so redraws are clean. */
  const threadLines = createMemo(() => {
    const rows = windowRows(state().threads, state().selectedIndex, listRows());
    const offset =
      state().threads.length - rows.length === 0
        ? 0
        : Math.max(
            0,
            Math.min(
              state().selectedIndex - Math.floor(listRows() / 2),
              state().threads.length - listRows(),
            ),
          );
    return padRows(
      rows.map((row, index) => {
        const selected = offset + index === state().selectedIndex;
        return `${selected ? "▸ " : "  "}${row.title}${row.branch === null ? "" : ` · ${row.branch}`}  (${row.projectTitle})`;
      }),
      listRows(),
      rowWidth(),
    );
  });

  /** Picker lines, padded to a common width for the same reason. */
  const pickerLines = createMemo(() =>
    padRows(
      pickerRows().map(
        (item) =>
          `${item.id === pickerSelection()?.id ? "▸ " : "  "}${item.label}${
            item.detail === null ? "" : `  ${item.detail}`
          }`,
      ),
      PICKER_ROWS,
      insetRowWidth(),
    ),
  );

  return (
    <box flexDirection="column" flexGrow={1} backgroundColor={theme.background}>
      <box
        height={1}
        flexDirection="row"
        justifyContent="space-between"
        paddingLeft={2}
        paddingRight={2}
      >
        <text fg={theme.text}>{header()}</text>
        <text fg={theme.textMuted}>{state().status}</text>
      </box>
      <box
        height={listRows()}
        minHeight={0}
        paddingLeft={2}
        paddingRight={2}
        flexDirection="column"
      >
        <Show when={state().view === "threads"}>
          <Show
            when={state().threads.length > 0}
            fallback={<text fg={theme.textMuted}>no threads yet — type /new to start one</text>}
          >
            <Index each={threadLines()}>
              {(line) => (
                <text fg={line().startsWith("▸") ? theme.primary : theme.text}>{line()}</text>
              )}
            </Index>
          </Show>
        </Show>
        <Show when={state().view === "thread"}>
          <scrollbox height={listRows()} minHeight={0}>
            <For each={state().lines}>
              {(line: TuiLine) => <text fg={lineColor(line.kind)}>{line.text}</text>}
            </For>
          </scrollbox>
        </Show>
        <Show when={state().view === "agents"}>
          <Show
            when={state().agents.length > 0}
            fallback={<text fg={theme.textMuted}>no sub-agents yet for this thread</text>}
          >
            <Index each={agentLines()}>
              {(line) => (
                <text fg={line().startsWith("▸") ? theme.primary : theme.text}>{line()}</text>
              )}
            </Index>
          </Show>
        </Show>
        <Show when={state().view === "agentLog"}>
          <scrollbox height={listRows()} minHeight={0}>
            <For each={state().agentLog?.lines ?? []}>
              {(line) => (
                <text fg={line.startsWith("#") ? theme.textMuted : theme.text}>{line}</text>
              )}
            </For>
          </scrollbox>
        </Show>
        <Show when={state().view === "usage"}>
          <box flexDirection="column">
            <For each={state().usageLines}>{(line) => <text fg={theme.text}>{line}</text>}</For>
          </box>
        </Show>
        <Show when={state().view === "agenttunnel"}>
          <box flexDirection="column">
            <For each={agentTunnelScreenRows(state())}>
              {(row, index) => (
                <text fg={index() === state().agentTunnelIndex ? theme.primary : theme.text}>
                  {`${index() === state().agentTunnelIndex ? "▸ " : "  "}${row.label}  ${row.value}`}
                </text>
              )}
            </For>
          </box>
        </Show>
        <Show when={state().view === "settings"}>
          <box flexDirection="column">
            <For each={settingsRows(state())}>
              {(row, index) => (
                <text fg={index() === state().settingsIndex ? theme.primary : theme.text}>
                  {`${index() === state().settingsIndex ? "▸ " : "  "}${row.label}  ${row.value}`}
                </text>
              )}
            </For>
          </box>
        </Show>
      </box>
      <Show when={state().draft !== null}>
        <box paddingLeft={2} paddingRight={2}>
          <text fg={theme.textMuted}>
            {`new thread in ${state().draft?.projectTitle ?? ""} — type the first message`}
          </text>
        </box>
      </Show>
      <Show when={state().recap !== null}>
        <box paddingLeft={2} paddingRight={2}>
          <text fg={theme.textMuted}>{`※ recap: ${state().recap ?? ""}`}</text>
        </box>
      </Show>
      <Show when={picker() !== null}>
        <box
          flexDirection="column"
          height={pickerLines().length + 1}
          borderStyle="rounded"
          borderColor={theme.border}
          backgroundColor={theme.surface}
          paddingLeft={1}
          paddingRight={1}
        >
          <text
            fg={theme.textMuted}
          >{`${picker()?.title ?? ""}${picker()?.query === "" ? "" : ` · ${picker()?.query ?? ""}`}`}</text>
          <Index each={pickerLines()}>
            {(line) => (
              <text fg={line().startsWith("▸") ? theme.primary : theme.text}>{line()}</text>
            )}
          </Index>
        </box>
      </Show>
      <Show when={state().picker === null && suggestions().length > 0}>
        <box flexDirection="column" height={suggestions().length} paddingLeft={2} paddingRight={2}>
          <Index each={suggestions()}>
            {(line) => (
              <text fg={line().startsWith("▸") ? theme.primary : theme.textFaint}>{line()}</text>
            )}
          </Index>
        </box>
      </Show>
      <Show when={state().view === "thread" || state().input.length > 0}>
        <box paddingLeft={2} paddingRight={2}>
          <text fg={theme.text}>{`❯ ${state().input}`}</text>
        </box>
      </Show>
      <box height={1} paddingLeft={2} paddingRight={2}>
        <text fg={theme.textFaint}>{hints()}</text>
      </box>
    </box>
  );
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
  await render(() => <App args={args} />);
}
