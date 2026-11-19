import { Events } from "@wailsio/runtime";
import { enablePrefMirror } from "./prefs";

export type Conn = {
  wsUrl: string;
  httpUrl: string;
  token: string;
  /** A project directory passed on the command line (agenttunnel <dir>): open
   * a thread in it once connected. Empty for a plain launch. */
  launchDir?: string;
  /** A thread id passed on the command line (agenttunnel open): select that
   * thread once connected. Empty for a plain launch. */
  launchThread?: string;
  /** Host:port of the daemon, for the "local" vs "attached" header badge. */
  host?: string;
  /** True when the window attached to a daemon it did not start. */
  attached?: boolean;
  /** The shell's saved keep-running preference. */
  keepRunning?: boolean;
};

type Pending = { resolve: (v: unknown) => void; reject: (e: Error) => void };
type ThreadEventHandler = (event: unknown) => void;
type AppOpenHandler = (params: { threadId?: string; projectId?: string }) => void;
type NotificationHandler = (notification: { method: string; params: unknown }) => void;

/** online: the socket is open. reconnecting: it dropped and a retry is pending
 * or running. closed: close() was called; nothing reconnects. */
export type RpcStatus = "online" | "reconnecting" | "closed";

/** Reconnect backoff: the first retry comes quickly, then doubles to a cap. */
const RECONNECT_BASE_MS = 500;
const RECONNECT_MAX_MS = 15_000;

/**
 * Rpc is a tiny JSON-RPC-over-WebSocket client. It is the same surface any
 * client uses: send a typed request, await a matching reply, and receive
 * streamed `thread.event` pushes on the same socket.
 *
 * A dropped socket reconnects with backoff. Subscriptions are replayed on the
 * new socket, and onReconnect handlers resync whatever they show. Calls made
 * while the socket is down reject at once.
 */
export class Rpc {
  private ws?: WebSocket;
  private nextId = 1;
  private pending = new Map<string, Pending>();
  private handlers = new Set<ThreadEventHandler>();
  private appOpenHandlers = new Set<AppOpenHandler>();
  private notificationHandlers = new Set<NotificationHandler>();
  private closeHandlers = new Set<() => void>();
  private statusHandlers = new Set<(s: RpcStatus) => void>();
  private reconnectHandlers = new Set<() => void>();
  /** Threads this client follows; replayed on every new socket. */
  private subscriptions = new Set<string>();
  /** Threads subscribed on the current socket: a repeat subscribe would make
   * the daemon send every frame twice. */
  private subscribedHere = new Set<string>();
  private status: RpcStatus = "online";
  private attempt = 0;
  private retryTimer?: number;
  private retrying = false;

  /**
   * `rediscover` re-reads the connection before each reconnect attempt: a
   * restarted daemon may listen on a new port with a new token. It resolves
   * null to keep the current connection.
   */
  constructor(
    private conn: Conn,
    private readonly rediscover?: () => Promise<Conn | null>,
  ) {}

  /** The connection in use; it changes when a reconnect rediscovered one. */
  get connection(): Conn {
    return this.conn;
  }

  connect(): Promise<void> {
    return this.open();
  }

  private open(): Promise<void> {
    return new Promise((resolve, reject) => {
      const ws = new WebSocket(`${this.conn.wsUrl}?token=${encodeURIComponent(this.conn.token)}`);
      this.ws = ws;
      this.subscribedHere.clear();
      let opened = false;
      ws.onopen = () => {
        opened = true;
        resolve();
      };
      ws.onerror = () => {
        if (!opened) reject(new Error("Could not connect to the daemon"));
      };
      ws.onmessage = (ev) => this.onMessage(String(ev.data));
      ws.onclose = () => {
        if (this.ws !== ws) return;
        this.ws = undefined;
        // Calls in flight on this socket will never get a reply; reject them
        // so their callers settle instead of waiting forever.
        const lost = new Error("Lost the connection to the daemon");
        const pending = [...this.pending.values()];
        this.pending.clear();
        pending.forEach((p) => p.reject(lost));
        if (!opened) {
          reject(new Error("Could not connect to the daemon"));
          return;
        }
        this.closeHandlers.forEach((h) => h());
        this.scheduleReconnect();
      };
    });
  }

