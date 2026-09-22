import type {
  OrchestrationShellSnapshot,
  OrchestrationThreadDetailSnapshot,
} from "@t3tools/contracts";
import {
  deriveAgentPanelModel,
  foldSubagentActivities,
} from "@t3tools/client-runtime/state/subagentRuntime";

/**
 * Turning the server's snapshots into the lines the TUI shows. Pure on purpose:
 * the renderer, the key handling, and this formatting can all be exercised
 * without a terminal or a server, which is what makes the TUI testable at all.
 */

export interface TuiThreadRow {
  readonly id: string;
  readonly title: string;
  readonly projectTitle: string;
  readonly branch: string | null;
  /** `7/7 settled`-style fleet summary, or null when the thread has no agents. */
  readonly fleet: string | null;
}

export interface TuiThreadView {
  readonly title: string;
  readonly recap: string | null;
  readonly lines: ReadonlyArray<TuiLine>;
}

export interface TuiLine {
  readonly kind: "user" | "assistant" | "agent" | "system";
  readonly text: string;
}

const TITLE_MAX_CHARS = 80;
const LINE_MAX_CHARS = 400;

function oneLine(text: string, max: number): string {
  const normalized = text.replace(/\s+/g, " ").trim();
  return normalized.length <= max ? normalized : `${normalized.slice(0, max - 1).trimEnd()}…`;
}

/** Threads newest first, with the fleet summary the Agents panel would show. */
export function threadRows(snapshot: OrchestrationShellSnapshot): ReadonlyArray<TuiThreadRow> {
  const projectTitles = new Map(snapshot.projects.map((project) => [project.id, project.title]));
  return snapshot.threads
    .slice()
    .sort((left, right) => right.updatedAt.localeCompare(left.updatedAt))
    .map((thread) => ({
      id: thread.id,
      title: oneLine(thread.title, TITLE_MAX_CHARS),
      projectTitle: projectTitles.get(thread.projectId) ?? "unknown project",
      branch: thread.branch,
      fleet: null,
    }));
}

/**
 * The transcript of one thread: the user's messages, the assistant's answers,
 * what each sub-agent did, and the system rows that explain the run. Agents and
 * their recap come from the same fold and derivation the web clients use, so a
 * TUI session and a browser session describe the same work the same way.
 */
export function threadView(
  snapshot: OrchestrationThreadDetailSnapshot,
  options?: { readonly includeActivities?: boolean },
): TuiThreadView {
  const includeActivities = options?.includeActivities ?? true;
  // The detail snapshot wraps the thread: messages, activities, and the session
  // live under `thread`, not at the top level.
  const thread = snapshot.thread;
  const agents = foldSubagentActivities(thread.activities, {
    sessionLive: thread.session !== null && thread.session.status === "running",
  });
  const model = deriveAgentPanelModel({ agents });
  const newestUser = [...thread.messages].reverse().find((message) => message.role === "user");
  // The web client's richer recap (`deriveThreadRecap`, PR #40) is not on this
  // branch: it lands with the sticky composer row. Until then the TUI says the
  // same thing with the data this branch has — goal, live fleet, settled work.
  const recapParts: string[] = [];
  const goal = newestUser === undefined ? "" : oneLine(newestUser.text, 140);
  if (goal.length > 0) recapParts.push(`Goal: ${goal}`);
  if (model.liveCount > 0)
    recapParts.push(`${model.liveCount} agent${model.liveCount === 1 ? "" : "s"} working`);
  else if (model.settledCount > 0) recapParts.push(`${model.settledCount} settled`);
  const recap = recapParts.length === 0 ? null : recapParts.join("  ·  ");

  const lines: TuiLine[] = [];
  for (const message of thread.messages) {
    const text = oneLine(message.text, LINE_MAX_CHARS);
    if (text.length === 0) continue;
    lines.push({ kind: message.role === "user" ? "user" : "assistant", text });
  }
  if (includeActivities) {
    for (const agent of agents) {
      const detail = agent.error ?? agent.result ?? agent.progress ?? null;
      if (detail === null) continue;
      lines.push({
        kind: "agent",
        text: oneLine(`${agent.role ?? agent.title} · ${agent.status} — ${detail}`, LINE_MAX_CHARS),
      });
    }
  }

  return {
    title: oneLine(thread.title, TITLE_MAX_CHARS),
    recap,
    lines,
  };
}
