// Agent-graph layout: pure geometry, no React. computeGraph turns the thread's
// agents (spawn tree, task dependencies) and mailbox (agent-to-agent messages)
// into positioned nodes plus edge paths in a fixed coordinate space.
// Deterministic for a given input, so the component only maps over the result
// and can memoize it.

import { totalTokens, type TokenUsage } from "./usage";

/** One agent-to-agent message, as delivered in thread state. `kind` is
 * "message" for current producers; older threads may still carry
 * "decision-request", "note", or "reply" — it is surfaced as-is. */
export type MailboxMessage = {
  from: string;
  to: string;
  kind: string;
  body: string;
  at: string;
};

/** One task-dependency edge: `to` waits on `from` before starting. */
export type DependEdge = {
  key: string;
  from: string;
  to: string;
  path: string;
};

/** One ordered pair's messages, oldest first. */
export type MessagePair = {
  from: string;
  to: string;
  count: number;
  last: string;
  items: MailboxMessage[];
};

export type GraphNode = {
  id: string;
  x: number;
  y: number;
  width: number;
  height: number;
  name: string;
  role: string;
  status: string;
  /** Total tokens for the node's token chip; 0 when no usage is recorded. */
  tokens: number;
};

export type SpawnEdge = {
  key: string;
  from: string;
  to: string;
  path: string;
};

export type MessageEdge = {
  key: string;
  from: string;
  to: string;
  count: number;
  last: string;
  path: string;
  labelX: number;
  labelY: number;
};

export type GraphLayout = {
  width: number;
  height: number;
  nodes: GraphNode[];
  spawnEdges: SpawnEdge[];
  dependEdges: DependEdge[];
  messageEdges: MessageEdge[];
};

type GraphAgentInput = {
  id: string;
  parentAgentId?: string;
  name?: string;
  role: string;
  status: string;
  task?: string;
  dependsOn?: string[];
};

// Fixed coordinate space; the constants only change with a deliberate
// redesign of the node chrome.
const NODE_W = 200;
const NODE_H = 56;
const ROW_GAP = 130;
const COL_GAP = 64;
const PAD = 32;
// Headroom above row 0 for self-loop arcs, which rise above their node.
const PAD_TOP = 48;

type Rect = { x: number; y: number; w: number; h: number; cx: number; cy: number };

const timeOf = (iso: string) => {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? 0 : t;
};

export function mailboxKey(from: string, to: string): string {
  return `${from}\u0000${to}`;
}

/** Order-independent key for a conversation between two agents. */
export function conversationKey(a: string, b: string): string {
  return a < b ? `${a}\u0000${b}` : `${b}\u0000${a}`;
}

/** Every message exchanged between the two agents in EITHER direction,
 * oldest first — the conversation an arrow click opens. */
export function conversationBetween(
  mailbox: MailboxMessage[] | null,
  a: string,
  b: string,
): MailboxMessage[] {
  return (mailbox ?? [])
    .filter((m) => (m.from === a && m.to === b) || (m.from === b && m.to === a))
    .sort((x, y) => timeOf(x.at) - timeOf(y.at));
}

/** Groups the mailbox per ORDERED pair {from,to}: A→B and B→A are separate
 * conversations. Entries missing an endpoint cannot be routed and are
 * skipped. Items are sorted by time (stable sort keeps arrival order on
 * ties). */
export function aggregateMailbox(mailbox: MailboxMessage[] | null): Map<string, MessagePair> {
  const pairs = new Map<string, MessagePair>();
  for (const m of mailbox ?? []) {
    if (!m?.from || !m?.to) continue;
    const key = mailboxKey(m.from, m.to);
    let pair = pairs.get(key);
    if (!pair) {
      pair = { from: m.from, to: m.to, count: 0, last: m.at, items: [] };
      pairs.set(key, pair);
    }
    pair.items.push(m);
    pair.count++;
    if (timeOf(m.at) > timeOf(pair.last)) pair.last = m.at;
  }
  for (const pair of pairs.values()) pair.items.sort((a, b) => timeOf(a.at) - timeOf(b.at));
  return pairs;
}

/** Computes the full graph: layered nodes, spawn edges, dashed dependency
 * edges, and message arcs. Row 0 holds the orchestrator (the parentless
 * agent leading agentOrder, falling back to the conventional
 * "agent-orchestrator" id); each parent's children stack in the row beneath,
 * in agentOrder sequence. A missing or cyclic parent demotes the agent to a
 * root so it is never dropped. */
