import {
  CommandId,
  DEFAULT_PROVIDER_INTERACTION_MODE,
  DEFAULT_RUNTIME_MODE,
  MessageId,
  ORCHESTRATION_WS_METHODS,
  ProjectId,
  ThreadId,
  WS_METHODS,
  type ModelSelection,
  type OrchestrationGetAgentOutputResult,
  type OrchestrationShellSnapshot,
  type OrchestrationShellStreamItem,
  type OrchestrationThreadDetailSnapshot,
  type OrchestrationThreadStreamItem,
  type ServerConfig,
  type ServerSettings,
  type ServerSettingsPatch,
} from "@t3tools/contracts";
import * as DateTime from "effect/DateTime";
import * as Effect from "effect/Effect";
import * as Random from "effect/Random";
import * as HttpClient from "effect/unstable/http/HttpClient";
import * as Layer from "effect/Layer";
import * as RpcClient from "effect/unstable/rpc/RpcClient";
import * as RpcSerialization from "effect/unstable/rpc/RpcSerialization";
import * as Socket from "effect/unstable/socket/Socket";
import * as Stream from "effect/Stream";
import * as Schema from "effect/Schema";
import type * as Scope from "effect/Scope";

import { issueRemoteWebSocketTicket } from "@t3tools/client-runtime/authorization";
import { makeWsRpcProtocolClient, type WsRpcProtocolClient } from "@t3tools/client-runtime/rpc";

/**
 * The TUI's connection to one AgentTunnel server: the same WebSocket RPC the web
 * and mobile clients use, wired directly instead of through the connection
 * supervisor, which owns credentials, relay, and reconnect policy the TUI does
 * not have yet. `--token` is sent as the `wsTicket` the server already accepts;
 * a server started without auth needs no token.
 */

/** Anything that stops the TUI from reading a snapshot. */
export class TuiSessionError extends Schema.TaggedErrorClass<TuiSessionError>()("TuiSessionError", {
  message: Schema.String,
}) {}

export interface TuiSession {
  readonly client: WsRpcProtocolClient;
}

export interface TuiConnection {
  readonly httpBaseUrl: string;
  readonly socketUrl: string;
}

/** `http://host:port` (or `https://…`) plus an optional ticket → the WS URL. */
export function resolveConnection(input: {
  readonly url: string;
  readonly ticket?: string | undefined;
}): TuiConnection {
  const httpBaseUrl = input.url.replace(/\/+$/u, "");
  const socketUrl = new URL(`${httpBaseUrl.replace(/^http/u, "ws")}/ws`);
  if (input.ticket !== undefined && input.ticket.trim().length > 0) {
    socketUrl.searchParams.set("wsTicket", input.ticket.trim());
  }
  return { httpBaseUrl, socketUrl: socketUrl.toString() };
}

/**
 * Exchange a session credential for the short-lived ticket the WebSocket
 * upgrade expects. The upgrade does not accept a session token directly: the
 * server mints `kind: "websocket"` tickets at this endpoint, and that ticket is
 * what `?wsTicket=` verifies. A server that needs no authentication needs no
 * token either, and the connection is made without a ticket.
 */
export const connect = (
  connection: TuiConnection,
): Effect.Effect<TuiSession, TuiSessionError, Scope.Scope> =>
  Effect.gen(function* () {
    const protocolLayer = Layer.effect(
      RpcClient.Protocol,
      RpcClient.makeProtocolSocket({ retryTransientErrors: false }),
    ).pipe(
      Layer.provide(
        Layer.mergeAll(
          Socket.layerWebSocket(connection.socketUrl, { openTimeout: "15 seconds" }).pipe(
            Layer.provide(Socket.layerWebSocketConstructorGlobal),
          ),
          RpcSerialization.layerJson,
        ),
      ),
    );
    const protocolContext = yield* Layer.build(protocolLayer);
    const client = yield* makeWsRpcProtocolClient.pipe(
      Effect.provide(protocolContext),
      Effect.mapError((cause) => new TuiSessionError({ message: String(cause) })),
    );
    return { client } satisfies TuiSession;
  });

export const resolveSocketTicket = (input: {
  readonly httpBaseUrl: string;
  readonly token?: string | undefined;
}): Effect.Effect<string | null, TuiSessionError, HttpClient.HttpClient> => {
  const token = input.token?.trim() ?? "";
  if (token.length === 0) return Effect.succeed(null);
  // The same helper the remote/web clients use: the upgrade verifies a ticket
  // minted for it, not the session credential itself.
  return issueRemoteWebSocketTicket({ httpBaseUrl: input.httpBaseUrl, bearerToken: token }).pipe(
    Effect.map((issued) => issued.ticket),
    Effect.mapError(
      (cause) =>
        new TuiSessionError({
          message: `the server refused the credential (${String(cause)}). Mint one with \`t3 auth session issue --token-only\` against the same data directory.`,
        }),
    ),
  );
};
/**
 * The first frame a subscription sends that the caller recognises, or a session
 * error. Subscriptions stream a snapshot and then incremental events; the TUI
 * reads the snapshot and leaves the deltas for a later pass.
 */
