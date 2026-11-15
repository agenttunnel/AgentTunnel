import { openExternal } from "../lib/rpc";

/** One pull request the thread opened, as the daemon reads it from the forge. */
export type PRRequest = {
  number: number;
  title: string;
  url: string;
  state: string;
  draft: boolean;
  comments: number;
  commits: number;
  assignees: string[] | null;
  reviewers: string[] | null;
  /** CI bucket and approval bucket, set when the forge reports them (GitLab). */
  checks?: string;
  review?: string;
  /** Set when the forge could not be read for this request; the link still works. */
  error?: string;
};

/** The pr.list reply: every request the thread tracks, plus why the forge may not be readable. */
export type PRListing = {
  /** The thread the listing was read for, so a late reply never shows on another thread. */
  threadId?: string;
  status: "ready" | "none" | "unavailable" | "unauthenticated" | "unsupported" | "error";
  message?: string;
  provider?: "github" | "gitlab";
  branch?: string;
  requests: PRRequest[] | null;
};

function names(list: string[] | null): string {
  return list && list.length > 0 ? list.join(", ") : "none";
}

function Fact({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="pr-row">
      <span className="pr-label">{label}</span>
      <span className="hint">{value}</span>
    </div>
  );
}

/**
 * PRPanel lists every pull request the thread opened. It is read-only: number,
 * title, link, comments, commits, assignees and reviewers. "Open PR" hands the
 * link to the default browser. Nothing here changes git or the forge.
 */
export function PRPanel({
  listing,
  loading,
  error,
  onRefresh,
}: {
  listing: PRListing | null;
  loading: boolean;
  error: string | null;
  onRefresh: () => void;
}) {
  const mr = listing?.provider === "gitlab";
  const noun = mr ? "MR" : "PR";
  const plural = mr ? "merge requests" : "pull requests";
  const header = (
    <div className="panel-bar">
      <span className="chat-title" title={listing?.branch ?? ""}>
        {listing?.branch ?? (mr ? "Merge requests" : "Pull requests")}
      </span>
      <button onClick={onRefresh} disabled={loading}>
        {loading ? "Checking…" : "Refresh"}
      </button>
    </div>
  );

  if (!listing) {
    return (
      <div className="pr-panel">
        {header}
        {error ? <p className="hint err-text">{error}</p> : <p className="hint">Checking pull requests…</p>}
      </div>
    );
  }

  const requests = listing.requests ?? [];
  return (
    <div className="pr-panel">
      {header}
      {error && <p className="hint err-text">{error}</p>}
      {listing.status !== "ready" && listing.message && (
        <p className={`hint${listing.status === "error" ? " err-text" : ""}`}>{listing.message}</p>
      )}
      {requests.length === 0 && listing.status === "ready" && (
        <p className="hint">No {plural} opened from this thread yet.</p>
      )}
      {requests.map((req) => (
        <PRCard key={req.url} req={req} noun={noun} />
      ))}
    </div>
  );
}

function PRCard({ req, noun }: { req: PRRequest; noun: string }) {
  return (
    <div className="pr-git">
      <div className="pr-title">
        <span className="pill">{noun} #{req.number}</span>
        <span className={`pill${req.state === "merged" ? " run" : ""}`}>{req.draft ? "draft" : req.state}</span>
      </div>
      {req.title && <p className="pr-heading">{req.title}</p>}
      <a className="pr-url" href={req.url}>
        {req.url}
      </a>
      {req.error ? (
        <p className="hint err-text">{req.error}</p>
      ) : (
        <>
          <Fact label="Comments" value={req.comments} />
          {noun !== "MR" && <Fact label="Commits" value={req.commits} />}
          {req.checks && <Fact label={noun === "MR" ? "Pipeline" : "Checks"} value={req.checks} />}
          {req.review && <Fact label="Review" value={req.review} />}
          <Fact label="Assignees" value={names(req.assignees)} />
          <Fact label="Reviewers" value={names(req.reviewers)} />
        </>
      )}
      <div className="pr-actions">
        <button className="primary" onClick={() => openExternal(req.url)}>
          Open {noun}
        </button>
      </div>
    </div>
  );
}
