import { memo, useCallback, useEffect, useState } from "react";
import { harnessName } from "../lib/limits";
import {
  pendingUpdates,
  type HarnessUpdateReport,
  type HarnessUpdateResult,
} from "../lib/harnessUpdates";
import type { Rpc } from "../lib/rpc";

/** HarnessUpdates reads the harness update report once connected and runs
 * the upgrade on click, holding its own state. */
export const HarnessUpdates = memo(function HarnessUpdates({ rpc }: { rpc: Rpc | null }) {
  const [harnessUpdates, setHarnessUpdates] = useState<HarnessUpdateReport | null>(null);
  const [harnessUpdateBusy, setHarnessUpdateBusy] = useState(false);
  const [harnessUpdateError, setHarnessUpdateError] = useState<string | null>(null);
  useEffect(() => {
    rpc?.call<HarnessUpdateReport>("harness.updates").then(setHarnessUpdates).catch(() => {});
  }, [rpc]);

  const updateHarnesses = useCallback(async () => {
    if (!rpc) return;
    setHarnessUpdateBusy(true);
    setHarnessUpdateError(null);
    try {
      const res = await rpc.call<HarnessUpdateResult>("harness.update", { harnesses: [] });
      setHarnessUpdates(res.report);
      if (res.failed && res.failed.length > 0) {
        setHarnessUpdateError(res.failed.map((f) => `${f.harness}: ${f.error}`).join("; "));
      }
    } catch (e) {
      setHarnessUpdateError(e instanceof Error ? e.message : String(e));
    } finally {
      setHarnessUpdateBusy(false);
    }
  }, [rpc]);

  return (
    <HarnessUpdateChip
      report={harnessUpdates}
      busy={harnessUpdateBusy}
      error={harnessUpdateError}
      onUpdate={updateHarnesses}
    />
  );
});

interface Props {
  report: HarnessUpdateReport | null;
  busy: boolean;
  error: string | null;
  onUpdate: () => void;
}

// Shown in the titlebar only when an installed harness is behind and can be
// upgraded. Upgrading runs the harness's own updater or package manager.
export function HarnessUpdateChip({ report, busy, error, onUpdate }: Props) {
  const pending = pendingUpdates(report);
  // A failed pass can drop the failed harness from the refreshed report, so
  // keep the chip visible when there is an error and nothing left to offer.
  if (pending.length === 0 && !error) return null;

  const versions = pending
    .map((h) => `${harnessName(h.harness)} ${h.installed ?? "?"} → ${h.latest ?? "?"}`)
    .join(", ");
  const label = busy
    ? "Updating…"
    : error
      ? "Harness update failed"
      : pending.length === 1
        ? `Update ${harnessName(pending[0].harness)}`
        : `Update ${pending.length} harnesses`;

  return (
    <button
      type="button"
      className="update-chip"
      onClick={onUpdate}
      disabled={busy}
      title={error ? (versions ? `${versions} — ${error}` : error) : versions}
    >
      {label}
    </button>
  );
}