const firstFrame = <A, B extends A>(
  stream: Stream.Stream<A, unknown>,
  wanted: (item: A) => item is B,
): Effect.Effect<B, TuiSessionError> =>
  stream.pipe(
    // The subscription never ends, so collecting it would wait forever: take
    // frames until the snapshot the caller wants, then stop reading.
    Stream.takeUntil(wanted),
    Stream.runCollect,
    Effect.map((chunk) => [...chunk].find(wanted)),
    Effect.mapError((cause) => new TuiSessionError({ message: String(cause) })),
    Effect.flatMap((found) =>
      found === undefined
        ? Effect.fail(new TuiSessionError({ message: "the server sent no snapshot" }))
        : Effect.succeed(found),
    ),
  );

/** The shell snapshot the thread list renders. */
export const loadShell = (
  session: TuiSession,
): Effect.Effect<OrchestrationShellSnapshot, TuiSessionError> =>
  firstFrame(
    session.client[ORCHESTRATION_WS_METHODS.subscribeShell]({}) as Stream.Stream<
      OrchestrationShellStreamItem,
      unknown
    >,
    (item): item is Extract<OrchestrationShellStreamItem, { kind: "snapshot" }> =>
      item.kind === "snapshot",
  ).pipe(Effect.map((frame) => frame.snapshot));

/** One thread's messages and activities. */
export const loadThread = (
  session: TuiSession,
  threadId: string,
): Effect.Effect<OrchestrationThreadDetailSnapshot, TuiSessionError> =>
  firstFrame(
    session.client[ORCHESTRATION_WS_METHODS.subscribeThread]({
      threadId: ThreadId.make(threadId),
    }) as Stream.Stream<OrchestrationThreadStreamItem, unknown>,
    (item): item is Extract<OrchestrationThreadStreamItem, { kind: "snapshot" }> =>
      item.kind === "snapshot",
  ).pipe(Effect.map((frame) => frame.snapshot));

/** Every failure a client method can report, as the one error the TUI renders. */
const toSessionError = (cause: unknown): TuiSessionError =>
  new TuiSessionError({ message: String(cause) });

/** Client-side ids: opaque to the server, unique per process. */
const newId = (seed: number, salt: number): string =>
  `tui-${seed.toString(36)}-${salt.toString(36)}`;

/** The ids and timestamp one command carries, drawn together. */
const newCommandIdentity = (): Effect.Effect<{
  readonly commandId: CommandId;
  readonly messageId: MessageId;
  readonly createdAt: string;
}> =>
  Effect.all([Random.nextInt, Random.nextInt, DateTime.now]).pipe(
    Effect.map(([seed, salt, at]) => ({
      commandId: CommandId.make(newId(seed, salt)),
      messageId: MessageId.make(newId(salt, seed)),
      createdAt: DateTime.formatIso(at),
    })),
  );

/** Everything a client needs about the server: providers, models, settings. */
export const loadServerConfig = (
  session: TuiSession,
): Effect.Effect<ServerConfig, TuiSessionError> =>
  session.client[WS_METHODS.serverGetConfig]({}).pipe(Effect.mapError(toSessionError));

/** Read and patch server settings (`server.getSettings` / `server.updateSettings`). */
export const loadServerSettings = (
  session: TuiSession,
): Effect.Effect<ServerSettings, TuiSessionError> =>
  session.client[WS_METHODS.serverGetSettings]({}).pipe(Effect.mapError(toSessionError));

export const updateServerSettings = (
  session: TuiSession,
  patch: ServerSettingsPatch,
): Effect.Effect<ServerSettings, TuiSessionError> =>
  session.client[WS_METHODS.serverUpdateSettings]({ patch }).pipe(Effect.mapError(toSessionError));

export interface StartThreadInput {
  readonly threadId: string;
  readonly projectId: string;
  readonly title: string;
  readonly text: string;
  readonly modelSelection: ModelSelection;
}

/**
 * Create a thread and send its first user message in one command, using
 * `thread.turn.start`'s `bootstrap.createThread`.
 */
export const startThread = (
  session: TuiSession,
  input: StartThreadInput,
): Effect.Effect<void, TuiSessionError> =>
  Effect.gen(function* () {
    const { commandId, messageId, createdAt } = yield* newCommandIdentity();
    yield* session.client[ORCHESTRATION_WS_METHODS.dispatchCommand]({
      type: "thread.turn.start",
      commandId,
      threadId: ThreadId.make(input.threadId),
      message: { messageId, role: "user", text: input.text, attachments: [] },
      modelSelection: input.modelSelection,
      runtimeMode: DEFAULT_RUNTIME_MODE,
      interactionMode: DEFAULT_PROVIDER_INTERACTION_MODE,
      bootstrap: {
        createThread: {
          projectId: ProjectId.make(input.projectId),
          title: input.title,
          modelSelection: input.modelSelection,
          runtimeMode: DEFAULT_RUNTIME_MODE,
          interactionMode: DEFAULT_PROVIDER_INTERACTION_MODE,
          branch: null,
          worktreePath: null,
          createdAt,
        },
      },
      createdAt,
    }).pipe(Effect.mapError(toSessionError));
  });

