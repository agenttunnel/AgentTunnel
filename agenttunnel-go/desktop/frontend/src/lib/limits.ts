// Subscription usage left, as the daemon's usage.limits RPC reports it.

export type LimitWindow = {
  id: string;
  label: string;
  /** 0..100; the daemon clamps it. */
  usedPercent: number;
  /** Absent when the window has no reset time. */
  resetsAt?: string;
};

export type HarnessLimits = {
  harness: string;
  available: boolean;
  /** Why no windows are shown. Never contains credentials. */
  reason?: string;
  windows?: LimitWindow[];
  fetchedAt: string;
};

const HARNESS_NAMES: Record<string, string> = { claude: "Claude", codex: "Codex", cursor: "Cursor" };

export function harnessName(harness: string): string {
  return HARNESS_NAMES[harness] ?? harness;
}

/** formatReset words the time until a window resets: "resets in 2h 10m" within a
 * day, "resets Thu 14:00" beyond it. Empty when the window has no reset time. */
export function formatReset(resetsAt: string | undefined, now: number): string {
  if (!resetsAt) return "";
  const at = new Date(resetsAt).getTime();
  if (Number.isNaN(at)) return "";
  const minutes = Math.round((at - now) / 60_000);
  if (minutes <= 0) return "resetting";
  if (minutes < 24 * 60) {
    const h = Math.floor(minutes / 60);
    const m = minutes % 60;
    return h === 0 ? `resets in ${m}m` : `resets in ${h}h ${m}m`;
  }
  const day = new Date(at).toLocaleDateString(undefined, { weekday: "short" });
  const time = new Date(at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false });
  return `resets ${day} ${time}`;
}

/** levelOf maps a used percentage to the bar's warning state. */
export function levelOf(usedPercent: number): "" | "warn" | "err" {
  if (usedPercent >= 95) return "err";
  if (usedPercent >= 80) return "warn";
  return "";
}
