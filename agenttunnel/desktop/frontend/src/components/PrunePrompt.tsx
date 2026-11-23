import type { WorktreeRow } from "../lib/worktrees";
import { useEscapeClose } from "../lib/escape";
import { useDialogFocus } from "../lib/dialogFocus";

/**
 * PrunePrompt is the once-a-day question: the background scan found checkouts
 * whose pull requests merged, so their directories can go. It lists every
 * candidate with its project and PR, and only offers the clean ones: a merged
 * worktree with uncommitted changes is mentioned, never swept up here.
 */
export function PrunePrompt({
  candidates,
  dirtyCount,
  busy,
  error,
  onPrune,
  onReview,
  onDismiss,
}: {
  candidates: WorktreeRow[];
  /** Merged worktrees held back because they have uncommitted changes. */
  dirtyCount: number;
  busy: boolean;
  error: string | null;
  onPrune: () => void;
  onReview: () => void;
  onDismiss: () => void;
}) {
  useEscapeClose(onDismiss);
  const box = useDialogFocus<HTMLElement>();
  return (
    <div className="overlay" onClick={onDismiss}>
      <section
        ref={box}
        className="sheet prune-sheet"
        role="dialog"
        aria-modal="true"
        aria-label="Prune merged worktrees"
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="sheet-head">
          <strong id="prune-title">
            {candidates.length} worktree{candidates.length === 1 ? "" : "s"} ready to prune
          </strong>
          <button type="button" onClick={onDismiss} aria-label="Close">
            ×
          </button>
        </header>
        <div className="sheet-body">
          <p className="hint">
            Their pull requests merged. Pruning removes the checkout only: the branch and the
            pull request stay on the forge.
          </p>
          <ul className="prune-list">
            {candidates.map(({ project, worktree, pr }) => (
              <li className="prune-row" key={worktree.path}>
                <div className="prune-row-main">
                  <span className="prune-branch" title={worktree.path}>
                    {worktree.branch || "(detached)"}
                  </span>
                  <span className="prune-project" title={project.path || project.name}>
                    {project.name}
                  </span>
                </div>
                {pr && (
                  <a className="prune-pr" href={pr.url} title={`Open PR #${pr.number}`}>
                    #{pr.number}
                  </a>
                )}
              </li>
            ))}
          </ul>
          {dirtyCount > 0 && (
            <p className="hint">
              {dirtyCount} more merged worktree{dirtyCount === 1 ? " has" : "s have"} uncommitted
              changes. They are marked on the Worktrees page so you can review them first.
            </p>
          )}
          {error && <p className="hint err-text">{error}</p>}
          <div className="sheet-actions">
            <button className="primary" disabled={busy} onClick={onPrune}>
              {busy ? "Pruning…" : `Prune ${candidates.length}`}
            </button>
            <button disabled={busy} onClick={onReview}>
              Review worktrees
            </button>
            <button disabled={busy} onClick={onDismiss}>
              Not now
            </button>
          </div>
        </div>
      </section>
    </div>
  );
}
