import { memo, useCallback, useEffect, useState } from "react";
import type { Rpc } from "../lib/rpc";

/** How often a connected app asks for a newer release. The daemon memoizes the
 * GitHub lookup for about 10 minutes and shares it across clients, so polling
 * here costs GitHub at most one call per window, however many tabs are open. */
const UPDATE_CHECK_MS = 5 * 60_000;

/** AppUpdate checks for a release once connected and every UPDATE_CHECK_MS,
 * and installs on click. Its state is its own, so a check never re-renders
 * the app. */
export const AppUpdate = memo(function AppUpdate({ rpc }: { rpc: Rpc | null }) {
  const [update, setUpdate] = useState<UpdateStatus | null>(null);
  const [updateBusy, setUpdateBusy] = useState(false);
  const [updateError, setUpdateError] = useState<string | null>(null);
  // A check keeps the "restart to run" state after an install; the daemon's
  // plain check result does not know about it.
  useEffect(() => {
    if (!rpc) return;
    const check = () =>
      rpc
        .call<UpdateStatus>("update.check")
        .then((s) => setUpdate((prev) => (prev?.installed ? prev : s)))
        .catch(() => {});
    // Not awaited: a slow GitHub lookup must not hold up startup.
    check();
    const id = window.setInterval(check, UPDATE_CHECK_MS);
    return () => window.clearInterval(id);
  }, [rpc]);

  const installUpdate = useCallback(async () => {
    if (!rpc) return;
    setUpdateBusy(true);
    setUpdateError(null);
    try {
      setUpdate(await rpc.call<UpdateStatus>("update.apply"));
    } catch (e) {
      setUpdateError(e instanceof Error ? e.message : String(e));
    } finally {
      setUpdateBusy(false);
    }
  }, [rpc]);

  return <UpdateChip status={update} busy={updateBusy} error={updateError} onInstall={installUpdate} />;
});

// What the daemon reports about updates (update.check / update.apply).
export interface UpdateStatus {
  current: string;
  latest?: string;
  enabled: boolean;
  available: boolean;
  installed?: string;
}

interface Props {
  status: UpdateStatus | null;
  busy: boolean;
  error: string | null;
  onInstall: () => void;
}

// Shown in the titlebar only when a newer release exists. Installing replaces
// files on disk but never restarts the app, so after an install the chip asks
// for a restart instead.
export function UpdateChip({ status, busy, error, onInstall }: Props) {
  if (!status) return null;
  if (status.installed) {
    return <span className="update-chip done">Restart to run {status.installed}</span>;
  }
  if (!status.available || !status.latest) return null;
  return (
    <button
      type="button"
      className="update-chip"
      onClick={onInstall}
      disabled={busy}
      title={error ?? `Install ${status.latest}`}
    >
      {busy ? "Installing…" : error ? "Update failed" : `Update to ${status.latest}`}
    </button>
  );
}
