import { useCallback, useEffect, useState } from "react";
import type { Rpc } from "../lib/rpc";
import { formatReset, harnessName, levelOf, type HarnessLimits } from "../lib/limits";

// The daemon caches each harness for 60s, so polling more often gains nothing.
const POLL_MS = 5 * 60_000;
// Reset countdowns are minute-precision; re-render once a minute to keep them true.
const CLOCK_MS = 60_000;

/**
 * LimitsMeter shows subscription usage left for harnesses that report it. It
 * renders nothing when no harness is available, which is the common case for
 * users without a subscription.
 */
export function LimitsMeter({ rpc }: { rpc: Rpc | null }) {
  const [limits, setLimits] = useState<HarnessLimits[]>([]);
  const [now, setNow] = useState(() => Date.now());
  // Default expanded; only harnesses the user collapsed are recorded.
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});

  const load = useCallback(
    (refresh: boolean) => {
      if (!rpc) return;
      rpc
        .call<HarnessLimits[]>("usage.limits", { refresh })
        .then(setLimits)
        .catch(() => {
          /* keep the last reading; a failed poll must not blank the meter */
        });
    },
    [rpc],
  );

  useEffect(() => {
    load(false);
    const poll = window.setInterval(() => {
      if (document.visibilityState === "visible") load(false);
    }, POLL_MS);
    const clock = window.setInterval(() => setNow(Date.now()), CLOCK_MS);
    return () => {
      window.clearInterval(poll);
      window.clearInterval(clock);
    };
  }, [load]);

  const shown = limits.filter((h) => h.available && h.windows && h.windows.length > 0);
  if (shown.length === 0) return null;

  return (
    <div className="limits">
      {shown.map((h) => {
        const open = !collapsed[h.harness];
        const rowsId = `limits-${h.harness}`;
        return (
          <div key={h.harness} className="limits-harness" title={`Checked ${new Date(h.fetchedAt).toLocaleTimeString()}`}>
            <div className="limits-head">
              <button
                type="button"
                className="limits-toggle"
                aria-expanded={open}
                aria-controls={rowsId}
                onClick={() => setCollapsed((c) => ({ ...c, [h.harness]: open }))}
              >
                <span className="limits-caret" aria-hidden="true">
                  {open ? "▾" : "▸"}
                </span>
                <span>{harnessName(h.harness)} usage</span>
              </button>
              <button type="button" className="limits-refresh" onClick={() => load(true)} title="Refresh usage">
                ↻
              </button>
            </div>
            <div id={rowsId} hidden={!open}>
              {h.windows?.map((w) => (
                <div key={w.id} className={`limit-row ${levelOf(w.usedPercent)}`}>
                  <span className="limit-label">{w.label}</span>
                  <span className="limit-bar" aria-hidden="true">
                    <i style={{ transform: `scaleX(${w.usedPercent / 100})` }} />
                  </span>
                  <span className="limit-value">
                    {Math.round(w.usedPercent)}%{formatReset(w.resetsAt, now) ? ` · ${formatReset(w.resetsAt, now)}` : ""}
                  </span>
                </div>
              ))}
            </div>
          </div>
        );
      })}
    </div>
  );
}
