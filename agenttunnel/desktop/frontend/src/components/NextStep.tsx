import { memo } from "react";
import { centerTabHint } from "../lib/navKeys";

/**
 * The quiet row under a finished turn: where the work goes next. It names
 * actions, not results, so it never claims changes that are not there.
 */
export const NextStep = memo(function NextStep({
  hasPR,
  onReview,
  onFiles,
  onPR,
}: {
  /** The thread's branch already has a pull request. */
  hasPR: boolean;
  onReview: () => void;
  onFiles: () => void;
  /** Shows the PR panel when there is one; otherwise drafts the ask in the composer. */
  onPR: () => void;
}) {
  return (
    <div className="next-step" role="group" aria-label="Next step">
      <span className="next-step-label">Next</span>
      <button type="button" onClick={onReview} title={`Review the changes (${centerTabHint("review")})`}>
        Review changes
      </button>
      <button type="button" onClick={onFiles} title={`Browse the files (${centerTabHint("files")})`}>
        Files
      </button>
      <button
        type="button"
        onClick={onPR}
        title={hasPR ? "Show the pull request panel (P)" : "Draft a request to open a pull request; you send it"}
      >
        {hasPR ? "View pull request" : "Ask for a pull request"}
      </button>
    </div>
  );
});