/** A follow-up user message in an existing thread. */
export const sendTurn = (
  session: TuiSession,
  input: {
    readonly threadId: string;
    readonly text: string;
    readonly modelSelection?: ModelSelection;
  },
): Effect.Effect<void, TuiSessionError> =>
  Effect.gen(function* () {
    const { commandId, messageId, createdAt } = yield* newCommandIdentity();
    yield* session.client[ORCHESTRATION_WS_METHODS.dispatchCommand]({
      type: "thread.turn.start",
      commandId,
      threadId: ThreadId.make(input.threadId),
      message: { messageId, role: "user", text: input.text, attachments: [] },
      // The thread already has a model: only restate it when the caller moved it.
      ...(input.modelSelection === undefined ? {} : { modelSelection: input.modelSelection }),
      runtimeMode: DEFAULT_RUNTIME_MODE,
      interactionMode: DEFAULT_PROVIDER_INTERACTION_MODE,
      createdAt,
    }).pipe(Effect.mapError(toSessionError));
  });

/**
 * Register a directory as a project. The id is derived from the path, so the
 * same directory always lands on the same project — adding it twice is refused
 * rather than duplicated.
 */
export const addProject = (
  session: TuiSession,
  input: { readonly workspaceRoot: string },
): Effect.Effect<string, TuiSessionError> =>
  Effect.gen(function* () {
    const workspaceRoot = input.workspaceRoot.trim().replace(/\/+$/u, "");
    const name =
      workspaceRoot
        .split("/")
        .filter((part) => part.length > 0)
        .at(-1) ?? workspaceRoot;
    const slug = `${name
      .toLowerCase()
      .replace(/[^a-z0-9]+/gu, "-")
      .replace(/^-+|-+$/gu, "")
      .slice(0, 32)}-${hash(workspaceRoot)}`;
    const at = DateTime.formatIso(yield* DateTime.now);
    yield* session.client["orchestration.dispatchCommand"]({
      type: "project.create",
      commandId: CommandId.make(`tui-project-${hash(`${workspaceRoot}${at}`)}`),
      projectId: ProjectId.make(slug),
      title: name.length === 0 ? workspaceRoot : name,
      workspaceRoot,
      createWorkspaceRootIfMissing: true,
      createdAt: at,
    });
    return slug;
  }).pipe(Effect.mapError(toSessionError));

/** Stable short id for a path, so re-adding the same directory is idempotent. */
const hash = (value: string): string => {
  let accumulator = 0;
  for (const character of value) {
    accumulator = (accumulator * 31 + character.codePointAt(0)!) % 0xffffffff;
  }
  return accumulator.toString(36);
};

/** Ask the server to stop the running turn. */
export const interruptTurn = (
  session: TuiSession,
  threadId: string,
): Effect.Effect<void, TuiSessionError> =>
  Effect.gen(function* () {
    const { commandId, createdAt } = yield* newCommandIdentity();
    yield* session.client[ORCHESTRATION_WS_METHODS.dispatchCommand]({
      type: "thread.turn.interrupt",
      commandId,
      threadId: ThreadId.make(threadId),
      createdAt,
    }).pipe(Effect.mapError(toSessionError));
  });

/**
 * The thread's live detail: the server sends a snapshot and then every event
 * that touches it. The caller keeps `afterSequence` from what it has applied
 * and passes it back on reconnect, which is how a resubscribe resumes without
 * gaps or duplicates. Run it with `Effect.runFork` and interrupt the fiber to
 * stop reading — a subscription outlives the call that started it.
 */
export const threadStream = (
  session: TuiSession,
  input: { readonly threadId: string; readonly afterSequence: number },
): Stream.Stream<OrchestrationThreadStreamItem, TuiSessionError> =>
  (
    session.client[ORCHESTRATION_WS_METHODS.subscribeThread]({
      threadId: ThreadId.make(input.threadId),
      afterSequence: input.afterSequence,
      requestCompletionMarker: true,
    }) as Stream.Stream<OrchestrationThreadStreamItem, unknown>
  ).pipe(Stream.mapError((cause: unknown) => toSessionError(cause)));

/** The thread list, live: projects and threads arrive as they change. */
export const shellStream = (
  session: TuiSession,
  input: { readonly afterSequence: number },
): Stream.Stream<OrchestrationShellStreamItem, TuiSessionError> =>
  (
    session.client[ORCHESTRATION_WS_METHODS.subscribeShell]({
      afterSequence: input.afterSequence,
      requestCompletionMarker: true,
    }) as Stream.Stream<OrchestrationShellStreamItem, unknown>
  ).pipe(Stream.mapError((cause: unknown) => toSessionError(cause)));

/** Read one sub-agent's log. Unary: the desktop polls it once a second. */
export const getAgentOutput = (
  session: TuiSession,
  outputPath: string,
): Effect.Effect<OrchestrationGetAgentOutputResult, TuiSessionError> =>
  session.client[ORCHESTRATION_WS_METHODS.getAgentOutput]({ outputPath }).pipe(
    Effect.mapError(toSessionError),
  );
