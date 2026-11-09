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
