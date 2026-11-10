// Smoke for the pure mobile logic: thread grouping, live-output reduction,
// markdown-lite, and the RPC client against a fake socket with manual timers.
// Run with: bun run scripts/smoke.ts   (no simulator, no network, no sleeps)
// Minimal assertions so the script type-checks without @types/node.
const assert = {
  ok(v: unknown, msg = "expected a truthy value"): void {
    if (!v) throw new Error(msg);
  },
  equal(actual: unknown, expected: unknown, msg?: string): void {
    if (!Object.is(actual, expected)) {
      throw new Error(msg ?? `expected ${String(expected)}, got ${String(actual)}`);
    }
  },
  deepEqual(actual: unknown, expected: unknown): void {
    const a = JSON.stringify(actual);
    const b = JSON.stringify(expected);
    if (a !== b) throw new Error(`expected ${b}, got ${a}`);
  },
  throws(fn: () => unknown, cls?: new (...args: never[]) => Error): void {
    try {
      fn();
    } catch (e) {
      if (cls && !(e instanceof cls)) throw new Error("threw the wrong error type");
      return;
    }
    throw new Error("expected a throw");
  },
  async rejects(p: Promise<unknown>, check: (e: unknown) => boolean): Promise<void> {
    try {
      await p;
    } catch (e) {
      if (!check(e)) throw new Error("rejected with an unexpected error");
      return;
    }
    throw new Error("expected a rejection");
  },
};

/** Last element, for arrays under noUncheckedIndexedAccess. */
function lastOf<T>(xs: readonly T[]): T {
  const v = xs[xs.length - 1];
  if (v === undefined) throw new Error("expected a non-empty array");
  return v;
}

import {
  appendLiveLine,
  arrangeThreads,
  dropSnapshotOverlap,
  groupThreads,
  inlineSpans,
  isStateFrame,
  LIVE_LIMIT,
  outputFromFrame,
  parseMarkdownLite,
  relativeTime,
  type LiveLine,
  type ProjectView,
  type ThreadSummary,
} from "../src/lib/normalize";
import {
  exchangePairCode,
  normalizePairCode,
  normalizeRelayUrl,
  RelayError,
  RpcClient,
  type RpcStatus,
} from "../src/lib/relay";

let passed = 0;
function test(name: string, fn: () => void | Promise<void>): Promise<void> {
  return Promise.resolve()
    .then(fn)
    .then(() => {
      passed++;
      console.log(`ok  ${name}`);
    });
}

const thread = (over: Partial<ThreadSummary> & { id: string }): ThreadSummary => ({
  title: over.id,
  updatedAt: "2026-11-10T00:00:00Z",
  messages: 0,
  agents: 0,
  runningAgents: 0,
  stale: false,
  ...over,
});

// ---------- fake socket + manual timers ----------

class FakeSocket {
  readyState = 0;
  sent: { id: string; method: string; params: unknown }[] = [];
  closed = false;
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  send(data: string): void {
    const m = JSON.parse(data) as { id: string; method: string; params: unknown };
    this.sent.push(m);
  }
  close(): void {
    this.closed = true;
    this.readyState = 3;
    this.onclose?.();
  }
  open(): void {
    this.readyState = 1;
    this.onopen?.();
  }
  reply(id: string, result: unknown): void {
    this.onmessage?.({ data: JSON.stringify({ id, result }) });
  }
  fail(id: string, message: string): void {
    this.onmessage?.({ data: JSON.stringify({ id, error: { code: 1, message } }) });
  }
  push(frame: unknown): void {
    this.onmessage?.({ data: JSON.stringify(frame) });
  }
}

type Timer = { fn: () => void; ms: number; cleared: boolean };

function fakeTimers() {
  const timers: Timer[] = [];
  return {
    timers,
    api: {
      set(fn: () => void, ms: number): unknown {
        const t: Timer = { fn, ms, cleared: false };
        timers.push(t);
        return t;
      },
      clear(handle: unknown): void {
        (handle as Timer).cleared = true;
      },
    },
    /** Fires the single live timer with the given delay. */
    fire(ms: number): void {
      const live = timers.filter((t) => !t.cleared && t.ms === ms);
      assert.equal(live.length, 1, `expected one live timer of ${ms}ms, got ${live.length}`);
      live[0]!.cleared = true;
      live[0]!.fn();
    },
    liveDelays(): number[] {
      return timers.filter((t) => !t.cleared).map((t) => t.ms);
    },
  };
}

