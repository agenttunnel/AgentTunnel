import { useCallback, useEffect, useState } from "react";
import type { Rpc } from "../lib/rpc";
import { switchProps } from "../lib/switchProps";

/** fs.browse's reply: the subdirectories of one folder on the daemon's machine. */
type Listing = {
  path: string;
  parent?: string;
  home?: string;
  entries: { name: string; path: string }[];
  truncated?: boolean;
};

/**
 * FolderBrowser picks a project folder in the web UI. A browser's own picker
 * cannot name a path on the daemon's machine, so the daemon lists folders
 * (fs.browse: names only, never files) and the user walks to one. The desktop
 * shell keeps its native picker.
 */
export function FolderBrowser({
  rpc,
  onPick,
  onClose,
}: {
  rpc: Rpc | null;
  onPick: (path: string) => void;
  onClose: () => void;
}) {
  const [listing, setListing] = useState<Listing | null>(null);
  const [typed, setTyped] = useState("");
  const [hidden, setHidden] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const browse = useCallback(
    async (path: string, showHidden: boolean) => {
      if (!rpc) return;
      setLoading(true);
      try {
        const next = await rpc.call<Listing>("fs.browse", { path, hidden: showHidden });
        setListing(next);
        setTyped(next.path);
        setError(null);
      } catch (e) {
        // Keep the last good listing on screen; the message says what failed.
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setLoading(false);
      }
    },
    [rpc],
  );

  // Open at the home directory.
  useEffect(() => {
    void browse("", false);
  }, [browse]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !e.isComposing) {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const here = listing?.path ?? "";
  const toggleHidden = () => {
    setHidden(!hidden);
    if (here) void browse(here, !hidden);
  };

  return (
    <div className="overlay" onClick={onClose}>
      <div
        className="sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="folder-browser-title"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="sheet-head">
          <strong id="folder-browser-title">Add project</strong>
          <button onClick={onClose}>Cancel</button>
        </div>
        <div className="sheet-body">
          <p className="hint">Pick the project folder on the daemon's machine.</p>
          <div style={{ display: "flex", gap: 6 }}>
            <button
              type="button"
              disabled={!listing?.parent || loading}
              onClick={() => listing?.parent && browse(listing.parent, hidden)}
              title="Parent folder"
            >
              ↑
            </button>
            <button
              type="button"
              disabled={!listing?.home || loading}
              onClick={() => listing?.home && browse(listing.home, hidden)}
              title="Home folder"
            >
              ~
            </button>
            <input
              autoFocus
              value={typed}
              placeholder="/path/to/your/repo"
              aria-label="Folder path"
              style={{ flex: 1 }}
              onChange={(e) => setTyped(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") void browse(typed.trim(), hidden);
              }}
            />
          </div>
          {error && <p className="err-text">{error}</p>}
          <div
            role="listbox"
            aria-label="Folders"
            style={{
              maxHeight: 320,
              overflowY: "auto",
              margin: "10px 0",
              border: "1px solid var(--border)",
              borderRadius: 6,
            }}
          >
            {listing && listing.entries.length === 0 && <p className="hint" style={{ padding: 8 }}>No folders here.</p>}
            {listing?.entries.map((d) => (
              <button
                key={d.path}
                type="button"
                role="option"
                aria-selected={false}
                style={{ display: "block", width: "100%", textAlign: "left", border: 0, borderRadius: 0 }}
                onClick={() => browse(d.path, hidden)}
              >
                {d.name}/
              </button>
            ))}
            {listing?.truncated && <p className="hint" style={{ padding: 8 }}>Too many folders to list; type a path instead.</p>}
          </div>
          <div className={`toggle ${hidden ? "on" : ""}`} {...switchProps(hidden, toggleHidden)}>
            <span className="sw" /> Show hidden folders
          </div>
          <div className="sheet-actions">
            <button className="primary" disabled={!here || loading} onClick={() => onPick(here)}>
              Add {here ? here.split(/[\\/]/).filter(Boolean).pop() ?? here : "folder"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
