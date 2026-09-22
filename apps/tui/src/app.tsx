/**
 * `t3-tui` — AgentTunnel in a terminal.
 *
 * The UI follows opencode's terminal client: OpenTUI + Solid rendered through
 * `@opentui/solid`'s JSX runtime (registered in `bunfig.toml`), composed as a
 * header row, a scrolling body, an optional recap line, the composer, and a
 * footer of key hints. The data layer is the one the web clients already use —
 * the shell and thread subscriptions from client-runtime — folded into app
 * state by `state.ts` / `format.ts`, which stay pure and tested.
 *
 *   bun src/index.tsx --url http://localhost:13773 [--token <wsTicket>]
 *   bun src/index.tsx --demo
 */
import {
  CommandId,
  DEFAULT_PROVIDER_INTERACTION_MODE,
  DEFAULT_RUNTIME_MODE,
  MessageId,
  ThreadId,
} from "@t3tools/contracts";
import * as DateTime from "effect/DateTime";
import * as Effect from "effect/Effect";
import * as Random from "effect/Random";
import * as Scope from "effect/Scope";
import { render, useKeyboard } from "@opentui/solid";
import { Show, createMemo, createSignal, onMount } from "solid-js";

import { threadRows, threadView, type TuiThreadRow } from "./format.ts";
import { connect, loadShell, loadThread, resolveConnection, type TuiSession } from "./session.ts";
import {
  initialTuiState,
  reduceKey,
  selectedThread,
  withStatus,
  withThread,
  withThreads,
  type TuiEffect,
  type TuiState,
} from "./state.ts";

/** OpenTUI takes hex colors; these mirror the app's palette. */
const theme = {
  background: "#0b0b0f",
  text: "#e6e6ef",
  textMuted: "#8b8b9e",
  textFaint: "#5a5a6b",
  primary: "#7aa2f7",
} as const;

const USAGE = `t3-tui — AgentTunnel in a terminal

Usage: bun src/index.tsx --url <http://host:port> [--token <wsTicket>]
       bun src/index.tsx --demo

  --url    AgentTunnel server origin (default: $T3CODE_URL)
  --token  session credential, sent as the WebSocket ticket (default: $T3CODE_TOKEN)
  --demo   render a canned thread without connecting
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

const lineColor = (kind: string): string =>
  kind === "user" ? theme.primary : kind === "agent" ? theme.textFaint : theme.text;

/** Client-side ids: opaque to the server, unique per process. */
const newId = (seed: number, salt: number): string =>
  `tui-${seed.toString(36)}-${salt.toString(36)}`;

function App(props: { readonly args: ReturnType<typeof parseArgs> }) {
  const [state, setState] = createSignal<TuiState>(
    props.args.demo
      ? withStatus(
          withThread(withThreads(initialTuiState, DEMO_THREADS), DEMO_THREAD),
          "demo — not connected",
        )
      : withStatus(initialTuiState, `connecting to ${props.args.url ?? ""}…`),
  );
  let session: TuiSession | null = null;

  // Solid's setter returns the stored value; the app treats it as a void write.
  const apply = (next: TuiState): void => {
    setState(next);
  };

  const refreshThreads = async (): Promise<void> => {
    if (session === null) return;
    const snapshot = await Effect.runPromise(loadShell(session));
    apply(
      withThreads(withStatus(state(), `${snapshot.threads.length} threads`), threadRows(snapshot)),
    );
  };

  const openThread = async (threadId: string): Promise<void> => {
    if (session === null) return;
    const snapshot = await Effect.runPromise(loadThread(session, threadId));
    const view = threadView(snapshot);
    apply(withThread(state(), { id: threadId, ...view }));
  };

  const sendMessage = async (threadId: string, text: string): Promise<void> => {
    if (session === null) return;
    const active = session;
    await Effect.runPromise(
      Effect.gen(function* () {
        const at = DateTime.formatIso(yield* DateTime.now);
        yield* active.client["orchestration.dispatchCommand"]({
          type: "thread.turn.start",
          commandId: CommandId.make(newId(yield* Random.nextInt, yield* Random.nextInt)),
          threadId: ThreadId.make(threadId),
          message: {
            messageId: MessageId.make(newId(yield* Random.nextInt, yield* Random.nextInt)),
            role: "user",
            text,
            attachments: [],
          },
          runtimeMode: DEFAULT_RUNTIME_MODE,
          interactionMode: DEFAULT_PROVIDER_INTERACTION_MODE,
          createdAt: at,
        });
      }),
    );
    apply(withStatus(state(), "sent — refreshing"));
    await openThread(threadId);
  };

  const interrupt = async (threadId: string): Promise<void> => {
    if (session === null) return;
    const active = session;
    await Effect.runPromise(
      Effect.all([Random.nextInt, Random.nextInt, DateTime.now]).pipe(
        Effect.flatMap(([seed, salt, at]) =>
          active.client["orchestration.dispatchCommand"]({
            type: "thread.turn.interrupt",
            commandId: CommandId.make(newId(seed, salt)),
            threadId: ThreadId.make(threadId),
            createdAt: DateTime.formatIso(at),
          }),
        ),
      ),
    );
    apply(withStatus(state(), "interrupt requested"));
  };

  const perform = (effect: TuiEffect, previous: TuiState): void => {
    const threadId = previous.openThreadId;
    switch (effect?.kind) {
      case "quit":
        process.exit(0);
        return;
      case "refresh":
        void (previous.view === "threads"
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
        void sendMessage(threadId, effect.text);
        return;
      case "interrupt":
        if (threadId === null || props.args.demo) return;
        void interrupt(threadId);
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
        // One session for the process: the socket scope lives until the TUI exits.
        const scope = await Effect.runPromise(Scope.make());
        session = await Effect.runPromise(
          connect(resolveConnection({ url, token: props.args.token })).pipe(
            Effect.provideService(Scope.Scope, scope),
          ),
        );
        await refreshThreads();
      } catch (error) {
        apply(withStatus(state(), `could not connect: ${String(error)}`));
      }
    })();
  });

  const header = createMemo(() =>
    state().view === "threads"
      ? `AgentTunnel · threads (${state().threads.length})`
      : `AgentTunnel · ${state().title}`,
  );
  const hints = createMemo(() =>
    state().view === "threads"
      ? "↑/↓ move · enter open · ctrl-r refresh · ctrl-c quit"
      : "enter send · ctrl-i interrupt · esc back · ctrl-r refresh · ctrl-c quit",
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
      <box flexGrow={1} minHeight={0} paddingLeft={2} paddingRight={2} flexDirection="column">
        <Show
          when={state().view === "threads"}
          fallback={
            <scrollbox flexGrow={1} minHeight={0}>
              {state().lines.map((line) => (
                <text fg={lineColor(line.kind)}>{line.text}</text>
              ))}
            </scrollbox>
          }
        >
          <Show
            when={state().threads.length > 0}
            fallback={<text fg={theme.textMuted}>no threads yet — ctrl-r to refresh</text>}
          >
            {state().threads.map((row, index) => (
              <text fg={index === state().selectedIndex ? theme.primary : theme.text}>
                {`${index === state().selectedIndex ? "▸ " : "  "}${row.title}${
                  row.branch === null ? "" : ` · ${row.branch}`
                }  (${row.projectTitle})`}
              </text>
            ))}
          </Show>
        </Show>
      </box>
      <Show when={state().recap !== null}>
        <box paddingLeft={2} paddingRight={2}>
          <text fg={theme.textMuted}>{`※ recap: ${state().recap ?? ""}`}</text>
        </box>
      </Show>
      <Show when={state().view === "thread"}>
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
