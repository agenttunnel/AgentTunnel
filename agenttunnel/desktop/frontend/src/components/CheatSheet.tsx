import { useEffect, useRef } from "react";
import { HOLD_MOD_LABEL } from "../lib/dictation";
import { getPref, setPref } from "../lib/prefs";

const SEEN_KEY = "at.cheatSheetSeen";
// Before the sheet became first-run only, users could opt out of it showing on
// every launch. That choice still means "already seen".
const LEGACY_OPT_OUT_KEY = "at.cheatSheetOptedOut";

/** Whether the sheet should open on its own at launch: only until it has been
 * seen once. The seen mark is a daemon-synced pref, so every client of one
 * daemon shares it. Private mode reads as unseen and shows it each session. */
export function showCheatSheetOnLaunch(): boolean {
  return getPref(SEEN_KEY) !== "1" && getPref(LEGACY_OPT_OUT_KEY) !== "1";
}

function markCheatSheetSeen() {
  if (getPref(SEEN_KEY) !== "1") setPref(SEEN_KEY, "1");
}

type Panel = { label: string; key: string };

/**
 * CheatSheet is the how-to-use sheet: shown on first run and reopened with
 * /help. Showing it marks it seen, so it does not open on its own again. Focus
 * moves into the sheet and returns to the previous element when it closes.
 */
export function CheatSheet({ panels, onClose }: { panels: Panel[]; onClose: () => void }) {
  const dialogRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    markCheatSheetSeen();
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
                Subthreads share their parent's. The Worktrees tab shows every checkout and marks
                the ones whose pull requests merged as ready to prune.
              </li>
              <li>
                <kbd>!</kbd> followed by a command runs it in the thread's project.
              </li>
              <li>
                Hold <kbd>{HOLD_MOD_LABEL}</kbd>+
                <kbd>T</kbd> to dictate; release to insert the text.
              </li>
              <li>
                <kbd>{HOLD_MOD_LABEL}</kbd>+<kbd>S</kbd> or <code>/compact</code> hides the side
                panes and the top bar for a clean composer window; press either again to bring
                everything back.
              </li>
              <li>
                <kbd>Option</kbd>/<kbd>Alt</kbd>+<kbd>Tab</kbd> opens All threads;
                press <kbd>Tab</kbd> again to step through the list.
              </li>
            </ul>
          </section>

          <section className="set-section">
            <h4>Slash commands</h4>
            <p className="hint">
              Type <kbd>/</kbd> in the composer. <code>/chat</code>, <code>/review</code>,{" "}
              <code>/files</code>, <code>/graph</code>, <code>/board</code> switch the center view.
            </p>
            <ul className="cheat-list">
              <li>
                <code>/btw &lt;question&gt;</code> asks a side question without interrupting the
                running turn; follow-ups continue the same side conversation until the panel is
                dismissed or the thread moves to the next turn.
              </li>
              <li>
                <code>/auto</code> toggles auto mode; <code>/peers</code> toggles agent-to-agent
                messages.
              </li>
              <li>
                <code>/stop</code> stops the running turn; <code>/new</code> starts a thread.
              </li>
              <li>
                <code>/compact</code> toggles the clean composer view.
              </li>
              <li>
                <code>/vim</code> turns on Vimium-style keys outside text fields: <kbd>f</kbd> shows
                link hints (type a label to click), <kbd>j</kbd>/<kbd>k</kbd> scroll,{" "}
                <kbd>Ctrl</kbd>+<kbd>D</kbd>/<kbd>U</kbd> scroll half a page, <kbd>g</kbd>/
                <kbd>G</kbd> jump to the top or bottom, and <kbd>Esc</kbd> leaves the composer. While
                on, <kbd>F</kbd> no longer opens Files; use <code>/files</code>.
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
          <button type="button" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
