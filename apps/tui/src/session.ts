import {
  ORCHESTRATION_WS_METHODS,
  ThreadId,
  type OrchestrationShellSnapshot,
  type OrchestrationShellStreamItem,
  type OrchestrationThreadDetailSnapshot,
  type OrchestrationThreadStreamItem,
} from "@t3tools/contracts";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as RpcClient from "effect/unstable/rpc/RpcClient";
import * as RpcSerialization from "effect/unstable/rpc/RpcSerialization";
import * as Socket from "effect/unstable/socket/Socket";
import * as Stream from "effect/Stream";
import * as Schema from "effect/Schema";
import type * as Scope from "effect/Scope";

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
  readonly token?: string | undefined;
}): TuiConnection {
  const httpBaseUrl = input.url.replace(/\/+$/u, "");
  const socketUrl = new URL(`${httpBaseUrl.replace(/^http/u, "ws")}/ws`);
  if (input.token !== undefined && input.token.trim().length > 0) {
    socketUrl.searchParams.set("wsTicket", input.token.trim());
  }
  return { httpBaseUrl, socketUrl: socketUrl.toString() };
}

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

/**
 * The first frame a subscription sends that the caller recognises, or a session
 * error. The subscriptions stream snapshots and incremental events; the TUI
 * reads the snapshot and leaves the deltas for a later pass.
 */
const firstFrame = <A, B extends A>(
  stream: Stream.Stream<A, unknown>,
  wanted: (item: A) => item is B,
): Effect.Effect<B, TuiSessionError> =>
  stream.pipe(
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