  private setStatus(next: RpcStatus) {
    if (this.status === next) return;
    this.status = next;
    this.statusHandlers.forEach((h) => h(next));
  }

  private scheduleReconnect() {
    if (this.status === "closed" || this.retrying || this.retryTimer !== undefined) return;
    this.setStatus("reconnecting");
    const delay = Math.min(RECONNECT_MAX_MS, RECONNECT_BASE_MS * 2 ** this.attempt);
    this.attempt++;
    this.retryTimer = window.setTimeout(() => {
      this.retryTimer = undefined;
      void this.reconnect();
    }, delay);
  }

  private async reconnect() {
    if (this.status !== "reconnecting" || this.retrying) return;
    this.retrying = true;
    try {
      const found = this.rediscover ? await this.rediscover().catch(() => null) : null;
      if (found) this.conn = found;
      await this.open();
    } catch {
      this.retrying = false;
      this.scheduleReconnect();
      return;
    }
    this.retrying = false;
    // close() may have run while the attempt was in flight.
    if ((this.status as RpcStatus) === "closed") {
      this.ws?.close();
      return;
    }
    this.attempt = 0;
    for (const threadId of this.subscriptions) {
      this.call("thread.subscribe", { threadId }).catch(() => undefined);
    }
    this.setStatus("online");
    this.reconnectHandlers.forEach((h) => h());
  }

  /** retryNow skips the backoff wait: the network came back, or the window
   * became visible again. A no-op while online. */
  retryNow() {
    if (this.status !== "reconnecting" || this.retrying) return;
    window.clearTimeout(this.retryTimer);
    this.retryTimer = undefined;
    void this.reconnect();
  }

  /** onClose fires each time an open connection drops, whoever closed it. */
  onClose(handler: () => void): () => void {
    this.closeHandlers.add(handler);
    return () => {
      this.closeHandlers.delete(handler);
    };
  }

  /** onStatus fires when the connection goes online, reconnecting, or closed. */
  onStatus(handler: (s: RpcStatus) => void): () => void {
    this.statusHandlers.add(handler);
    return () => {
      this.statusHandlers.delete(handler);
    };
  }

  /** onReconnect fires after a dropped socket is back and its subscriptions
   * are replayed. Frames sent while it was down are lost: resync here. */
  onReconnect(handler: () => void): () => void {
    this.reconnectHandlers.add(handler);
    return () => {
      this.reconnectHandlers.delete(handler);
    };
  }

  private onMessage(raw: string) {
    let msg: {
      id?: string;
      result?: unknown;
      error?: { message: string };
      method?: string;
      params?: unknown;
    };
    try {
      msg = JSON.parse(raw);
    } catch {
      return;
    }
    if (msg.method === "thread.event") {
      this.handlers.forEach((h) => h(msg.params));
      return;
    }
    if (msg.method === "app.open") {
      this.appOpenHandlers.forEach((h) => h((msg.params ?? {}) as { threadId?: string; projectId?: string }));
      return;
    }
    if (msg.method) {
      this.notificationHandlers.forEach((h) => h({ method: msg.method as string, params: msg.params }));
      return;
    }
    if (!msg.id) return;
    const pending = this.pending.get(msg.id);
    if (!pending) return;
    this.pending.delete(msg.id);
    if (msg.error) pending.reject(new Error(msg.error.message));
    else pending.resolve(msg.result);
  }

