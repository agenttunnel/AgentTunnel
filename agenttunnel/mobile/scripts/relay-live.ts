// Live interop for the REAL mobile relay client (src/lib/relay.ts): it pairs,
// attaches, and drives a full turn against a real atrelay and a real daemon,
// then proves a desktop-initiated unpair rejects the phone. Everything the
// fake-socket smoke cannot show: the real hello/pair/attach wire exchange,
// turn replies that arrive only at turn end, and revocation semantics.
//
// Driven by pkg/daemon/relay_mobile_interop_test.go, and runnable by hand
// against a live relay + daemon (the desktop must call relay.unpair when the
// script prints READY-FOR-UNPAIR):
//
//   AT_RELAY_URL=ws://127.0.0.1:7777 AT_PAIR_CODE=ABCDE-FGHJK bun run scripts/relay-live.ts
//
// stdout lines are its protocol; exit 0 means every check passed.
import { RelayError, RpcClient, type RpcStatus, exchangePairCode } from "../src/lib/relay";

const relayUrl = process.env.AT_RELAY_URL ?? "";
const pairCode = process.env.AT_PAIR_CODE ?? "";
if (!relayUrl || !pairCode) {
  console.error("AT_RELAY_URL and AT_PAIR_CODE are required");
  process.exit(2);
}

function fail(msg: string): never {
  console.error(`INTEROP-FAIL ${msg}`);
  process.exit(1);
  throw new Error("unreachable");
}

const say = (line: string): void => {
  process.stdout.write(`${line}\n`);
};

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

// One slot is enough: only one waitFor is active at a time. Function calls
// reset property narrowing, so the condition sees callback writes.
const gate = { notify: null as (() => void) | null };

// waitFor blocks until cond holds, notified on every callback the client
// fires (status change or event), with a deadline so a wedged relay fails
// loudly instead of hanging.
function waitFor(what: string, cond: () => boolean, ms: number): Promise<void> {
  return new Promise((resolve, reject) => {
    if (cond()) {
      resolve();
      return;
    }
    const timer = setTimeout(() => {
      gate.notify = null;
      reject(new Error(`timed out after ${ms}ms waiting for ${what}`));
    }, ms);
    const sub = (): void => {
      if (!cond()) return;
      clearTimeout(timer);
      gate.notify = null;
      resolve();
    };
    gate.notify = sub;
  });
}

const poke = (): void => {
  gate.notify?.();
};

type EventFrame = { type?: unknown; threadId?: unknown };

async function main(): Promise<void> {
  // ---------- pair through the real relay ----------
  const pairing = await exchangePairCode(relayUrl, pairCode, { deviceName: "QA bun client" });
  if (!pairing.token) fail("pairing ack carried no token");
  say("paired");

  // ---------- attach and drive the daemon ----------
  const state = {
    events: [] as EventFrame[],
    statuses: [] as RpcStatus[],
    lastError: null as RelayError | null,
  };
  const client = new RpcClient({
    wsUrl: pairing.wsUrl,
    token: pairing.token,
    onEvent: (frame) => {
      state.events.push(frame as EventFrame);
      poke();
    },
    onStatus: (s, e) => {
      state.statuses.push(s);
      if (e) state.lastError = e;
      poke();
    },
  });
  client.start();
  await waitFor("open", () => client.getStatus() === "open", 15_000);
  if (state.statuses.includes("reconnecting")) fail("first connect should open cleanly, not reconnect");
  say("open");

  const created = await client.send<{ threadId?: string }>("thread.create", { title: "relay-live" });
  const threadId = created?.threadId;
  if (!threadId) fail("thread.create returned no threadId");
  await client.send("thread.subscribe", { threadId });
  const list = await client.send<{ id?: string }[]>("thread.list");
  if (!Array.isArray(list) || !list.some((t) => t && t.id === threadId)) {
    fail(`thread.list after create must list ${String(threadId)}`);
  }
  say("listed");

  // The turn replies only when it ends, so it runs on the no-timeout path
  // (timeoutMs 0). The steer rides the same turn and replies at once.
  const turn = client.send("thread.turn.start", { threadId, text: "build it over the relay" }, 0);
  const steer = await client.send<{ ok?: boolean }>("thread.steer", { threadId, text: "and steer it" });
  if (!steer || steer.ok !== true) fail(`thread.steer must reply ok, got ${JSON.stringify(steer)}`);
  say("steered");
  await turn;
  say("turn-done");

  // thread.event frames must have landed while the turn ran.
  const sawOutput = state.events.some((f) => f.type === "agent.output");
  const sawState = state.events.some((f) => typeof f.type === "string" && String(f.type).startsWith("thread."));
  if (!sawOutput || !sawState) {
    fail(`expected agent.output and thread.* event frames, got ${JSON.stringify(state.events.map((f) => f.type))}`);
  }
  say(`events ${state.events.length}`);

  // ---------- the desktop unpairs this phone ----------
  say("READY-FOR-UNPAIR");
  await waitFor("the revoked socket to close", () => client.getStatus() === "closed", 60_000);
  const revokedError = state.lastError;
  if (!revokedError || revokedError.code !== "unpaired") {
    fail(`revocation must surface the unpaired error, got ${revokedError ? revokedError.code : "none"}`);
  }
  say("REVOKED-UNPAIRED");

  // ---------- the phone is rejected: same token, no retry loop ----------
  const after = {
    statuses: [] as RpcStatus[],
    lastError: null as RelayError | null,
  };
  const second = new RpcClient({
    wsUrl: pairing.wsUrl,
    token: pairing.token,
    onEvent: () => undefined,
    onStatus: (s, e) => {
      after.statuses.push(s);
      if (e) after.lastError = e;
      poke();
    },
  });
  second.start();
  await waitFor("the revoked token to be refused", () => second.getStatus() === "closed", 15_000);
  if (after.statuses.includes("open")) fail("a revoked token must never open");
  const refusedError = after.lastError;
  if (!refusedError || refusedError.code !== "unpaired") {
    fail(`a refused token must surface unpaired, got ${refusedError ? refusedError.code : "none"}`);
  }
  // Proving the ABSENCE of a retry loop needs a window of silence. This is
  // the only deliberate wait in the script.
  await sleep(2_000);
  if (second.getStatus() !== "closed") fail(`client must stay closed after bad_token, is ${second.getStatus()}`);
  say("NO-RETRY-LOOP");

  say("INTEROP-OK");
  // bun keeps running after main returns if any handle is still open (the
  // pairing socket's close is not always prompt), so exit explicitly. The
  // lines above were already flushed; a failure path above exits earlier.
  process.exit(0);
}

main().catch((e: unknown) => {
  fail(e instanceof Error ? e.message : String(e));
});
