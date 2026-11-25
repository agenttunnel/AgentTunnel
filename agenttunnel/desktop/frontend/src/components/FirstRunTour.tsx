import { useEffect, useRef, useState, type ReactNode } from "react";
import { ALT, MOD } from "../lib/navKeys";
import { markCheatSheetSeen } from "./CheatSheet";

const CARDS: { title: string; body: ReactNode }[] = [
  {
    title: "Projects hold threads",
    body: (
      <>
        Open a folder as a project. Each thread is one piece of work with its own team of agents,
        and in a git project its own worktree, so threads never step on each other.
      </>
    ),
  },
  {
    title: "Talk to the team",
    body: (
      <>
        Describe the task and press <kbd>Enter</kbd>. Pressing it again while a turn runs steers
        the team. <kbd>@</kbd> attaches files, <kbd>/</kbd> runs commands, <kbd>!</kbd> runs a
        shell command in the project.
      </>
    ),
  },
  {
    title: "Review, then ship",
    body: (
      <>
        When a turn ends, its changes are one click away: the Review and Files tabs, and the pull
        request. Comment on diff lines to send the team a review.
      </>
    ),
  },
  {
    title: "Jump anywhere",
    body: (
      <>
        <kbd>{MOD}K</kbd> finds any thread, project, file or command. <kbd>{ALT}Tab</kbd> shows every
        thread across projects. Type <code>/help</code> any time for the full reference.
      </>
    ),
  },
];

/**
 * The first-run tour: four short cards, shown once (the same seen mark the
 * how-to sheet used, synced through the daemon's prefs). Arrow keys page,
 * Esc skips.
 */
export function FirstRunTour({ onClose }: { onClose: () => void }) {
  const [i, setI] = useState(0);
  const dialogRef = useRef<HTMLDivElement>(null);
  const last = i === CARDS.length - 1;

  useEffect(() => {
    markCheatSheetSeen();
    dialogRef.current?.focus();
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing) return;
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      } else if (e.key === "ArrowRight") {
        setI((n) => Math.min(n + 1, CARDS.length - 1));
      } else if (e.key === "ArrowLeft") {
        setI((n) => Math.max(n - 1, 0));
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const card = CARDS[i];
  return (
    <div className="overlay" onClick={onClose}>
      <div
        ref={dialogRef}
        className="sheet tour"
        role="dialog"
        aria-modal="true"
        aria-labelledby="tour-title"
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="tour-count">
          {i + 1} of {CARDS.length}
        </div>
        <h2 id="tour-title" className="tour-title">
          {card.title}
        </h2>
        <p className="tour-body">{card.body}</p>
        <div className="tour-foot">
          <div className="tour-dots" aria-hidden="true">
            {CARDS.map((c, n) => (
              <span key={c.title} className={n === i ? "on" : ""} />
            ))}
          </div>
          <button type="button" className="tour-skip" onClick={onClose}>
            {last ? "Close" : "Skip"}
          </button>
          {i > 0 && (
            <button type="button" onClick={() => setI(i - 1)}>
              Back
            </button>
          )}
          <button type="button" className="primary" onClick={() => (last ? onClose() : setI(i + 1))}>
            {last ? "Get started" : "Next"}
          </button>
        </div>
      </div>
    </div>
  );
}