  call<T = unknown>(method: string, params?: unknown): Promise<T> {
    if (method === "thread.subscribe") {
      const threadId = (params as { threadId?: string } | undefined)?.threadId;
      if (threadId) {
        // Remembered even while offline: the reconnect replays it.
        this.subscriptions.add(threadId);
        if (this.subscribedHere.has(threadId)) return Promise.resolve({ subscribed: threadId } as T);
      }
    }
    const id = String(this.nextId++);
    return new Promise<T>((resolve, reject) => {
      if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
        reject(new Error("daemon connection is not open"));
        return;
      }
      this.pending.set(id, {
        resolve: resolve as (v: unknown) => void,
        reject,
      });
      if (method === "thread.subscribe") {
        const threadId = (params as { threadId?: string } | undefined)?.threadId;
        if (threadId) this.subscribedHere.add(threadId);
      }
      this.ws.send(JSON.stringify({ id, method, params: params ?? null }));
    });
  }

  onThreadEvent(handler: ThreadEventHandler): () => void {
    this.handlers.add(handler);
    return () => {
      this.handlers.delete(handler);
    };
  }

  /** onAppOpen subscribes to app.open pushes: another `agenttunnel <dir>`
   * invocation forwarded a thread to this running instance. */
  onAppOpen(handler: AppOpenHandler): () => void {
    this.appOpenHandlers.add(handler);
    return () => {
      this.appOpenHandlers.delete(handler);
    };
  }

  /** onNotification subscribes to server pushes that are not thread events.
   * Handlers see the method and params, so a client refreshes only what changed. */
  onNotification(handler: NotificationHandler): () => void {
    this.notificationHandlers.add(handler);
    return () => {
      this.notificationHandlers.delete(handler);
    };
  }

  close() {
    this.setStatus("closed");
    window.clearTimeout(this.retryTimer);
    this.retryTimer = undefined;
    this.ws?.close();
  }

  /** retryOnWake reconnects at once when the browser comes back online or
   * the window becomes visible (a laptop waking), instead of waiting out the
   * backoff. Returns the cleanup. */
  retryOnWake(): () => void {
    const wake = () => {
      if (document.visibilityState === "visible") this.retryNow();
    };
    window.addEventListener("online", wake);
    document.addEventListener("visibilitychange", wake);
    return () => {
      window.removeEventListener("online", wake);
      document.removeEventListener("visibilitychange", wake);
    };
  }
}

/** One image attached to a message: enough to render and refetch the bytes.
 * Mirrors contracts.ImageRef on the daemon — never the bytes themselves. */
export type ImageRef = {
  id: string;
  name?: string;
  mime: string;
  size: number;
  width?: number;
  height?: number;
};

/** Reads a failed /attachments response: the daemon replies with a short
 * plain-language message (bad mime, too large, unknown thread). */
async function attachmentsError(res: Response): Promise<Error> {
  let detail = "";
  try {
    detail = (await res.text()).trim();
  } catch {
    /* fall through to the status line */
  }
  return new Error(detail || `attachment upload failed (${res.status})`);
}

/**
 * uploadImage POSTs raw image bytes to the daemon's HTTP /attachments
 * endpoint and resolves to the stored ImageRef. Images ride HTTP with Bearer
 * auth — they never go over the WebSocket or into the event log.
 */