/** `positions` pins nodes the user dragged; every other node keeps its row slot. */
export function computeGraph(input: {
  agents: GraphAgentInput[];
  roleNames: Record<string, string>;
  usage: Record<string, { usage: TokenUsage }> | null;
  mailbox: MailboxMessage[] | null;
  positions?: Record<string, { x: number; y: number }>;
}): GraphLayout {
  const { agents, roleNames, usage, mailbox, positions } = input;
  const byId = new Map(agents.map((a) => [a.id, a]));

  // Spawn depth: 0 for the orchestrator, parent depth + 1 below. The
  // pre-seeded cache value breaks parent cycles by treating the repeated
  // agent as a root mid-walk.
  const depths = new Map<string, number>();
  const depthOf = (id: string): number => {
    const cached = depths.get(id);
    if (cached !== undefined) return cached;
    depths.set(id, 0);
    const parent = byId.get(id)?.parentAgentId ?? "";
    const depth = parent && byId.has(parent) ? depthOf(parent) + 1 : 0;
    depths.set(id, depth);
    return depth;
  };
  for (const a of agents) depthOf(a.id);

  const childrenOf = new Map<string, string[]>();
  for (const a of agents) {
    const parent = a.parentAgentId ?? "";
    if (!parent || !byId.has(parent)) continue;
    const list = childrenOf.get(parent);
    if (list) list.push(a.id);
    else childrenOf.set(parent, [a.id]);
  }

  // Depth-first stacking keeps each subtree contiguous on its row; siblings
  // stay in agentOrder sequence.
  const order = new Map(agents.map((a, i) => [a.id, i]));
  const rootRank = (id: string) => (id === "agent-orchestrator" ? -1e6 : 0) + (order.get(id) ?? 0);
  const roots = agents
    .filter((a) => (depths.get(a.id) ?? 0) === 0)
    .sort((a, b) => rootRank(a.id) - rootRank(b.id));

  const rows: string[][] = [];
  const visited = new Set<string>();
  const visit = (id: string) => {
    if (visited.has(id)) return;
    visited.add(id);
    const depth = depths.get(id) ?? 0;
    const row = rows[depth] ?? (rows[depth] = []);
    row.push(id);
    for (const child of childrenOf.get(id) ?? []) visit(child);
  };
  for (const r of roots) visit(r.id);
  // Orphans the walk never reached (broken/cyclic parent chains) still get a
  // deterministic spot on their depth row.
  for (const a of agents) {
    if (visited.has(a.id)) continue;
    const depth = depths.get(a.id) ?? 0;
    const row = rows[depth] ?? (rows[depth] = []);
    row.push(a.id);
  }

  const rects = new Map<string, Rect>();
  const nodes: GraphNode[] = [];
  rows.forEach((row, depth) => {
    row.forEach((id, col) => {
      const a = byId.get(id)!;
      const pinned = positions?.[id];
      const x = pinned ? pinned.x : PAD + col * (NODE_W + COL_GAP);
      const y = pinned ? pinned.y : PAD_TOP + depth * (NODE_H + ROW_GAP);
      rects.set(id, { x, y, w: NODE_W, h: NODE_H, cx: x + NODE_W / 2, cy: y + NODE_H / 2 });
      const u = usage?.[id]?.usage;
      nodes.push({
        id,
        x,
        y,
        width: NODE_W,
        height: NODE_H,
        name: a.name || roleNames[a.role] || a.role,
        role: roleNames[a.role] || a.role,
        status: a.status,
        tokens: u ? totalTokens(u) : 0,
      });
    });
  });

  const spawnEdges: SpawnEdge[] = [];
  for (const a of agents) {
    const parent = a.parentAgentId ?? "";
    const from = rects.get(parent);
    const to = rects.get(a.id);
    if (!parent || !from || !to) continue;
    const y1 = from.y + from.h;
    const y2 = to.y;
    spawnEdges.push({
      key: `spawn:${parent}:${a.id}`,
      from: parent,
      to: a.id,
      path: `M ${from.cx},${y1} C ${from.cx},${(y1 + y2) / 2} ${to.cx},${(y1 + y2) / 2} ${to.cx},${y2}`,
    });
  }

  // Task dependencies: `dependsOn` names the task (or agent name) it waits
  // on; the latest matching agent wins, unresolved names are skipped.
  const dependEdges: DependEdge[] = [];
  for (const a of agents) {
    for (const dep of a.dependsOn ?? []) {
      let target: GraphAgentInput | null = null;
      for (const cand of agents) {
        if (cand !== a && (cand.task === dep || cand.name === dep)) target = cand;
      }
      const from = target ? rects.get(target.id) : undefined;
      const to = rects.get(a.id);
      if (!target || !from || !to) continue;
      dependEdges.push({
        key: `dep:${target.id}:${a.id}`,
        from: target.id,
        to: a.id,
        path: `M ${from.cx},${from.y + from.h + 4} Q ${(from.cx + to.cx) / 2},${Math.max(from.y + from.h, to.y + to.h) + 22} ${to.cx},${to.y + to.h + 4}`,
      });
    }
  }

  const messageEdges: MessageEdge[] = [];
  for (const pair of aggregateMailbox(mailbox).values()) {
    const from = rects.get(pair.from);
    const to = rects.get(pair.to);
    // Pairs naming agents that are gone from state keep their conversation
    // in the mailbox map but get no arrow on the canvas.
    if (!from || !to) continue;
    const arc = from === to ? selfLoop(from) : messageArc(from, to);
    messageEdges.push({
      key: `msg:${pair.from}:${pair.to}`,
      from: pair.from,
      to: pair.to,
      count: pair.count,
      last: pair.last,
      ...arc,
    });
  }
  messageEdges.sort((a, b) => timeOf(a.last) - timeOf(b.last) || a.key.localeCompare(b.key));

  const width = [...rects.values()].reduce(
    (m, r) => Math.max(m, r.x + r.w + PAD),
    PAD * 2 + NODE_W,
  );
  const height = [...rects.values()].reduce(
    (m, r) => Math.max(m, r.y + r.h + PAD),
    PAD_TOP + NODE_H + PAD,
  );

  return { width, height, nodes, spawnEdges, dependEdges, messageEdges };
}

