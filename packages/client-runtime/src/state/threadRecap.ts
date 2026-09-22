/**
 * The one-line recap the client shows above the composer: where the thread's
 * goal stands right now, what the fleet is doing, and what the last report said
 * to do next. Derived from data every client already streams — the agent fold,
 * the newest user message, and the last assistant report — so the recap is live
 * while sub-agents work and needs no server round-trip.
 */
import type { AgentPanelModel, RuntimeSubagent } from "./subagentRuntime";

export interface ThreadRecap {
  readonly goal: string | null;
  readonly working: number;
  readonly settled: number;
  readonly latestActivity: string | null;
  readonly nextSteps: ReadonlyArray<string>;
}

const GOAL_MAX_CHARS = 140;
const ACTIVITY_MAX_CHARS = 110;
const STEP_MAX_CHARS = 120;
const MAX_NEXT_STEPS = 3;

function oneLine(text: string, max: number): string {
  const normalized = text.replace(/\s+/g, " ").trim();
  return normalized.length <= max ? normalized : `${normalized.slice(0, max - 1).trimEnd()}…`;
}

const isLive = (agent: RuntimeSubagent): boolean =>
  agent.status === "running" || agent.status === "waiting" || agent.status === "pending";

/**
 * What the busiest agent is doing right now: the newest progress line or tool
 * name among live agents, labelled with its role so the reader knows who is
 * speaking. Null when nothing is running.
 */
export function latestLiveActivity(agents: ReadonlyArray<RuntimeSubagent>): string | null {
  const live = agents
    .filter(isLive)
    .slice()
    .sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
  for (const agent of live) {
    const detail = agent.progress ?? (agent.lastToolName ? `▸ ${agent.lastToolName}` : null);
    if (detail === null) continue;
    const who = agent.role ?? agent.title;
    return oneLine(`${who}: ${detail}`, ACTIVITY_MAX_CHARS);
  }
  const [busiest] = live;
  return busiest ? oneLine(`${busiest.role ?? busiest.title}: starting`, ACTIVITY_MAX_CHARS) : null;
}

/**
 * The bullets under the final report's `### Next steps` heading. Tolerant by
 * design: a thread whose last message is not a report simply has no next steps.
 */
export function parseReportNextSteps(text: string): ReadonlyArray<string> {
  const lines = text.split("\n");
  const headingIndex = lines.findIndex((line) => /^#{2,4}\s*Next steps\s*$/iu.test(line.trim()));
  if (headingIndex < 0) return [];

  const steps: string[] = [];
  for (const line of lines.slice(headingIndex + 1)) {
    const trimmed = line.trim();
    if (/^#{1,6}\s/u.test(trimmed)) break;
    if (trimmed.length === 0) continue;
    const bullet = /^[-*]\s+(.*)$/u.exec(trimmed);
    if (!bullet?.[1]) continue;
    steps.push(oneLine(bullet[1], STEP_MAX_CHARS));
    if (steps.length === MAX_NEXT_STEPS) break;
  }
  return steps;
}

/**
 * The recap for one thread, or null when there is nothing worth saying — no
 * goal, no agents, and no report. Callers render nothing for null.
 */
export function deriveThreadRecap(input: {
  readonly goal: string | null;
  readonly model: AgentPanelModel;
  readonly latestAssistantText: string | null;
}): ThreadRecap | null {
  const agents = [
    ...input.model.workflows.flatMap((group) => [
      ...group.phases.flatMap((phase) => phase.members),
      ...group.unphasedMembers,
    ]),
    ...input.model.directAgents,
  ];
  const goal = input.goal === null ? null : oneLine(input.goal, GOAL_MAX_CHARS);
  const nextSteps =
    input.latestAssistantText === null ? [] : parseReportNextSteps(input.latestAssistantText);

  if (goal === null && !input.model.hasAgents && nextSteps.length === 0) return null;

  return {
    goal,
    working: input.model.liveCount,
    settled: input.model.settledCount,
    latestActivity: latestLiveActivity(agents),
    nextSteps,
  };
}

/** The recap as one line, in the order a reader scans it: goal, work, next. */
export function formatThreadRecap(recap: ThreadRecap): string {
  const parts: string[] = [];
  if (recap.goal !== null) parts.push(`Goal: ${recap.goal}`);
  if (recap.working > 0) {
    const noun = recap.working === 1 ? "agent" : "agents";
    parts.push(
      recap.latestActivity === null
        ? `${recap.working} ${noun} working`
        : `${recap.working} ${noun} working · ${recap.latestActivity}`,
    );
  } else if (recap.settled > 0) {
    parts.push(`${recap.settled} settled`);
  }
  if (recap.nextSteps.length > 0) parts.push(`Next: ${recap.nextSteps.join(" · ")}`);
  return parts.join("  ·  ");
}