export async function uploadImage(
  httpUrl: string,
  token: string,
  threadId: string,
  blob: Blob,
  name: string,
): Promise<ImageRef> {
  const res = await fetch(
    `${httpUrl}/attachments?threadId=${encodeURIComponent(threadId)}&name=${encodeURIComponent(name)}`,
    {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` },
      body: blob,
    },
  );
  if (!res.ok) throw await attachmentsError(res);
  return (await res.json()) as ImageRef;
}

/** fetchImageBlob GETs one stored image attachment's bytes for rendering. */
export async function fetchImageBlob(
  httpUrl: string,
  token: string,
  threadId: string,
  id: string,
): Promise<Blob> {
  const res = await fetch(
    `${httpUrl}/attachments?threadId=${encodeURIComponent(threadId)}&id=${encodeURIComponent(id)}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  if (!res.ok) throw await attachmentsError(res);
  return res.blob();
}

/** discover asks the desktop shell for the daemon's loopback address + token. */
export function discover(): Promise<Conn> {
  // Served as the daemon's web UI, /conn describes the same connection
  // same-origin; anything but the desktop shell reads it there. The
  // at:connection event can never fire in a plain browser, so a failed
  // handoff rejects with a reason the UI can show instead of falling back
  // to a wait that never settles.
  const found = inDesktopShell()
    ? discoverViaShell()
    : discoverOverHttp().catch((e) => {
        throw new Error(`web UI handoff failed: ${e instanceof Error ? e.message : String(e)}`);
      });
  // Both shells learn the daemon's address here, so both mirror preferences to it.
  return found.then((conn) => {
    enablePrefMirror(conn);
    return conn;
  });
}

/**
 * rediscover re-reads the connection for a reconnect. The web UI asks /conn
 * again, since a restarted daemon has a new token; the desktop shell's
 * connection does not change, so it resolves null there.
 */
export async function rediscover(): Promise<Conn | null> {
  if (inDesktopShell()) return null;
  const conn = await discoverOverHttp();
  enablePrefMirror(conn);
  return conn;
}

/** discoverViaShell is the desktop shell path: ask for an at:connection push. */
function discoverViaShell(): Promise<Conn> {
  return new Promise((resolve) => {
    Events.On("at:connection", (event: { data: Conn }) => resolve(event.data));
    try {
      Events.Emit("at:request", null);
    } catch {
      /* not running under the desktop shell */
    }
  });
}

/** discoverOverHttp reads the same-origin /conn the daemon web UI is served with. */
async function discoverOverHttp(): Promise<Conn> {
  const res = await fetch("/conn", { headers: { accept: "application/json" } });
  if (!res.ok) throw new Error(`/conn replied ${res.status}`);
  const conn = (await res.json()) as Conn;
  if (typeof conn.wsUrl !== "string" || typeof conn.token !== "string") {
    throw new Error("/conn is not a connection object");
  }
  return conn;
}

/** inDesktopShell mirrors the Wails runtime's own transport check: outside the shell there is no native bridge. */
export function inDesktopShell(): boolean {
  const w = window as Window & {
    chrome?: { webview?: unknown };
    webkit?: { messageHandlers?: { external?: unknown } };
  };
  return Boolean(w.chrome?.webview || w.webkit?.messageHandlers?.external);
}

/**
 * openExternal opens a web link in the user's default browser. The desktop shell
 * does it natively; a plain browser (headless daemon UI) opens a new tab.
 */
export function openExternal(url: string): void {
  if (!/^https?:\/\//.test(url)) return;
  if (inDesktopShell()) {
    Promise.resolve(Events.Emit("at:open-url", { url })).catch(() => window.open(url, "_blank", "noopener,noreferrer"));
    return;
  }
  window.open(url, "_blank", "noopener,noreferrer");
}

/**
 * installExternalLinks sends every http(s) link click to openExternal, so no link
 * navigates the AgentTunnel window. The capture listener runs before any component
 * handler and covers markdown, diffs, and target="_blank" links alike.
 */
export function installExternalLinks(): void {
  document.addEventListener(
    "click",
    (event) => {
      if (!(event.target instanceof Element)) return;
      const link = event.target.closest<HTMLAnchorElement>("a[href]");
      if (!link || (link.protocol !== "https:" && link.protocol !== "http:")) return;
      event.preventDefault();
      openExternal(link.href);
    },
    true,
  );
}

/**
 * setKeepRunning asks the desktop shell to persist whether its daemon should
 * outlive the window. A capability event, not daemon data: outside the shell it
 * is a no-op.
 */
export function setKeepRunning(enabled: boolean): void {
  if (!inDesktopShell()) return;
  try {
    Events.Emit("at:keep-running", { enabled });
  } catch {
    /* not running under the desktop shell */
  }
}

/**
 * pickFolder opens the native folder picker in the desktop shell and resolves to
 * the chosen path, or null when cancelled or when not running in the shell.
 */
export function pickFolder(): Promise<string | null> {
  // Outside the shell the emit is a no-op and the at:folder event never
  // comes; settle null at once instead of leaving the caller pending.
  if (!inDesktopShell()) return Promise.resolve(null);
  return new Promise((resolve) => {
    let done = false;
    const off = Events.On("at:folder", (event: { data?: { path?: string } }) => {
      if (done) return;
      done = true;
      if (typeof off === "function") off();
      resolve(event.data?.path ? event.data.path : null);
    });

    try {
      Events.Emit("at:pick-folder", null);
    } catch {
      done = true;
      resolve(null);
    }
  });
}
