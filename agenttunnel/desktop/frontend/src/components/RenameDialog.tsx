// The thread rename sheet. The title being typed is local, so a keystroke
// re-renders this sheet only.

import { memo, useState } from "react";
import { useEscapeClose } from "../lib/escape";

export const RenameDialog = memo(function RenameDialog({
  initial,
  onRename,
  onCancel,
}: {
  initial: string;
  /** Called with the trimmed title; an empty title does nothing. */
  onRename: (title: string) => void;
  onCancel: () => void;
}) {
  const [renameValue, setRenameValue] = useState(initial);
  const confirm = () => {
    const title = renameValue.trim();
    if (title) onRename(title);
  };
  useEscapeClose(onCancel);
  return (
    <div className="overlay" onClick={onCancel}>
      <div className="sheet" onClick={(e) => e.stopPropagation()}>
        <div className="sheet-head">
          <strong>Rename thread</strong>
          <button onClick={onCancel}>Cancel</button>
        </div>
        <div className="sheet-body">
          <input
            autoFocus
            value={renameValue}
            placeholder="Thread title"
            onChange={(e) => setRenameValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") confirm();
            }}
          />
          <div className="sheet-actions">
            <button className="primary" onClick={confirm}>
              Rename
            </button>
          </div>
        </div>
      </div>
    </div>
  );
});
