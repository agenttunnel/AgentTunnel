import {
  ORCHESTRATION_WS_METHODS,
  ThreadId,
  type OrchestrationShellSnapshot,
  type OrchestrationShellStreamItem,
  type OrchestrationThreadDetailSnapshot,
  type OrchestrationThreadStreamItem,
} from "@t3tools/contracts";
import * as Effect from "effect/Effect";
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
