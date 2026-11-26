// The titlebar's navigation pieces: the breadcrumb that always says where you
// are, the palette button, and the status popover that holds the environment
// facts which used to sit in the header as chips.

import { memo } from "react";
import { MOD } from "../lib/navKeys";
import { STATUS_LABEL, type ThreadStatus } from "../lib/threadStatus";
import { Popover } from "./Popover";

export type Crumb = { label: string; title?: string; onClick?: () => void };

/** Environment page or project > thread, with the thread's status last. */
export const Breadcrumb = memo(function Breadcrumb({
  crumbs,
  status,
}: {
  crumbs: Crumb[];
  status?: ThreadStatus | null;
}) {
  const label = status ? STATUS_LABEL[status] : "";
  return (
    <nav className="crumbs" aria-label="Location">
      {crumbs.map((c, i) => {
        const last = i === crumbs.length - 1;
        return (
          <span key={i} className={`crumb${last ? " last" : ""}`}>
            {i > 0 && (
              <span className="crumb-sep" aria-hidden="true">
                /
              </span>
            )}
            {c.onClick && !last ? (
              <button type="button" className="crumb-link" title={c.title ?? c.label} onClick={c.onClick}>
                {c.label}
              </button>
            ) : (
              <span className="crumb-text" title={c.title ?? c.label} aria-current={last ? "page" : undefined}>
                {c.label}
              </span>
            )}
          </span>
        );
      })}
      {status && label && <span className={`status-chip ${status}`}>{label}</span>}
    </nav>
  );
});

/** Undo and redo arrows at the titlebar's left. Each names the change it
 * would step over, so the tooltip says what the next press does. */
export function HistoryButtons({
  canUndo,
  canRedo,
  undoLabel,
  redoLabel,
  onUndo,
  onRedo,
}: {
  canUndo: boolean;
  canRedo: boolean;
  undoLabel: string;
  redoLabel: string;
  onUndo: () => void;
  onRedo: () => void;
}) {
  return (
    <div className="history-nav">
      <button
        type="button"
        className="history-btn"
        disabled={!canUndo}
        title={canUndo ? `Undo ${undoLabel} (${MOD}Z)` : "Nothing to undo"}
        aria-label="Undo"
        onClick={onUndo}
      >
        <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <polyline points="9 14 4 9 9 4" />
          <path d="M20 20v-7a4 4 0 0 0-4-4H4" />
        </svg>
      </button>
      <button
        type="button"
        className="history-btn"
        disabled={!canRedo}
        title={canRedo ? `Redo ${redoLabel} (${MOD === "⌘" ? "⇧⌘Z" : "Ctrl+Y"})` : "Nothing to redo"}
        aria-label="Redo"
        onClick={onRedo}
      >
        <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <polyline points="15 14 20 9 15 4" />
          <path d="M4 20v-7a4 4 0 0 1 4-4h12" />
        </svg>
      </button>
    </div>
  );
}

/** Opens the command palette; it is the way to jump anywhere. */
export function PaletteButton({ onOpen }: { onOpen: () => void }) {
  return (
    <button type="button" className="palette-btn" onClick={onOpen} title={`Jump to anything (${MOD}K)`}>
      <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
        <circle cx="11" cy="11" r="7" />
        <path d="m20 20-3.5-3.5" />
      </svg>
      <span className="palette-btn-label">Search</span>
      <kbd>{MOD}K</kbd>
    </button>
  );
}

type ConnState = "connecting" | "online" | "reconnecting" | "offline";

/** The connection light, which opens the environment details. Only states the
 * user can act on (reconnecting, attached to another daemon) show as text. */
export function StatusPopover({
  status,
  attached,
  host,
  harnesses,
  roles,
  onSettings,
}: {
  status: ConnState;
  attached: boolean;
  host: string;
  harnesses: string[];
  roles: number;
  onSettings: () => void;
}) {
  const connLabel =
    status === "online" ? "Connected" : status === "reconnecting" ? "Reconnecting…" : status === "offline" ? "Offline" : "Connecting…";
  return (
    <Popover
      className="status-pop"
      title={`${connLabel} · ${attached ? `attached to ${host || "a remote daemon"}` : "local daemon"}`}
      label="Environment status"
      trigger={
        <>
          <span className={`led ${status}${attached ? " attached" : ""}`} />
          {status === "reconnecting" && <span className="status-pop-text">Reconnecting…</span>}
          {attached && status !== "reconnecting" && <span className="status-pop-text host">{host || "remote"}</span>}
        </>
      }
    >
      {(close) => (
        <div className="status-body">
        <dl className="status-list">
          <div>
            <dt>Connection</dt>
            <dd>{connLabel}</dd>
          </div>
          <div>
            <dt>Daemon</dt>
            <dd>{attached ? `Attached · ${host || "remote"}` : `Local${host ? ` · ${host}` : ""}`}</dd>
          </div>
          <div>
            <dt>Harnesses</dt>
            <dd title={harnesses.join(", ")}>
              {harnesses.length} enabled{harnesses.length ? ` · ${harnesses.join(", ")}` : ""}
            </dd>
          </div>
          <div>
            <dt>Roles</dt>
            <dd>{roles}</dd>
          </div>
        </dl>
          <button
            type="button"
            className="status-settings"
            onClick={() => {
              close();
              onSettings();
            }}
          >
            Open settings
          </button>
        </div>
      )}
    </Popover>
  );
}
