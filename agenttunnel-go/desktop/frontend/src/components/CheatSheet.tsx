import { useEffect, useRef } from "react";
import { HOLD_MOD_LABEL } from "../lib/dictation";

const OPT_OUT_KEY = "at.cheatSheetOptedOut";

/** Whether the user chose "Do not show this again". Private mode reads as not opted out. */
export function loadCheatSheetOptedOut(): boolean {
  try {
    return window.localStorage.getItem(OPT_OUT_KEY) === "1";
  } catch {
    return false;
  }
}

export function saveCheatSheetOptedOut(optedOut: boolean) {
  try {
    if (optedOut) window.localStorage.setItem(OPT_OUT_KEY, "1");
    else window.localStorage.removeItem(OPT_OUT_KEY);
  } catch {
    /* private mode: the opt-out lasts only for this launch */
  }
}

type Panel = { label: string; key: string };

/**
 * CheatSheet is the how-to-use sheet shown on launch and reopened with /help.
 * Closing hides it for this launch only; the opt-out is a separate choice that
 * the parent persists. Focus moves into the sheet and returns to the previous
 * element when it closes.
 */
export function CheatSheet({
  panels,
  optedOut,
  onSetOptedOut,
  onClose,
}: {
  panels: Panel[];
  optedOut: boolean;
  onSetOptedOut: (optedOut: boolean) => void;
  onClose: () => void;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const previous = document.activeElement;
    dialogRef.current?.focus();
    return () => {
      if (previous instanceof HTMLElement) previous.focus();
    };
  }, []);

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

  return (
    <div className="overlay" onClick={onClose}>
      <div
        ref={dialogRef}
        className="sheet cheat-sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="cheat-sheet-title"
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="sheet-head">
          <strong id="cheat-sheet-title">How to use AgentTunnel</strong>
          <span className="hint">Esc closes · reopen with /help</span>
        </div>
        <div className="sheet-body cheat-body">
          <section className="set-section">
            <h4>Threads</h4>
            <ul className="cheat-list">
              <li>
                Type a task in the composer and press <kbd>Enter</kbd>. <kbd>Shift</kbd>+
                <kbd>Enter</kbd> adds a newline.
              </li>
              <li>
                Enter while a turn runs steers the team instead of starting a new one.
              </li>
              <li>
                Each top-level thread in a git project gets its own worktree on its first turn.
                Subthreads share their parent's.
              </li>
              <li>
                <kbd>!</kbd> followed by a command runs it in the thread's project.
              </li>
              <li>
                Hold <kbd>{HOLD_MOD_LABEL}</kbd>+
                <kbd>T</kbd> to dictate; release to insert the text.
              </li>
            </ul>
          </section>

          <section className="set-section">
            <h4>Slash commands</h4>
            <p className="hint">
              Type <kbd>/</kbd> in the composer. <code>/chat</code>, <code>/review</code>,{" "}
              <code>/graph</code>, <code>/board</code> switch the center view.
            </p>
            <ul className="cheat-list">
              <li>
                <code>/btw &lt;question&gt;</code> asks a side question without interrupting the
                running turn.
              </li>
              <li>
                <code>/auto</code> toggles auto mode; <code>/peers</code> toggles agent-to-agent
                messages.
              </li>
              <li>
                <code>/stop</code> stops the running turn; <code>/new</code> starts a thread.
              </li>
              <li>
                <code>/defaults</code> saves this view for new threads; <code>/settings</code>{" "}
                opens settings.
              </li>
            </ul>
          </section>

          <section className="set-section">
            <h4>Side panel</h4>
            <p className="hint">
              Add panels with <kbd>+</kbd>, or press a panel's letter while a thread is open
              (outside text fields). Agents and Token usage stay pinned; close the others with ×.
            </p>
            <div className="cheat-keys">
              {panels.map((p) => (
                <span key={p.label}>
                  <kbd>{p.key}</kbd> {p.label}
                </span>
              ))}
            </div>
          </section>
        </div>
        <div className="sheet-actions cheat-actions">
          <button
            type="button"
            onClick={() => {
              onSetOptedOut(!optedOut);
              if (!optedOut) onClose();
            }}
          >
            {optedOut ? "Show on every launch" : "Do not show this again"}
          </button>
          <button type="button" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