function harness(opts: { requestTimeoutMs?: number; maxQueue?: number } = {}) {
  const sockets: FakeSocket[] = [];
  const events: unknown[] = [];
  const statuses: RpcStatus[] = [];
  const errors: (RelayError | undefined)[] = [];
  const opened: number[] = [];
  const t = fakeTimers();
  const client = new RpcClient({
    wsUrl: "wss://relay.test/ws",
    token: "tok-secret",
    onEvent: (f) => events.push(f),
    onStatus: (s, e) => {
      statuses.push(s);
      errors.push(e);
    },
    onOpen: () => opened.push(sockets.length),
    requestTimeoutMs: opts.requestTimeoutMs ?? 1000,
    reconnect: { baseMs: 100, maxMs: 400 },
    maxQueue: opts.maxQueue,
    timers: t.api,
    socketFactory: () => {
      const s = new FakeSocket();
      sockets.push(s);
      return s as unknown as WebSocket;
    },
  });
  return { client, sockets, events, statuses, errors, opened, t };
}

async function main() {
  // ---------- pure logic ----------

  await test("threads group by project in project order, orphans last", () => {
    const projects: ProjectView[] = [
      { id: "p2", name: "beta", path: "/b", threads: 1 },
      { id: "p1", name: "alpha", path: "/a", threads: 2 },
    ];
    const sections = groupThreads(
      [
        thread({ id: "t1", projectId: "p1" }),
        thread({ id: "t2", projectId: "p2" }),
        thread({ id: "t3", projectId: "p1" }),
        thread({ id: "t4", projectId: "gone" }),
      ],
      projects,
    );
    assert.deepEqual(
      sections.map((s) => [s.name, s.threads.map((t) => t.id)]),
      [
        ["beta", ["t2"]],
        ["alpha", ["t1", "t3"]],
        ["Other", ["t4"]],
      ],
    );
  });

  await test("subthreads follow their parent, indented; orphaned subthreads stay visible", () => {
    const rows = arrangeThreads([
      { id: "child", parentThreadId: "parent" },
      { id: "other" },
      { id: "parent" },
      { id: "lost", parentThreadId: "missing" },
    ]);
    assert.deepEqual(
      rows.map((r) => [r.id, r.depth]),
      [
        ["other", 0],
        ["parent", 0],
        ["child", 1],
        ["lost", 0],
      ],
    );
  });

  await test("parent cycles cannot hide threads", () => {
    const rows = arrangeThreads([
      { id: "a", parentThreadId: "b" },
      { id: "b", parentThreadId: "a" },
    ]);
    assert.equal(rows.length, 2);
  });

  await test("text chunks from one agent merge; other kinds stack", () => {
    let lines: LiveLine[] = [];
    lines = appendLiveLine(lines, { agentId: "a", kind: "text", text: "Hel" });
    lines = appendLiveLine(lines, { agentId: "a", kind: "text", text: "lo" });
    lines = appendLiveLine(lines, { agentId: "a", kind: "tool", text: "ls" });
    lines = appendLiveLine(lines, { agentId: "a", kind: "tool", text: "pwd" });
    assert.deepEqual(lines, [
      { agentId: "a", kind: "text", text: "Hello" },
      { agentId: "a", kind: "tool", text: "ls" },
      { agentId: "a", kind: "tool", text: "pwd" },
    ]);
  });

  await test("live output is bounded", () => {
    let lines: LiveLine[] = [];
    for (let i = 0; i < LIVE_LIMIT + 50; i++) {
      lines = appendLiveLine(lines, { agentId: "a", kind: "tool", text: String(i) });
    }
    assert.equal(lines.length, LIVE_LIMIT);
    assert.equal(lines[lines.length - 1]?.text, String(LIVE_LIMIT + 49));
  });

  await test("buffered frames already in the snapshot tail are dropped, new ones kept", () => {
    const snapshot: LiveLine[] = [
      { agentId: "a", kind: "tool", text: "one" },
      { agentId: "a", kind: "tool", text: "two" },
    ];
    const buffered: LiveLine[] = [
      { agentId: "a", kind: "tool", text: "two" },
      { agentId: "a", kind: "tool", text: "three" },
    ];
    assert.deepEqual(dropSnapshotOverlap(snapshot, buffered), [{ agentId: "a", kind: "tool", text: "three" }]);
    assert.deepEqual(dropSnapshotOverlap([], buffered), buffered);
  });

  await test("agent.output frames are output; other events are state refreshes", () => {
    const out = outputFromFrame({
      type: "agent.output",
      threadId: "t1",
      payload: { agentId: "a1", kind: "text", text: "hi" },
    });
    assert.deepEqual(out, { threadId: "t1", line: { agentId: "a1", kind: "text", text: "hi" } });
    assert.equal(outputFromFrame({ type: "thread.message-sent", threadId: "t1" }), null);
    assert.equal(isStateFrame({ type: "thread.message-sent" }), true);
    assert.equal(isStateFrame({ type: "agent.output" }), false);
    assert.equal(isStateFrame("nope"), false);
  });

  await test("markdown-lite: headings, bullets, paragraphs, fenced code", () => {
    const blocks = parseMarkdownLite("# Plan\n\nRun **tests** with `go test`.\n- first\n* second\n```ts\nconst x = 1;\n```\n");
    assert.deepEqual(blocks, [
      { kind: "heading", level: 1, spans: [{ kind: "text", text: "Plan" }] },
      {
        kind: "para",
        spans: [
          { kind: "text", text: "Run " },
          { kind: "bold", text: "tests" },
          { kind: "text", text: " with " },
          { kind: "code", text: "go test" },
          { kind: "text", text: "." },
        ],
      },
      { kind: "bullet", spans: [{ kind: "text", text: "first" }] },
      { kind: "bullet", spans: [{ kind: "text", text: "second" }] },
      { kind: "code", lang: "ts", text: "const x = 1;" },
    ]);
  });

  await test("markdown-lite leaves an unclosed fence as code, not as crash", () => {
    const blocks = parseMarkdownLite("```\nunterminated");
    assert.deepEqual(blocks, [{ kind: "code", lang: "", text: "unterminated" }]);
  });

  await test("inline spans keep plain text around code and bold", () => {
    assert.deepEqual(inlineSpans("plain"), [{ kind: "text", text: "plain" }]);
    assert.deepEqual(inlineSpans("`a`"), [{ kind: "code", text: "a" }]);
  });

  await test("relative time buckets", () => {
    const now = Date.parse("2026-11-10T12:00:00Z");
    assert.equal(relativeTime("2026-11-10T11:59:30Z", now), "now");
    assert.equal(relativeTime("2026-11-10T11:55:00Z", now), "5m");
    assert.equal(relativeTime("2026-11-10T09:00:00Z", now), "3h");
    assert.equal(relativeTime("2026-11-08T12:00:00Z", now), "2d");
    assert.equal(relativeTime("not a date", now), "");
  });

  // ---------- pairing input ----------

  await test("relay URL: trims, drops trailing slashes, rejects non-http", () => {
    assert.equal(normalizeRelayUrl("  https://relay.test/  "), "https://relay.test");
    assert.throws(() => normalizeRelayUrl("relay.test"), RelayError);
    assert.throws(() => normalizeRelayUrl("ftp://relay.test"), RelayError);
  });

  await test("pair code: spaces, dashes, and case are normalized", () => {
    assert.equal(normalizePairCode(" abcd-1234 "), "ABCD1234");
    assert.throws(() => normalizePairCode("  -  "), RelayError);
  });

  await test("pairing exchange fails with a typed unpublished error until the protocol lands", async () => {
    await assert.rejects(
      exchangePairCode("https://relay.test", "ABCD1234"),
      (e) => e instanceof RelayError && e.code === "unpublished",
    );
  });

  // ---------- RPC client ----------

  await test("request ids match replies; an rpc error surfaces its message", async () => {
    const h = harness();
    h.client.start();
    h.sockets[0]!.open();
    const a = h.client.call<string[]>("thread.list");
    const b = h.client.call("thread.get", { threadId: "t1" });
    const sock = h.sockets[0]!;
    assert.deepEqual(
      sock.sent.map((m) => [m.id, m.method]),
      [
        ["1", "thread.list"],
        ["2", "thread.get"],
      ],
    );
    sock.reply("2", { id: "t1" });
    sock.reply("1", ["x"]);
    assert.deepEqual(await a, ["x"]);
    assert.deepEqual(await b, { id: "t1" });
    const c = h.client.call("thread.turn.stop", { threadId: "t1" });
    sock.fail("3", "thread t1 does not exist");
    await assert.rejects(c, (e: unknown) => e instanceof RelayError && e.code === "rpc" && e.message.includes("does not exist"));
  });

  await test("thread.event pushes reach the handler and are not treated as replies", () => {
    const h = harness();
    h.client.start();
    h.sockets[0]!.open();
    h.sockets[0]!.push({ method: "thread.event", params: { type: "agent.output", threadId: "t1" } });
    assert.equal(h.events.length, 1);
  });

  await test("reads reject immediately while offline; open after the socket opens", async () => {
    const h = harness();
    await assert.rejects(h.client.call("thread.list"), (e: unknown) => e instanceof RelayError && e.code === "offline");
    h.client.start();
    assert.equal(h.sockets[0]!.sent.length, 0);
    h.sockets[0]!.open();
    assert.equal(h.client.getStatus(), "open");
  });

  await test("a request with no reply times out with a typed error", async () => {
    const h = harness({ requestTimeoutMs: 1000 });
    h.client.start();
    h.sockets[0]!.open();
    const p = h.client.call("thread.list");
    h.t.fire(1000);
    await assert.rejects(p, (e: unknown) => e instanceof RelayError && e.code === "timeout");
  });

  await test("timeoutMs 0 never times out (a turn replies only when it ends)", () => {
    const h = harness({ requestTimeoutMs: 1000 });
    h.client.start();
    h.sockets[0]!.open();
    void h.client.send("thread.turn.start", { threadId: "t1", text: "go" }, 0);
    assert.equal(h.t.liveDelays().length, 0, "no timer armed for a zero timeout");
  });

  await test("a drop rejects in-flight requests, backs off, and reconnects", async () => {
    const h = harness();
    h.client.start();
    h.sockets[0]!.open();
    const p = h.client.call("thread.list");
    h.sockets[0]!.onclose?.();
    await assert.rejects(p, (e: unknown) => e instanceof RelayError && e.code === "closed");
    assert.equal(h.client.getStatus(), "reconnecting");
    assert.equal(lastOf(h.errors)?.code, "unreachable");
    h.t.fire(100);
    assert.equal(h.sockets.length, 2);
    h.sockets[1]!.open();
    assert.equal(h.client.getStatus(), "open");
    // onOpen fires once per socket open, stamped with the socket count then.
    assert.deepEqual(h.opened, [1, 2]);
  });

  await test("backoff doubles and is capped", () => {
    const h = harness();
    h.client.start();
    const delays: number[] = [];
    for (let i = 0; i < 4; i++) {
      lastOf(h.sockets).onclose?.();
      const live = h.t.timers.filter((t) => !t.cleared);
      delays.push(lastOf(live).ms);
      h.t.fire(lastOf(live).ms);
    }
    assert.deepEqual(delays, [100, 200, 400, 400]);
  });

  await test("sends queued while offline flush in order after reconnect, onOpen first", async () => {
    const h = harness();
    h.client.start();
    const first = h.client.send<string>("thread.turn.start", { threadId: "t1", text: "one" }, 0);
    const second = h.client.send<string>("thread.steer", { threadId: "t1", text: "two" });
    assert.equal(h.client.queuedCount(), 2);
    assert.equal(h.sockets[0]!.sent.length, 0, "nothing sent before the socket opens");
    h.sockets[0]!.open();
    assert.equal(h.client.queuedCount(), 0);
    const sock = h.sockets[0]!;
    assert.deepEqual(
      sock.sent.map((m) => [m.id, m.method]),
      [
        ["1", "thread.turn.start"],
        ["2", "thread.steer"],
      ],
    );
    sock.reply("1", "done one");
    sock.reply("2", "steered");
    assert.equal(await first, "done one");
    assert.equal(await second, "steered");
  });

  await test("a full queue fails fast with a typed error", async () => {
    const h = harness({ maxQueue: 1 });
    h.client.start();
    void h.client.send("thread.steer", { threadId: "t1", text: "a" }).catch(() => undefined);
    await assert.rejects(
      h.client.send("thread.steer", { threadId: "t1", text: "b" }),
      (e: unknown) => e instanceof RelayError && e.code === "queue-full",
    );
  });

  await test("stop rejects queued sends and never reconnects", async () => {
    const h = harness();
    h.client.start();
    const queued = h.client.send("thread.steer", { threadId: "t1", text: "x" });
    h.client.stop();
    await assert.rejects(queued, (e: unknown) => e instanceof RelayError && e.code === "closed");
    assert.equal(h.client.getStatus(), "closed");
    assert.equal(h.t.liveDelays().length, 0, "no retry armed after stop");
    assert.equal(h.sockets.length, 1);
  });

  console.log(`\n${passed} smoke checks passed`);
}

main().catch((e: unknown) => {
  console.error(e);
  process.exit(1);
});
