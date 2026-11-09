export type PRCheck = { name: string; state: string };
export type PRComment = {
  author: string;
  body: string;
  url?: string;
  kind: "comment" | "review";
  reviewState?: string;
  createdAt: string;
};
export type PRRequest = {
  number: number;
  title: string;
  url: string;
  state: string;
  draft: boolean;
  review: string;
  checks: string;
  checkRuns: PRCheck[] | null;
  comments: PRComment[] | null;
};
export type PRSnapshot = {
  /** The thread the snapshot was read for, so a late reply never shows on another thread. */
  threadId?: string;
  status: "ready" | "none" | "unavailable" | "unauthenticated" | "unsupported" | "error";
  message?: string;
  provider?: "github" | "gitlab";
  branch?: string;
  request?: PRRequest | null;
};

const reviewLabel: Record<string, string> = {
  approved: "Approved",
  changes_requested: "Changes requested",
  review_required: "Review required",
  none: "No review",
};

/** Dots reuse the thread status colors: passing = ok, failing = err, pending = running; anything else stays faint. */
const checkDot: Record<string, string> = { passing: "succeeded", failing: "failed", pending: "running" };

function Check({ state }: { state: string }) {
  return <span className={`pill${state === "passing" ? " run" : ""}`}>{state}</span>;
}

/**
 * PRPanel shows the pull or merge request for the thread's branch: CI checks,
 * review state, and recent comments. It renders whatever the daemon reports,
 * including why no request can be shown (CLI missing, logged out, no PR yet).
 */
export function PRPanel({
  snapshot,
  loading,
  error,
  onRefresh,
}: {
  snapshot: PRSnapshot | null;
  loading: boolean;
  error: string | null;
  onRefresh: () => void;
}) {
  const header = (
    <div className="panel-bar">
      <span className="chat-title" title={snapshot?.branch ?? ""}>
        {snapshot?.branch ?? "Pull request"}
      </span>
      <button onClick={onRefresh} disabled={loading}>
        {loading ? "Checking…" : "Refresh"}
      </button>
    </div>
  );

  if (!snapshot) {
    return (
      <div className="pr-panel">
        {header}
        {error ? <p className="hint err-text">{error}</p> : <p className="hint">Checking pull request…</p>}
      </div>
    );
  }

  const req = snapshot.request;
  if (snapshot.status !== "ready" || !req) {
    return (
      <div className="pr-panel">
        {header}
        <p className={`hint${snapshot.status === "error" ? " err-text" : ""}`}>
          {snapshot.message ?? "No pull request for this branch yet."}
        </p>
        {error && <p className="hint err-text">{error}</p>}
      </div>
    );
  }

  const runs = req.checkRuns ?? [];
  const comments = req.comments ?? [];
  return (
    <div className="pr-panel">
      {header}
      {error && <p className="hint err-text">{error}</p>}
      <div className="pr-title">
        <span className="pill">{snapshot.provider === "gitlab" ? "MR" : "PR"} #{req.number}</span>
        <span className={`pill${req.state === "merged" ? " run" : ""}`}>{req.draft ? "draft" : req.state}</span>
      </div>
      <p className="pr-heading">{req.title}</p>
      <p className="hint pr-url">{req.url}</p>

      <div className="pr-row">
        <span className="pr-label">Checks</span>
        <Check state={req.checks} />
      </div>
      {runs.length > 0 && (
        <ul className="pr-checks">
          {runs.map((c, i) => (
            <li key={`${c.name}-${i}`}>
              <span className={`dot ${checkDot[c.state] ?? ""}`} aria-hidden="true" />
              <span className="pr-check-name">{c.name}</span>
              <span className="hint">{c.state}</span>
            </li>
          ))}
        </ul>
      )}
      {req.review && (
        <div className="pr-row">
          <span className="pr-label">Review</span>
          <span className="hint">{reviewLabel[req.review] ?? req.review}</span>
        </div>
      )}

      <div className="pr-label pr-comments-label">Comments</div>
      {snapshot.provider === "gitlab" ? (
        <p className="hint">Comments are not available for GitLab yet.</p>
      ) : comments.length === 0 ? (
        <p className="hint">No comments yet.</p>
      ) : (
        <ul className="pr-comments">
          {comments.map((c, i) => (
            <li key={`${c.createdAt}-${i}`}>
              <div className="hint">
                <b>{c.author || "unknown"}</b>
                {c.kind === "review" && c.reviewState ? ` · ${c.reviewState.replace(/_/g, " ")}` : ""}
              </div>
              <div className="pr-body">{c.body}</div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
