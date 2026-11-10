// Relay transport: pairing and the JSON-RPC client the phone uses to reach its
// instance through atrelay. The wire contract is docs/relay-protocol.md.
//
// No secret goes in a URL. The phone opens the relay's /v1/client socket and
// authenticates with a first frame: `pair` (with the code) to pair, or `hello`
// (with the token) followed by `attach` to resume. After attach the socket
// carries the daemon's own JSON-RPC, the same surface the desktop speaks:
// request {id, method, params}, reply {id, result|error}, and pushed
// {method: "thread.event", params} frames.

export type RelayErrorCode =
  | "rejected" // the address, pairing code, or relay refused the request
  | "unpaired" // this phone's token was revoked; it must pair again
  | "unreachable" // the socket could not open or dropped
  | "offline" // a read was attempted while not connected
  | "timeout" // no reply within the deadline
  | "closed" // the client was stopped, or the socket dropped with the request in flight
  | "rpc" // the daemon replied with an error
  | "queue-full"; // too many sends are waiting for the connection

export class RelayError extends Error {
  readonly code: RelayErrorCode;
  constructor(code: RelayErrorCode, message: string) {
    super(message);
    this.name = "RelayError";
    this.code = code;
  }
}

/** What a successful pairing leaves on the device. The token is a secret. */
export type Pairing = {
  /** The relay base address the user entered, normalized. */
  relayUrl: string;
  /** The relay's phone endpoint. It carries no secret. */
  wsUrl: string;
  token: string;
};

const PAIR_TIMEOUT_MS = 15_000;
const HANDSHAKE_TIMEOUT_MS = 10_000;

