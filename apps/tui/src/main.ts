/**
 * `t3-tui` — AgentTunnel in a terminal.
 *
 * The flow the web client offers, kept to what a terminal needs: pick a thread,
 * read it (messages, what each sub-agent did, the live recap), send a message,
 * interrupt the turn, refresh. It speaks the same WebSocket RPC the web and
 * mobile clients speak; only the rendering and the key handling are new.
 *
 *   node apps/tui/src/main.ts --url http://localhost:13773 [--token <wsTicket>]
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
import * as NodeReadline from "node:readline";

import { threadRows, threadView } from "./format.ts";
import { connect, loadShell, loadThread, resolveConnection, type TuiSession } from "./session.ts";
import {
  initialTuiState,
  reduceKey,
  selectedThread,
  withStatus,
  withThread,
  withThreads,
  type TuiState,
} from "./state.ts";
import { renderTui } from "./view.ts";

const USAGE = `t3-tui — AgentTunnel in a terminal

Usage: t3-tui --url <http://host:port> [--token <wsTicket>]

  --url    AgentTunnel server origin (default: $T3CODE_URL)
  --token  session token, sent as the WebSocket ticket (default: $T3CODE_TOKEN);
           omit it for a server started without auth
  --help   this message
`;

export function parseArgs(argv: ReadonlyArray<string>): {
  readonly url: string | null;
  readonly token: string | undefined;
  readonly help: boolean;
} {
  let url: string | null = process.env.T3CODE_URL?.trim() ?? null;
  let token = process.env.T3CODE_TOKEN?.trim() || undefined;
  let help = false;
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === "--help" || arg === "-h") help = true;
    else if (arg === "--url") url = argv[index + 1]?.trim() ?? null;
    else if (arg === "--token") token = argv[index + 1]?.trim() || undefined;
  }
  return { url: url === "" ? null : url, token, help };
}

/** Client-side id for one command: opaque to the server, unique per process. */
const newId = (seed: number, salt: number): string =>
  `tui-${seed.toString(36)}-${salt.toString(36)}`;

const paint = (state: TuiState): void => {
  const size = { columns: process.stdout.columns ?? 100, rows: process.stdout.rows ?? 30 };
  const frame = renderTui(state, size).join("\n");
  process.stdout.write(`\u001b[H\u001b[2J${frame}`);
};

const run = async (): Promise<void> => {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) {
    process.stdout.write(USAGE);
    return;
  }
  if (args.url === null) {
    process.stderr.write(`${USAGE}\n--url is required (or set T3CODE_URL).\n`);
    process.exitCode = 2;
    return;
  }

  let state = withStatus(initialTuiState, `connecting to ${args.url}…`);
  let session: TuiSession | null = null;
  paint(state);

  const setState = (next: TuiState): void => {
    state = next;
    paint(state);
  };

  const refreshThreads = async (): Promise<void> => {
    if (session === null) return;
    const snapshot = await Effect.runPromise(loadShell(session));
    setState(
      withThreads(withStatus(state, `${snapshot.threads.length} threads`), threadRows(snapshot)),
    );
  };

  const openThread = async (threadId: string): Promise<void> => {
    if (session === null) return;
    const snapshot = await Effect.runPromise(loadThread(session, threadId));
    const view = threadView(snapshot);
    setState(withThread(state, { id: threadId, ...view }));
  };

  const sendMessage = async (threadId: string, text: string): Promise<void> => {
    if (session === null) return;
    const active = session;
    await Effect.runPromise(
      Effect.gen(function* () {
        const now = DateTime.formatIso(yield* DateTime.now);
        const commandId = CommandId.make(newId(yield* Random.nextInt, yield* Random.nextInt));
        const messageId = MessageId.make(newId(yield* Random.nextInt, yield* Random.nextInt));
        yield* active.client["orchestration.dispatchCommand"]({
          type: "thread.turn.start",
          commandId,
          threadId: ThreadId.make(threadId),
          message: { messageId, role: "user", text, attachments: [] },
          runtimeMode: DEFAULT_RUNTIME_MODE,
          interactionMode: DEFAULT_PROVIDER_INTERACTION_MODE,
          createdAt: now,
        });
      }),
    );
    setState(withStatus(state, "sent — refreshing"));
    await openThread(threadId);
  };

  const interrupt = async (threadId: string): Promise<void> => {
    if (session === null) return;
    const active = session;
    await Effect.runPromise(
      Effect.gen(function* () {
        yield* active.client["orchestration.dispatchCommand"]({
          type: "thread.turn.interrupt",
          commandId: CommandId.make(newId(yield* Random.nextInt, yield* Random.nextInt)),
          threadId: ThreadId.make(threadId),
          createdAt: DateTime.formatIso(yield* DateTime.now),
        });
      }),
    );
    setState(withStatus(state, "interrupt requested"));
  };

  const finish = (code: number): void => {
    process.stdout.write("\u001b[2J\u001b[H");
    if (process.stdin.isTTY) process.stdin.setRawMode(false);
    process.stdin.pause();
    process.exitCode = code;
  };

  try {
    // One session for the process: the socket scope is released when the TUI exits.
    const scope = await Effect.runPromise(Scope.make());
    session = await Effect.runPromise(
      connect(resolveConnection({ url: args.url, token: args.token })).pipe(
        Effect.provideService(Scope.Scope, scope),
      ),
    );
    await refreshThreads();
  } catch (error) {
    setState(withStatus(state, `could not connect: ${String(error)}`));
    finish(1);
    return;
  }

  NodeReadline.emitKeypressEvents(process.stdin);
  if (process.stdin.isTTY) process.stdin.setRawMode(true);
  process.stdin.resume();
  process.stdin.on("keypress", (_input, key) => {
    const { state: next, effect } = reduceKey(state, {
      name: key?.name,
      text: key?.sequence,
    });
    setState(next);
    const threadId = next.openThreadId;
    void (async () => {
      try {
        switch (effect?.kind) {
          case "quit":
            finish(0);
            return;
          case "refresh":
            if (next.view === "threads") await refreshThreads();
            else if (threadId !== null) await openThread(threadId);
            return;
          case "open": {
            const selected = selectedThread(next);
            if (selected !== null) await openThread(selected.id);
            return;
          }
          case "send":
            if (threadId !== null) await sendMessage(threadId, effect.text);
            return;
          case "interrupt":
            if (threadId !== null) await interrupt(threadId);
            return;
          default:
            return;
        }
      } catch (error) {
        setState(withStatus(state, `${effect?.kind ?? "action"} failed: ${String(error)}`));
      }
    })();
  });
};

await run();
