import { harnessName } from "../lib/limits";
import { pendingUpdates, type HarnessUpdateReport } from "../lib/harnessUpdates";

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