// scheme://host[:port][/path]. No userinfo, query, or fragment: none of those
// belong in a relay address, and a secret there would be logged by proxies.
const RELAY_URL = /^(https?|wss?):\/\/([^\s/?#@]+)(\/[^\s?#]*)?$/i;

function isLocalHost(hostPort: string): boolean {
  const host = hostPort.startsWith("[")
    ? hostPort.slice(1, hostPort.indexOf("]"))
    : hostPort.replace(/:\d+$/, "");
  if (host === "localhost" || host === "::1") return true;
  const v4 = /^(\d+)\.(\d+)\.\d+\.\d+$/.exec(host);
  if (!v4) return false;
  const a = Number(v4[1]);
  const b = Number(v4[2]);
  return a === 127 || a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 169 && b === 254);
}

/**
 * Trims and checks a relay address. Any host and any path prefix are accepted,
 * since self-hosted relays often sit behind a reverse proxy. ws:// and wss://
 * are read as http:// and https://. Plain http is accepted only for loopback
 * and private addresses, because the token and pairing code travel on the socket.
 */
export function normalizeRelayUrl(input: string): string {
  const trimmed = input.trim().replace(/\/+$/, "");
  const m = RELAY_URL.exec(trimmed);
  if (!m) throw new RelayError("rejected", "Enter the relay address, for example https://relay.example.com");
  const secure = /^(https|wss)$/i.test(m[1] ?? "");
  const host = m[2] ?? "";
  if (!secure && !isLocalHost(host)) {
    throw new RelayError("rejected", "Use https:// for a relay on the internet. Plain http:// only works on this network.");
  }
  return `${secure ? "https" : "http"}://${host}${m[3] ?? ""}`;
}

/** The relay's phone endpoint for a normalized base address. */
function clientSocketUrl(relayUrl: string): string {
  return `${relayUrl.replace(/^http/i, "ws")}/v1/client`;
}

/** Pairing codes are typed by hand: spaces and dashes are ignored, case is not. */
export function normalizePairCode(input: string): string {
  const code = input.replace(/[\s-]/g, "").toUpperCase();
  if (code.length === 0) throw new RelayError("rejected", "Enter the pairing code shown on your desktop");
  return code;
}

export type RpcTimers = {
  set(fn: () => void, ms: number): unknown;
  clear(handle: unknown): void;
};

const defaultTimers: RpcTimers = {
  set: (fn, ms) => setTimeout(fn, ms),
  clear: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

/** One frame off the relay socket. Control frames carry `t`; JSON-RPC does not. */
type Frame = {
  t?: string;
  ok?: boolean;
  error?: { message?: string } | string;
  token?: string;
  reason?: string;
  resync?: boolean;
  daemon?: string;
  id?: string | number;
  result?: unknown;
  method?: string;
  params?: unknown;
};

function parseFrame(raw: string): Frame | null {
  try {
    const v: unknown = JSON.parse(raw);
    return v !== null && typeof v === "object" ? (v as Frame) : null;
  } catch {
    return null;
  }
}

function pairRefusal(code: string | undefined): RelayError {
  switch (code) {
    case "bad_code":
      return new RelayError("rejected", "That code didn't work. Codes last five minutes and work once. Make a new one on your desktop.");
    case "rate_limited":
      return new RelayError("rejected", "Too many tries. Wait a minute, then try again.");
    default:
      return new RelayError("rejected", `The relay refused the pairing${code ? ` (${code})` : ""}.`);
  }
}

/** The label used when the user leaves the device name empty. */
export const DEFAULT_DEVICE_NAME = "Phone";
/** Matches the relay's clamp (pkg/relay maxClientNameRune). */
const MAX_DEVICE_NAME_RUNES = 64;

/**
 * Starting value for the Pair screen's device name, from the platform alone
 * (no device-info dependency). Editable by the user.
 */
export function defaultDeviceName(os: string): string {
  if (os === "ios") return "iPhone";
  if (os === "android") return "Android phone";
  return DEFAULT_DEVICE_NAME;
}

/** Trims and clamps a user-entered device name; empty falls back to the default. */
export function cleanDeviceName(raw: string | undefined): string {
  const trimmed = (raw ?? "").trim();
  if (trimmed === "") return DEFAULT_DEVICE_NAME;
  return Array.from(trimmed).slice(0, MAX_DEVICE_NAME_RUNES).join("");
}

export type PairOptions = {
  /** The label this phone shows in the desktop's device list. Cleaned before sending. */
  deviceName?: string;
  timeoutMs?: number;
  /** Injection seams: the smoke test drives sockets and timers by hand. */
  socketFactory?: (url: string) => WebSocket;
  timers?: RpcTimers;
};

/**
 * Redeems a pairing code at the relay and returns what the phone keeps: the
 * instance's socket endpoint and this phone's token. The code travels in a
 * `pair` frame, never in a URL. The relay makes it single use.
 */
export async function exchangePairCode(relayUrl: string, code: string, opts: PairOptions = {}): Promise<Pairing> {
  const base = normalizeRelayUrl(relayUrl);
  const pairCode = normalizePairCode(code);
  const wsUrl = clientSocketUrl(base);
  const timers = opts.timers ?? defaultTimers;
  const makeSocket = opts.socketFactory ?? ((url: string) => new WebSocket(url));
  const unreachable = () => new RelayError("unreachable", "Could not reach the relay. Check the address and your connection.");

  return new Promise<Pairing>((resolve, reject) => {
    let done = false;
    let timer: unknown = null;
    let ws: WebSocket | null = null;
    const finish = (settle: () => void): void => {
      if (done) return;
      done = true;
      if (timer !== null) timers.clear(timer);
      ws?.close();
      settle();
    };

    try {
      ws = makeSocket(wsUrl);
    } catch {
      reject(unreachable());
      return;
    }
    const sock = ws;
    timer = timers.set(
      () => finish(() => reject(new RelayError("timeout", "The relay did not answer. Check the address and try again."))),
      opts.timeoutMs ?? PAIR_TIMEOUT_MS,
    );
    sock.onopen = () => {
      try {
        sock.send(JSON.stringify({ t: "pair", code: pairCode, name: cleanDeviceName(opts.deviceName) }));
      } catch {
        finish(() => reject(unreachable()));
      }
    };
    sock.onmessage = (ev) => {
      const frame = parseFrame(String(ev.data));
      if (frame?.t !== "ack") return;
      if (frame.ok !== true) {
        const code = typeof frame.error === "string" ? frame.error : undefined;
        finish(() => reject(pairRefusal(code)));
        return;
      }
      const token = frame.token;
      if (!token) {
        finish(() => reject(new RelayError("rejected", "The relay sent no token.")));
        return;
      }
      finish(() => resolve({ relayUrl: base, wsUrl, token }));
    };
    const dropped = () => finish(() => reject(new RelayError("unreachable", "The relay closed the connection before pairing finished.")));
    sock.onclose = dropped;
    sock.onerror = dropped;
  });
}

// ---------- JSON-RPC client ----------

export type RpcStatus = "idle" | "connecting" | "open" | "reconnecting" | "closed";
export type DaemonPresence = "online" | "offline";

export type RpcOptions = {
  /** The relay's phone endpoint, from Pairing.wsUrl. */
  wsUrl: string;
  /** Sent in the first frame after connect. Never placed in the URL. */
  token: string;
  /** Receives every pushed thread.event frame's params. */
  onEvent: (frame: unknown) => void;
  /** Fires on every status change. `error` is set when the change is a failure. */
  onStatus: (status: RpcStatus, error?: RelayError) => void;
  /** Fires after each (re)connect is attached, before queued sends flush. Resubscribe here. */
  onOpen?: () => void;
  /** The relay asked for a re-read (a daemon-side overflow dropped events). */
  onResync?: () => void;
  /** The daemon's presence at the relay, from the attach ack and each status frame. */
  onDaemon?: (presence: DaemonPresence) => void;
  requestTimeoutMs?: number;
  reconnect?: { baseMs: number; maxMs: number };
  maxQueue?: number;
  /** Injection seams: the smoke test drives sockets and timers by hand. */
  socketFactory?: (url: string) => WebSocket;
  timers?: RpcTimers;
};

type Pending = {
  resolve: (value: unknown) => void;
  reject: (err: RelayError) => void;
  timer: unknown | null;
};

type Queued = {
  method: string;
  params: unknown;
  timeoutMs: number | undefined;
  resolve: (value: unknown) => void;
  reject: (err: RelayError) => void;
};

function unpairedError(): RelayError {
  return new RelayError("unpaired", "This phone is no longer paired. Pair it again from your desktop.");
}

export class RpcClient {
  private socket: WebSocket | null = null;
  private status: RpcStatus = "idle";
  private nextId = 1;
  private attempts = 0;
  private stopped = false;
  private retryHandle: unknown = null;
  /** Where the first-frame handshake stands: hello, then attach, then done (null). */
  private handshake: "hello" | "attach" | null = null;
  private handshakeTimer: unknown = null;
  private readonly pending = new Map<string, Pending>();
  private queue: Queued[] = [];
  private readonly timers: RpcTimers;
  private readonly requestTimeoutMs: number;
  private readonly reconnect: { baseMs: number; maxMs: number };
  private readonly maxQueue: number;

  constructor(private readonly opts: RpcOptions) {
    this.timers = opts.timers ?? defaultTimers;
    this.requestTimeoutMs = opts.requestTimeoutMs ?? 15_000;
    this.reconnect = opts.reconnect ?? { baseMs: 500, maxMs: 15_000 };
    this.maxQueue = opts.maxQueue ?? 20;
  }

  getStatus(): RpcStatus {
    return this.status;
  }

  /** Sends waiting for the connection. Surfaced so the UI can say "queued". */
  queuedCount(): number {
    return this.queue.length;
  }

  start(): void {
    this.stopped = false;
    this.open();
  }

  /**
   * A read. Rejects at once when the socket is not open: reads are not worth
   * holding, the caller refreshes on reconnect instead.
   */
  call<T = unknown>(method: string, params?: unknown, timeoutMs?: number): Promise<T> {
    if (this.status !== "open") {
      return Promise.reject(new RelayError("offline", "Not connected to your instance"));
    }
    return this.transmit(method, params, timeoutMs) as Promise<T>;
  }

  /**
   * A mutation the user expects to happen (send, stop). Sent now when open;
   * otherwise held and sent in order on reconnect. Pass timeoutMs 0 for calls
   * whose reply arrives only when the work ends (thread.turn.start replies when
   * the whole turn finishes), so they are never timed out.
   */
  send<T = unknown>(method: string, params?: unknown, timeoutMs?: number): Promise<T> {
    if (this.status === "open") return this.transmit(method, params, timeoutMs) as Promise<T>;
    if (this.queue.length >= this.maxQueue) {
      return Promise.reject(new RelayError("queue-full", "Too many messages are waiting for the connection"));
    }
    return new Promise<unknown>((resolve, reject) => {
      this.queue.push({ method, params, timeoutMs, resolve, reject });
    }) as Promise<T>;
  }

  stop(): void {
    this.halt();
  }

  /** Ends the client for good: no retry, queued and in-flight requests reject. */
  private halt(err?: RelayError): void {
    this.stopped = true;
    this.clearRetry();
    this.clearHandshake();
    const ws = this.socket;
    this.socket = null;
    ws?.close();
    this.failAll(err ?? new RelayError("closed", "Disconnected from your instance"));
    this.setStatus("closed", err);
  }

  private open(): void {
    this.setStatus(this.attempts === 0 ? "connecting" : "reconnecting");
    let ws: WebSocket;
    try {
      ws = this.opts.socketFactory ? this.opts.socketFactory(this.opts.wsUrl) : new WebSocket(this.opts.wsUrl);
    } catch {
      this.scheduleRetry();
      return;
    }
    this.socket = ws;
    ws.onopen = () => {
      if (this.socket !== ws) return;
      this.armHandshake(ws);
      this.handshake = "hello";
      this.sendFrame(ws, { t: "hello", role: "client", token: this.opts.token });
    };
    ws.onmessage = (ev) => {
      if (this.socket === ws) this.receive(String(ev.data));
    };
    ws.onclose = () => {
      if (this.socket !== ws) return;
      this.socket = null;
      this.clearHandshake();
      this.scheduleRetry();
    };
  }

  private scheduleRetry(): void {
    this.failAll(new RelayError("closed", "Connection to your instance was lost"));
    if (this.stopped) return;
    this.attempts += 1;
    const delay = Math.min(this.reconnect.maxMs, this.reconnect.baseMs * 2 ** (this.attempts - 1));
    this.setStatus("reconnecting", new RelayError("unreachable", "Can't reach your instance. Retrying."));
    this.retryHandle = this.timers.set(() => {
      this.retryHandle = null;
      this.open();
    }, delay);
  }

  private clearRetry(): void {
    if (this.retryHandle !== null) {
      this.timers.clear(this.retryHandle);
      this.retryHandle = null;
    }
  }

  /** A socket that does not finish hello and attach in time is dropped and retried. */
  private armHandshake(ws: WebSocket): void {
    this.clearHandshake();
    this.handshakeTimer = this.timers.set(() => {
      this.handshakeTimer = null;
      if (this.socket !== ws) return;
      this.socket = null;
      this.handshake = null;
      ws.close();
      this.scheduleRetry();
    }, HANDSHAKE_TIMEOUT_MS);
  }

  private clearHandshake(): void {
    if (this.handshakeTimer !== null) {
      this.timers.clear(this.handshakeTimer);
      this.handshakeTimer = null;
    }
    this.handshake = null;
  }

  private sendFrame(ws: WebSocket, frame: object): void {
    try {
      ws.send(JSON.stringify(frame));
    } catch {
      // The socket is dead. Its close, or the handshake timer, schedules the retry.
    }
  }

  private transmit(method: string, params: unknown, timeoutMs?: number): Promise<unknown> {
    const id = String(this.nextId++);
    const limit = timeoutMs ?? this.requestTimeoutMs;
    return new Promise<unknown>((resolve, reject) => {
      const ws = this.socket;
      if (!ws) {
        reject(new RelayError("offline", "Not connected to your instance"));
        return;
      }
      const timer =
        limit > 0
          ? this.timers.set(() => {
              if (this.pending.delete(id)) {
                reject(new RelayError("timeout", `${method} did not answer in time`));
              }
            }, limit)
          : null;
      this.pending.set(id, { resolve, reject, timer });
      try {
        ws.send(JSON.stringify({ id, method, params: params ?? null }));
      } catch {
        this.pending.delete(id);
        if (timer !== null) this.timers.clear(timer);
        reject(new RelayError("unreachable", `${method} could not be sent`));
      }
    });
  }

  private flushQueue(): void {
    const items = this.queue;
    this.queue = [];
    for (const q of items) {
      this.transmit(q.method, q.params, q.timeoutMs).then(q.resolve, q.reject);
    }
  }

  private receive(raw: string): void {
    const msg = parseFrame(raw);
    if (!msg) return;
    if (typeof msg.t === "string") {
      this.control(msg);
      return;
    }
    if (msg.method === "thread.event") {
      this.opts.onEvent(msg.params);
      return;
    }
    if (msg.id === undefined) return;
    const id = String(msg.id);
    const p = this.pending.get(id);
    if (!p) return;
    this.pending.delete(id);
    if (p.timer !== null) this.timers.clear(p.timer);
    if (msg.error) {
      const text = typeof msg.error === "string" ? msg.error : msg.error.message;
      p.reject(new RelayError("rpc", text ?? "The daemon returned an error"));
    } else {
      p.resolve(msg.result);
    }
  }

  private control(msg: Frame): void {
    switch (msg.t) {
      case "ack":
        this.onAck(msg);
        return;
      case "bye":
        // Revoked means this token is dead. Other reasons (replaced, shutdown)
        // close the socket, and the close schedules the normal retry.
        if (msg.reason === "revoked") this.halt(unpairedError());
        return;
      case "status":
        this.reportDaemon(msg);
        if (msg.resync) this.opts.onResync?.();
        return;
      default:
        return;
    }
  }

  /** Passes on a daemon presence value. Unknown values are ignored, not guessed at. */
  private reportDaemon(msg: Frame): void {
    if (msg.daemon === "online" || msg.daemon === "offline") this.opts.onDaemon?.(msg.daemon);
  }

  /** Advances the handshake: hello ok sends attach; attach ok means open. */
  private onAck(msg: Frame): void {
    const ws = this.socket;
    if (!ws || this.handshake === null) return;
    if (msg.ok !== true) {
      // A refused token is final. Other refusals make the relay close the
      // socket, and the close schedules the retry.
      if (msg.error === "bad_token") this.halt(unpairedError());
      return;
    }
    if (this.handshake === "hello") {
      this.handshake = "attach";
      this.sendFrame(ws, { t: "attach" });
      return;
    }
    this.clearHandshake();
    this.attempts = 0;
    this.reportDaemon(msg);
    this.setStatus("open");
    this.opts.onOpen?.();
    this.flushQueue();
  }

  private failAll(err: RelayError): void {
    for (const [id, p] of this.pending) {
      if (p.timer !== null) this.timers.clear(p.timer);
      this.pending.delete(id);
      p.reject(err);
    }
    const queued = this.queue;
    if (this.stopped) {
      this.queue = [];
      for (const q of queued) q.reject(err);
    }
  }

  private setStatus(status: RpcStatus, error?: RelayError): void {
    this.status = status;
    this.opts.onStatus(status, error);
  }
}
