/**
 * Client preferences shared by every client of one daemon.
 *
 * The daemon owns the saved pairs (prefs.json in its state dir) and serves
 * them at /prefs. The desktop shell and the web UI both seed localStorage from
 * the daemon before React runs, so reads below stay synchronous. Writes are
 * mirrored back to the daemon, so the next launch of any client starts from
 * the same values. Until the connection is known, writes wait in memory.
 */

const FLUSH_MS = 250;

type Endpoint = { httpUrl: string; token: string };

let endpoint: Endpoint | null = null;
let pending = new Map<string, string | null>();
let timer: number | undefined;

/** enablePrefMirror starts mirroring writes to the daemon this connection
 * points at. Writes made while the app was still connecting flush now. */
export function enablePrefMirror(conn: Endpoint): void {
  endpoint = conn;
  if (pending.size > 0) schedule();
}

/** getPref reads one preference; private mode and unavailable storage read null. */
export function getPref(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

/** setPref writes one preference and schedules a mirror to the daemon. */
export function setPref(key: string, value: string): void {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // No storage means no way to read it back, and mirroring would overwrite a
    // good saved value with a default.
    return;
  }
  queue(key, value);
}

/** setLocalPref writes a preference that stays on this client. Drafts use it:
 * they change on every keystroke, and sending them to the daemon would make
 * each pause in typing a file write. */
export function setLocalPref(key: string, value: string): void {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    /* private mode: the draft lasts this session */
  }
}

/** removePref deletes one preference and schedules the deletion on the daemon. */
export function removePref(key: string): void {
  try {
    window.localStorage.removeItem(key);
  } catch {
    return;
  }
  queue(key, null);
}

function queue(key: string, value: string | null): void {
  pending.set(key, value);
  if (endpoint) schedule();
}

function schedule(): void {
  if (timer !== undefined) return;
  timer = window.setTimeout(flush, FLUSH_MS);
}

function flush(): void {
  timer = undefined;
  if (!endpoint || pending.size === 0) return;
  const changes: Record<string, string | null> = {};
  for (const [k, v] of pending) changes[k] = v;
  pending = new Map();
  const { httpUrl, token } = endpoint;
  // keepalive lets the last edit leave during pagehide, when the window closes.
  void fetch(`${httpUrl}/prefs`, {
    method: "POST",
    headers: { "content-type": "application/json", authorization: `Bearer ${token}` },
    body: JSON.stringify(changes),
    keepalive: true,
  }).catch(() => {
    /* the daemon is gone; the next launch seeds from the last successful save */
  });
}

window.addEventListener("pagehide", () => {
  if (timer !== undefined) {
    clearTimeout(timer);
    timer = undefined;
  }
  flush();
});
