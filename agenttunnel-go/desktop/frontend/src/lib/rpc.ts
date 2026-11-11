import { Events } from "@wailsio/runtime";

export type Conn = {
  wsUrl: string;
  httpUrl: string;
  token: string;
  /** A project directory passed on the command line (agenttunnel <dir>): open
   * a thread in it once connected. Empty for a plain launch. */
  launchDir?: string;
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

/**
 * Rpc is a tiny JSON-RPC-over-WebSocket client. It is the same surface any
 * client uses: send a typed request, await a matching reply, and receive
 * streamed `thread.event` pushes on the same socket.
 */
export class Rpc {
  private ws?: WebSocket;
  private nextId = 1;
  private pending = new Map<string, Pending>();
  private handlers = new Set<ThreadEventHandler>();
  private appOpenHandlers = new Set<AppOpenHandler>();
  private notificationHandlers = new Set<NotificationHandler>();

  constructor(private readonly conn: Conn) {}

  connect(): Promise<void> {
    return new Promise((resolve, reject) => {
      const ws = new WebSocket(`${this.conn.wsUrl}?token=${encodeURIComponent(this.conn.token)}`);
      this.ws = ws;
      ws.onopen = () => resolve();
      ws.onerror = () => reject(new Error("Could not connect to the daemon"));
      ws.onmessage = (ev) => this.onMessage(String(ev.data));
    });
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
    this.ws?.close();
  }
}

/** discover asks the desktop shell for the daemon's loopback address + token. */
export function discover(): Promise<Conn> {
  return new Promise((resolve) => {
    Events.On("at:connection", (event: { data: Conn }) => resolve(event.data));
    try {
      Events.Emit("at:request", null);
    } catch {
      /* not running under the desktop shell */
    }
  });
}

/** inDesktopShell mirrors the Wails runtime's own transport check: outside the shell there is no native bridge. */
function inDesktopShell(): boolean {
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
