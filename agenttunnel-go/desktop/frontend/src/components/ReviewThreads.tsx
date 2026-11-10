// Local structural types matching the thread.get wire shape (same approach as
// DiffView: no import of the app component).
export type ReviewReplyLike = { authorId: string; body: string; at: string };
export type ReviewThreadLike = {
  id: string;
  file: string;
  line: number;
  endLine: number;
  side?: "old" | "new";
  body: string;
  authorId: string;
  status: string;
  replies?: ReviewReplyLike[] | null;
  openedAt: string;
  updatedAt: string;
};
export type ReviewPeople = {
  agents: Record<string, { name?: string; role: string }> | null;
  /** Role id to display name. */
  roleNames: Record<string, string>;
};

/** Anchor key shared by the thread map and DiffView lines: file|side|line. */
export function reviewAnchorKey(file: string, side: "old" | "new", line: number): string {
  return `${file}|${side}|${line}`;
}

function timeOf(at: string): string {
  if (!at) return "";
  const d = new Date(at);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

/** Name plus a role badge, looked up from the thread's agents. */
function Author({ id, people }: { id: string; people: ReviewPeople }) {
  const a = people.agents?.[id];
  if (!a) return <span className="rt-author">Unknown</span>;
  const role = people.roleNames[a.role] || a.role;
  return (
    <span className="rt-author">
      {a.name || role}
      {a.name && role && <span className="rt-role">{role}</span>}
    </span>
  );
}

function ThreadCard({ thread, people }: { thread: ReviewThreadLike; people: ReviewPeople }) {
  const resolved = thread.status === "resolved";
  const range =
    thread.endLine > thread.line ? `${thread.line}-${thread.endLine}` : `${thread.line}`;
  return (
    <div className={`review-thread ${resolved ? "resolved" : "open"}`}>
      <div className="rt-head">
        <Author id={thread.authorId} people={people} />
        <span className={`rt-status ${resolved ? "resolved" : "open"}`}>
          {resolved ? "resolved" : "open"}
        </span>
        <span className="rt-anchor">
          {thread.file}:{range}
        </span>
      </div>
      <div className="rt-body">{thread.body}</div>
      {(thread.replies ?? []).map((r, i) => (
        <div className="rt-reply" key={i}>
          <div className="rt-head">
            <Author id={r.authorId} people={people} />
            <span className="rt-time">{timeOf(r.at)}</span>
          </div>
          <div className="rt-body">{r.body}</div>
        </div>
      ))}
    </div>
  );
}

/**
 * Inline review threads under one diff line. Open threads show in full;
 * resolved ones collapse to a one-line summary until opened.
 */
export function ReviewThreads({
  threads,
  people,
}: {
  threads: ReviewThreadLike[];
  people: ReviewPeople;
}) {
  return (
    <div className="review-threads">
      {threads.map((t) =>
        t.status === "resolved" ? (
          <details className="rt-collapsed" key={t.id}>
            <summary>
              <Author id={t.authorId} people={people} />
              <span className="rt-status resolved">resolved</span>
              <span className="rt-anchor">
                {(t.replies ?? []).length} {(t.replies ?? []).length === 1 ? "reply" : "replies"}
              </span>
            </summary>
            <ThreadCard thread={t} people={people} />
          </details>
        ) : (
          <ThreadCard key={t.id} thread={t} people={people} />
        ),
      )}
    </div>
  );
}
