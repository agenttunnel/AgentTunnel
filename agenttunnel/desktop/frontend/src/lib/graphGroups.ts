// Client-side agent groups for the graph: pure selection logic and the group
// prompt builder. Groups are UI state only; nothing here touches the daemon.

/** A set of agents the user picked together. Members are agent ids of one thread. */
export type Group = { id: string; members: string[] };

/** A box that can be measured on the graph canvas. */
export type Box = { id: string; x: number; y: number; width: number; height: number };

/** Edge gap (px) under which a dragged node shows the target's proximity rings. */
export const RING_GAP = 110;
/** Edge gap (px) under which a dragged node magnetically snaps beside its target. */
export const SNAP_GAP = 40;
/** Space left between a snapped node and its target. */
const SLOT_GAP = 12;

/** Per-member and total caps on the output tail sent in a group prompt. */
const MEMBER_CHARS = 1500;
const TOTAL_CHARS = 12000;
const MEMBER_LINES = 20;
/** Cap on one member's Task line, including its "Task: " prefix and ellipsis. */
const TASK_CHARS = 500;

const clip = (s: string, max: number) => (s.length > max ? `${s.slice(0, max - 1)}…` : s);

/** A group with fewer than two members is not a group. */
const dissolve = (groups: Group[]) => groups.filter((g) => g.members.length >= 2);

/** Forget members that no longer exist, then dissolve groups left too small. */
export function pruneGroups(groups: Group[], live: Set<string>): Group[] {
  return dissolve(groups.map((g) => ({ ...g, members: g.members.filter((id) => live.has(id)) })));
}

const groupOf = (groups: Group[], id: string) => groups.find((g) => g.members.includes(id));

/** True when both agents are members of the same group. */
export function inSameGroup(groups: Group[], a: string, b: string): boolean {
  const g = groupOf(groups, a);
  return !!g && g.members.includes(b);
}

const nextGroupId = (groups: Group[]) => {
  const n = groups.reduce((max, g) => Math.max(max, Number(g.id.slice(1)) || 0), 0);
  return `g${n + 1}`;
};

/** Drop dragged node `a` onto node `b`: joins b's group, or starts one with it. */
export function mergeNodes(groups: Group[], a: string, b: string): Group[] {
  if (a === b) return groups;
  const ga = groupOf(groups, a);
  const gb = groupOf(groups, b);
  if (ga && ga === gb) return groups;
  if (ga && gb) {
    const members = [...ga.members, ...gb.members.filter((id) => !ga.members.includes(id))];
    return groups.filter((g) => g !== gb).map((g) => (g === ga ? { ...g, members } : g));
  }
  if (ga) return groups.map((g) => (g === ga ? { ...g, members: [...g.members, b] } : g));
  if (gb) return groups.map((g) => (g === gb ? { ...g, members: [...g.members, a] } : g));
  return [...groups, { id: nextGroupId(groups), members: [a, b] }];
}

/** Take one agent out of its group; a group left with a single member dissolves. */
export function removeFromGroup(groups: Group[], id: string): Group[] {
  return dissolve(
    groups.map((g) => (g.members.includes(id) ? { ...g, members: g.members.filter((m) => m !== id) } : g)),
  );
}

export function ungroup(groups: Group[], groupId: string): Group[] {
  return groups.filter((g) => g.id !== groupId);
}

/** The live node nearest the dragged one, measured as the gap between their edges. */
export function nearestNode(dragged: Box, others: Box[]): { target: Box; gap: number } | null {
  let best: { target: Box; gap: number } | null = null;
  const cx = dragged.x + dragged.width / 2;
  const cy = dragged.y + dragged.height / 2;
  for (const o of others) {
    if (o.id === dragged.id) continue;
    const gapX = Math.abs(o.x + o.width / 2 - cx) - (o.width + dragged.width) / 2;
    const gapY = Math.abs(o.y + o.height / 2 - cy) - (o.height + dragged.height) / 2;
    const gap = Math.max(gapX, gapY, 0);
    if (!best || gap < best.gap) best = { target: o, gap };
  }
  return best;
}

/** Where a snapped node sits: beside the target, on the side the drag came from. */
export function snapSlot(dragged: Box, target: Box): { x: number; y: number } {
  const dx = dragged.x + dragged.width / 2 - (target.x + target.width / 2);
  const dy = dragged.y + dragged.height / 2 - (target.y + target.height / 2);
  if (Math.abs(dx) >= Math.abs(dy)) {
    return {
      x: dx < 0 ? target.x - dragged.width - SLOT_GAP : target.x + target.width + SLOT_GAP,
      y: target.y,
    };
  }
  return {
    x: target.x,
    y: dy < 0 ? target.y - dragged.height - SLOT_GAP : target.y + target.height + SLOT_GAP,
  };
}

export type GroupMember = {
  name: string;
  role: string;
  status: string;
  task?: string;
  /** Agent output log, oldest first. Only text and error lines are kept. */
  logs: { kind: string; text: string }[];
};

/** Last lines of one member's output, capped by lines and characters (newest kept). */
function tailOf(logs: GroupMember["logs"], budget: number): string {
  if (budget <= 0) return "";
  const lines: string[] = [];
  let used = 0;
  for (let i = logs.length - 1; i >= 0 && lines.length < MEMBER_LINES; i--) {
    const text = logs[i].text.trim();
    if (!text) continue;
    if (used + text.length > budget) {
      if (lines.length === 0) lines.unshift(text.slice(-budget));
      break;
    }
    lines.unshift(text);
    used += text.length;
  }
  return lines.join("\n");
}

/** The prompt a group turn sends: the user's ask, then each member's state and output tail. */
export function buildGroupPrompt(task: string, members: GroupMember[]): string {
  const goals = members.map((m) => (m.task?.trim() ? clip(`Task: ${m.task.trim()}`, TASK_CHARS) : ""));
  // Task lines count against the envelope, so the log budget is what remains after them.
  const goalChars = goals.reduce((sum, g) => sum + g.length, 0);
  const budget = Math.min(
    MEMBER_CHARS,
    Math.floor(Math.max(TOTAL_CHARS - goalChars, 0) / Math.max(members.length, 1)),
  );
  const sections = members.map((m, i) => {
    const head = `### ${m.name} (${m.role}, ${m.status})`;
    const output = tailOf(
      m.logs.filter((l) => l.kind === "text" || l.kind === "error"),
      budget,
    );
    return [head, goals[i], output ? `Recent output:\n${output}` : "No output yet."]
      .filter(Boolean)
      .join("\n");
  });
  return [task.trim(), "## Group context", ...sections].join("\n\n");
}
