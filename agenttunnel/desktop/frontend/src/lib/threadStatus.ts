// One honest status per thread, derived from the list row the daemon already
// sends. The sidebar, the breadcrumb and the palette all read it, so a thread
// never says "working" in one place and "idle" in another.

export type ThreadStatus = "running" | "asking" | "scheduled" | "unseen" | "draft" | "new" | "idle";

type StatusFields = {
  runningAgents?: number;
  pendingQuestions?: number;
  waiting?: unknown;
  unseen?: boolean;
  draft?: boolean;
  messages: number;
};

/** Most urgent first: a question waiting on the human outranks a running turn. */
export function threadStatus(t: StatusFields): ThreadStatus {
  if ((t.pendingQuestions ?? 0) > 0) return "asking";
  if ((t.runningAgents ?? 0) > 0) return "running";
  if (t.waiting) return "scheduled";
  if (t.unseen) return "unseen";
  if (t.draft) return "draft";
  if (t.messages === 0) return "new";
  return "idle";
}

/** The short label a status pill shows; idle and new threads show none. */
export const STATUS_LABEL: Record<ThreadStatus, string> = {
  asking: "Needs you",
  running: "Working",
  scheduled: "Scheduled",
  unseen: "Done",
  draft: "Draft",
  new: "",
  idle: "",
};

/** Statuses that ask for the user's eyes: the row is marked and counted. */
export function needsAttention(s: ThreadStatus): boolean {
  return s === "asking" || s === "unseen";
}