/** Curved arc between two nodes, bowed perpendicular to the straight line —
 * visually distinct from the spawn edges. Ordered pairs A→B and B→A bow to
 * opposite sides automatically, because the direction vector (and with it the
 * normal) flips, so two-way conversations never overlap. */
function messageArc(a: Rect, b: Rect): { path: string; labelX: number; labelY: number } {
  const dx = b.cx - a.cx;
  const dy = b.cy - a.cy;
  // Anchor on the side of each node that faces the other.
  let x1: number;
  let y1: number;
  let x2: number;
  let y2: number;
  if (Math.abs(dx) >= Math.abs(dy)) {
    x1 = dx > 0 ? a.x + a.w + 3 : a.x - 3;
    y1 = a.cy;
    x2 = dx > 0 ? b.x - 3 : b.x + b.w + 3;
    y2 = b.cy;
  } else {
    x1 = a.cx;
    y1 = dy > 0 ? a.y + a.h + 3 : a.y - 3;
    x2 = b.cx;
    y2 = dy > 0 ? b.y - 3 : b.y + b.h + 3;
  }
  const ex = x2 - x1;
  const ey = y2 - y1;
  const len = Math.hypot(ex, ey) || 1;
  const bow = Math.min(70, Math.max(26, len * 0.22));
  const cx = (x1 + x2) / 2 + (-ey / len) * bow;
  const cy = (y1 + y2) / 2 + (ex / len) * bow;
  return {
    path: `M ${x1},${y1} Q ${cx},${cy} ${x2},${y2}`,
    labelX: 0.25 * x1 + 0.5 * cx + 0.25 * x2,
    labelY: 0.25 * y1 + 0.5 * cy + 0.25 * y2 - 4,
  };
}

/** Self-arc (an agent messaging itself): a small loop rising from the node's
 * top edge. Chosen over omission because it stays trivial and keeps the pair
 * clickable on the canvas. */
function selfLoop(a: Rect): { path: string; labelX: number; labelY: number } {
  const x = a.x + a.w - 44;
  return {
    path: `M ${x},${a.y} C ${x - 6},${a.y - 34} ${x + 30},${a.y - 34} ${x + 30},${a.y - 2}`,
    labelX: x + 12,
    labelY: a.y - 26,
  };
}
