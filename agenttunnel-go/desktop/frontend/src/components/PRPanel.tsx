import { useState } from "react";

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

/** Runs one daemon write (pr.commit, pr.push, ...). Resolves true when it succeeded. */
export type PRAction = (method: string, params?: Record<string, unknown>) => Promise<boolean>;

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

/** Commit form: a message plus an opt-in "stage everything first" switch. */
function CommitForm({ busy, onAction }: { busy: boolean; onAction: PRAction }) {
  const [message, setMessage] = useState("");
  const [all, setAll] = useState(false);
  const trimmed = message.trim();

  const commit = async () => {
    const ok = await onAction("pr.commit", { message: trimmed, all });
    if (ok) setMessage("");
  };

  return (
    <div className="pr-git-form">
      <textarea
        rows={2}
        placeholder="Commit message"
        value={message}
        onChange={(e) => setMessage(e.target.value)}
        disabled={busy}
      />
      <div className="pr-row">
        <label className="hint pr-check-toggle">
          <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} disabled={busy} />
          Stage all changes
        </label>
        <button className="primary" onClick={() => void commit()} disabled={busy || trimmed === ""}>
          Commit
        </button>
      </div>
    </div>
  );
}

/** Create-request form: an optional title; blank lets gh build it from the commits. */
function CreateRequest({ busy, onAction }: { busy: boolean; onAction: PRAction }) {
  const [title, setTitle] = useState("");
  return (
    <div className="pr-git-form">
      <input
        placeholder="Title (blank uses the commits)"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        disabled={busy}
      />
      <div className="pr-row">
        <span className="hint">Push the branch first if it is new.</span>
        <button className="primary" onClick={() => void onAction("pr.create", { title })} disabled={busy}>
          Create PR
        </button>
      </div>
    </div>
  );
}

/** Merge is one destructive click, so it takes a second click to confirm. */
function MergeButton({ busy, onAction }: { busy: boolean; onAction: PRAction }) {
  const [armed, setArmed] = useState(false);
  if (!armed) {
    return (
      <button className="danger" onClick={() => setArmed(true)} disabled={busy}>
        Merge…
      </button>
    );
  }
  return (
    <button
      className="danger"
      onClick={() => {
        setArmed(false);
        void onAction("pr.merge");
      }}
      disabled={busy}
    >
      Confirm squash merge
    </button>
  );
}

/**
 * PRPanel shows the pull or merge request for the thread's branch: CI checks,
 * review state, and recent comments. It also offers the common git and forge
 * writes (commit, pull, push, create, ready, merge), each run by the daemon in
 * the thread's worktree. It renders whatever the daemon reports, including why
 * no request can be shown (CLI missing, logged out, no PR yet).
 */
export function PRPanel({
  snapshot,
  loading,
  error,
  busy,
  notice,
  onRefresh,
  onAction,
}: {
  snapshot: PRSnapshot | null;
  loading: boolean;
  error: string | null;
  /** The write in flight ("pr.push" ...), or null when idle. */
  busy: string | null;
  /** Result of the last successful write, e.g. the new PR URL. */
  notice: string | null;
  onRefresh: () => void;
  onAction: PRAction;
}) {
  const header = (
    <div className="panel-bar">
      <span className="chat-title" title={snapshot?.branch ?? ""}>
        {snapshot?.branch ?? "Pull request"}
      </span>
      <button onClick={onRefresh} disabled={loading || busy !== null}>
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
  const isGitHub = snapshot.provider === "github";
  const writing = busy !== null;
  const ready = snapshot.status === "ready" && req != null;
  const openReq = ready && req.state === "open";

  return (
    <div className="pr-panel">
      {header}
      {error && <p className="hint err-text">{error}</p>}
      {notice && (
        <p className="hint">
          {/^https?:\/\//.test(notice) ? (
            <a className="pr-link" href={notice}>
              {notice}
            </a>
          ) : (
            notice
          )}
        </p>
      )}

      {!ready && (
        <p className={`hint${snapshot.status === "error" ? " err-text" : ""}`}>
          {snapshot.message ?? "No pull request for this branch yet."}
        </p>
      )}

      {ready && req && <PRDetails req={req} provider={snapshot.provider} />}

      <div className="pr-git">
        <div className="pr-label">Git</div>
        <CommitForm busy={writing} onAction={onAction} />
        <div className="pr-actions">
          <button onClick={() => void onAction("pr.pull")} disabled={writing}>
            {busy === "pr.pull" ? "Pulling…" : "Pull"}
          </button>
          <button onClick={() => void onAction("pr.push")} disabled={writing}>
            {busy === "pr.push" ? "Pushing…" : "Push"}
          </button>
        </div>
      </div>

      {isGitHub && snapshot.status === "none" && (
        <div className="pr-git">
          <div className="pr-label">Pull request</div>
          <CreateRequest busy={writing} onAction={onAction} />
        </div>
      )}

      {isGitHub && openReq && req && (
        <div className="pr-actions">
          {req.draft && (
            <button onClick={() => void onAction("pr.ready")} disabled={writing}>
              {busy === "pr.ready" ? "Marking…" : "Mark ready"}
            </button>
          )}
          {!req.draft && <MergeButton busy={writing} onAction={onAction} />}
        </div>
      )}

      {ready && req && <PRComments comments={req.comments ?? []} gitlab={snapshot.provider === "gitlab"} />}
    </div>
  );
}

function PRDetails({ req, provider }: { req: PRRequest; provider?: string }) {
  const runs = req.checkRuns ?? [];
  return (
    <>
      <div className="pr-title">
        <span className="pill">{provider === "gitlab" ? "MR" : "PR"} #{req.number}</span>
        <span className={`pill${req.state === "merged" ? " run" : ""}`}>{req.draft ? "draft" : req.state}</span>
      </div>
      <p className="pr-heading">{req.title}</p>
      <a className="pr-url" href={req.url}>
        {req.url}
      </a>

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
    </>
  );
}

function PRComments({ comments, gitlab }: { comments: PRComment[]; gitlab: boolean }) {
  return (
    <>
      <div className="pr-label pr-comments-label">Comments</div>
      {gitlab ? (
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
    </>
  );
}
