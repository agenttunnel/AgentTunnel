// The threads screen's body: the project's threads as cards or as a board,
// filtered by the shared thread search. Reading the search here, not in App,
// keeps a keystroke in the search box from re-rendering the app.

import { memo, type MouseEvent } from "react";
import type { ThreadSummary } from "../App";
import { soloHue } from "../lib/soloIdentity";
import { clickable } from "../lib/switchProps";
import { useProjectThreads } from "./Sidebar/ThreadList";
import { ThreadBoard, type BoardIssue, type BoardIssues, type ThreadState as BoardThreadState } from "./ThreadBoard";
import { ThreadClock, fmtUpdated } from "./ThreadClock";
import { needsAttention, STATUS_LABEL, threadStatus } from "../lib/threadStatus";

export const ProjectThreads = memo(function ProjectThreads({
  threads,
  projectId,
  view,
  states,
  issues,
  issuesLoading,
  onNew,
  onSelect,
  onMenu,
  onOpen,
  onStartDraft,
  onDropIssue,
  onRefreshIssues,
  onArchive,
  onDelete,
}: {
  threads: ThreadSummary[];
  projectId: string | null;
  view: "list" | "board";
  states: Record<string, BoardThreadState>;
  issues: BoardIssues | null;
  issuesLoading: boolean;
  onNew: () => void;
  onSelect: (id: string) => void;
  onMenu: (t: ThreadSummary, e: MouseEvent) => void;
  onOpen: (id: string, draft: boolean) => void;
  onStartDraft: (id: string) => void;
  onDropIssue: (issue: BoardIssue) => void;
  onRefreshIssues: () => void;
  onArchive: (ids: string[], archived: boolean) => void;
  onDelete: (ids: string[]) => void;
}) {
  const { projectThreads, query } = useProjectThreads(threads, projectId);
  return (
    <>
      {view === "list" && projectThreads.length === 0 && query !== "" && (
        <div className="empty">No threads match that filter.</div>
      )}
      {view === "board" ? (
        <ThreadBoard
          threads={projectThreads}
          states={states}
          formatUpdated={fmtUpdated}
          issues={issues}
          issuesLoading={issuesLoading}
          onOpen={onOpen}
          onStartDraft={onStartDraft}
          onDropIssue={onDropIssue}
          onRefreshIssues={onRefreshIssues}
          onArchive={onArchive}
          onDelete={onDelete}
        />
      ) : (
        <div className="thread-cards">
          <div className="new-card" aria-label="New thread" title="New thread" {...clickable(onNew)}>
            <span className="new-card-plus" aria-hidden="true">
              +
            </span>
          </div>
          {projectThreads.map((t) => {
            const tint = t.soloRuntime ? `solo-identity ${soloHue(t.id)}` : "";
            const status = threadStatus(t);
            const label = STATUS_LABEL[status];
            return (
              <div
                className={`thread-card${t.stale ? " is-stale" : ""}${needsAttention(status) ? " attn" : ""}${tint ? ` ${tint}` : ""}`}
                key={t.id}
                style={t.depth ? { marginLeft: t.depth * 16 } : undefined}
                {...clickable(() => onSelect(t.id))}
                onContextMenu={(e) => onMenu(t, e)}
              >
                <div className="tc-head">
                  <div className="tc-title" title={t.title}>
                    {t.title}
                  </div>
                  {t.owner === "orchestrator" && (
                    <span className="tc-owner" title="Started by the orchestrator">
                      Orchestrator
                    </span>
                  )}
                </div>
                <div className="tc-meta">
                  {t.messages > 0
                    ? `${t.messages} message${t.messages === 1 ? "" : "s"} · ${t.agents} agent${t.agents === 1 ? "" : "s"}${t.autoMode ? " · auto" : ""}`
                    : "No messages yet"}
                </div>
                <div className="tc-foot">
                  {status !== "running" && label && <span className={`status-chip ${status}`}>{label}</span>}
                  <ThreadClock t={t} />
                  {fmtUpdated(t.updatedAt) && (
                    <span className="tc-updated">Updated {fmtUpdated(t.updatedAt)}</span>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      )}
    </>
  );
});
