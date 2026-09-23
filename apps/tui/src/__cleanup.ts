import * as DateTime from "effect/DateTime";
import * as Effect from "effect/Effect";
import * as Scope from "effect/Scope";
import { CommandId, ThreadId } from "@t3tools/contracts";
import { remoteHttpClientLayer } from "@t3tools/client-runtime/rpc";

import { connect, resolveConnection, resolveSocketTicket } from "./session.ts";

/** Throwaway: removes the thread created while verifying the provider default. */
const url = process.env.T3CODE_URL ?? "http://localhost:15191";
const ticket = await Effect.runPromise(
  resolveSocketTicket({ httpBaseUrl: url, token: process.env.T3CODE_TOKEN }).pipe(
    Effect.provide(remoteHttpClientLayer(fetch)),
  ),
);
const scope = await Effect.runPromise(Scope.make());
const session = await Effect.runPromise(
  connect(resolveConnection({ url, ticket: ticket ?? undefined })).pipe(
    Effect.provideService(Scope.Scope, scope),
  ),
);
const result = await Effect.runPromise(
  Effect.gen(function* () {
    const at = DateTime.formatIso(yield* DateTime.now);
    return yield* session.client["orchestration.dispatchCommand"]({
      type: "thread.delete",
      commandId: CommandId.make(`tui-cleanup-${Date.parse(at)}`),
      threadId: ThreadId.make(process.env.THREAD_ID ?? ""),
      createdAt: at,
    });
  }),
);
process.stdout.write(`deleted: ${JSON.stringify(result)}\n`);
