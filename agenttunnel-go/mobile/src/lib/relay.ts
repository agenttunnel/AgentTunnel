// Relay transport: pairing and the JSON-RPC-over-WebSocket client the phone
// uses to drive a user's instance. The RPC surface is the same one the desktop
// client speaks (desktop/frontend/src/lib/rpc.ts over internal/transport):
// request {id, method, params}, reply {id, result|error}, and pushed
// {method: "thread.event", params} frames on the same socket.

export type RelayErrorCode =
  | "unpublished" // the pairing exchange is not available in this build
  | "rejected" // the relay refused the pairing code
  | "unreachable" // the socket could not open or dropped
  | "offline" // a read was attempted while not connected
  | "timeout" // no reply within the request timeout
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
  relayUrl: string;
  wsUrl: string;
  token: string;
};

/** Trims and checks a relay base URL. Only http(s) is accepted. */
export function normalizeRelayUrl(input: string): string {
  const trimmed = input.trim().replace(/\/+$/, "");
  if (!/^https?:\/\/[^\s/]+/i.test(trimmed)) {
    throw new RelayError("rejected", "Enter the relay address, starting with https://");
  }
  return trimmed;
}

/** Pairing codes are typed by hand: spaces and dashes are ignored, case is not. */
export function normalizePairCode(input: string): string {
  const code = input.replace(/[\s-]/g, "").toUpperCase();
  if (code.length === 0) throw new RelayError("rejected", "Enter the pairing code shown on your desktop");
  return code;
}

/**
 * Exchanges a pairing code for the instance's WebSocket URL and token.
 *
 * The relay's pairing wire format is not published yet (no PROTOCOL note exists
 * in the repo). This is the single place that call lives: until the design
 * lands it fails with a typed "unpublished" error, so the Pair screen shows the
 * gap instead of guessing a contract.
 */
export async function exchangePairCode(relayUrl: string, code: string): Promise<Pairing> {
  normalizeRelayUrl(relayUrl);
  normalizePairCode(code);
  throw new RelayError(
    "unpublished",
    "Pairing is not available yet: the relay protocol has not been published.",
  );
}

// ---------- JSON-RPC client ----------

export type RpcStatus = "idle" | "connecting" | "open" | "reconnecting" | "closed";

export type RpcTimers = {
  set(fn: () => void, ms: number): unknown;
  clear(handle: unknown): void;
};

export type RpcOptions = {
  wsUrl: string;
  token: string;
  /** Receives every pushed thread.event frame's params. */
  onEvent: (frame: unknown) => void;
  /** Fires on every status change. `error` is set when the change is a failure. */
  onStatus: (status: RpcStatus, error?: RelayError) => void;
  /** Fires after each (re)connect, before queued sends flush. Resubscribe here. */
  onOpen?: () => void;
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

const defaultTimers: RpcTimers = {
  set: (fn, ms) => setTimeout(fn, ms),
  clear: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

export class RpcClient {
  private socket: WebSocket | null = null;
  private status: RpcStatus = "idle";
  private nextId = 1;
  private attempts = 0;
  private stopped = false;
  private retryHandle: unknown = null;
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
    this.stopped = true;
    if (this.retryHandle !== null) {
      this.timers.clear(this.retryHandle);
      this.retryHandle = null;
    }
    const ws = this.socket;
    this.socket = null;
    ws?.close();
    this.failAll(new RelayError("closed", "Disconnected from your instance"));
    this.setStatus("closed");
  }

  private open(): void {
    this.setStatus(this.attempts === 0 ? "connecting" : "reconnecting");
    const url = `${this.opts.wsUrl}?token=${encodeURIComponent(this.opts.token)}`;
    let ws: WebSocket;
    try {
      ws = this.opts.socketFactory ? this.opts.socketFactory(url) : new WebSocket(url);
    } catch {
      this.scheduleRetry();
      return;
    }
    this.socket = ws;
    ws.onopen = () => {
      if (this.socket !== ws) return;
      this.attempts = 0;
      this.setStatus("open");
      this.opts.onOpen?.();
      this.flushQueue();
    };
    ws.onmessage = (ev) => {
      if (this.socket === ws) this.receive(String(ev.data));
    };
    ws.onclose = () => {
      if (this.socket !== ws) return;
      this.socket = null;
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
    let msg: { id?: string; result?: unknown; error?: { message?: string }; method?: string; params?: unknown };
    try {
      msg = JSON.parse(raw) as typeof msg;
    } catch {
      return;
    }
    if (!msg || typeof msg !== "object") return;
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
    if (msg.error) p.reject(new RelayError("rpc", msg.error.message ?? "The daemon returned an error"));
    else p.resolve(msg.result);
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
