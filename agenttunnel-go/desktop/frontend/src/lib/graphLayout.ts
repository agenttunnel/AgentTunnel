// Layered layout for the aside's agent-team graph. Pure: given the same thread
// state it produces the same nodes, edges, and canvas size — no React, no DOM,
// no dependencies — so a bad payload can never loop the UI.

export type GraphAgent = {
  id: string;
  parentAgentId?: string;
  name?: string;
  role: string;
  task: string;
  status: string;
  startedAt?: string;
  dependsOn?: string[];
};

export type GraphMailboxMessage = {
  from: string;
  to: string;
  kind: string;
  body: string;
  at?: string;
};

export type GraphNode = {
  key: string;
  id: string;
  name: string;
  role: string;
  status: string;
  depth: number;
  x: number;
  y: number;
  usage: number | null;
  task: string;
};

export type GraphEdge = {
  key: string;
  from: string;
  to: string;
  kind: "spawn" | "depends" | "message";
  count: number;
  fromId: string;
  toId: string;
};

export type Graph = {
  nodes: GraphNode[];
  edges: GraphEdge[];
  width: number;
  height: number;
};

export const NODE_W = 150;
export const NODE_H = 44;
export const GAP_X = 26;
export const LAYER_H = 104;
export const MARGIN = 20;

/**
 * layoutGraph layers the agents by parent-chain depth (orchestrator on top,
 * subagents below their spawner), orders each layer by agentOrder, and derives
 * the edges: spawn from parentAgentId, depends from dependsOn task names,
 * messages aggregated per ordered mailbox pair.
 *
 * Legacy protocol paths can mint agent-<name> ids that repeat across passes,
 * so nodes key on id + startedAt (disambiguated further on collision) and
 * every id resolution goes through a visited set.
 */
export function layoutGraph(
  agents: Record<string, GraphAgent> | null,
  agentOrder: string[] | null,
  mailbox: GraphMailboxMessage[],
  usageByAgentId?: Record<string, number> | null,
): Graph {
  const record = agents ?? {};
  const order = (agentOrder ?? Object.keys(record)).filter((id) => record[id]);
  if (order.length === 0) return { nodes: [], edges: [], width: 0, height: 0 };

  const usedKeys = new Set<string>();
  const rows: { key: string; agent: GraphAgent; parentKey: string | null }[] = [];
  for (const id of order) {
    const agent = record[id];
    const base = agent.startedAt ? `${agent.id}@${agent.startedAt}` : agent.id;
    let key = base;
    let n = 2;
    while (usedKeys.has(key)) key = `${base}#${n++}`;
    usedKeys.add(key);
    rows.push({ key, agent, parentKey: null });
  }

  // A raw agent id refers to its latest occurrence, so current-pass agents
  // anchor the spawn and message edges.
  const lastKeyById = new Map<string, string>();
  for (const row of rows) lastKeyById.set(row.agent.id, row.key);
  for (const row of rows) {
    if (!row.agent.parentAgentId) continue;
    const parentKey = lastKeyById.get(row.agent.parentAgentId) ?? null;
    if (parentKey !== row.key) row.parentKey = parentKey;
  }

  // Depth via memoized parent walk; the visiting set keeps a malformed parent
  // chain (or repeated ids) from looping the layout — a cycle breaks at 0.
  const rowByKey = new Map(rows.map((r) => [r.key, r]));
  const depthByKey = new Map<string, number>();
  const depthOf = (key: string, visiting: Set<string>): number => {
    const memo = depthByKey.get(key);
    if (memo != null) return memo;
    const parentKey = rowByKey.get(key)?.parentKey ?? null;
    if (!parentKey || visiting.has(parentKey)) return 0;
    visiting.add(key);
    const depth = depthOf(parentKey, visiting) + 1;
    visiting.delete(key);
    depthByKey.set(key, depth);
    return depth;
  };
  const depth = (key: string) => depthOf(key, new Set());

  // Layer the rows: depth levels, agentOrder reading order within a layer.
  const layers = new Map<number, typeof rows>();
  for (const row of rows) {
    const d = depth(row.key);
    const layer = layers.get(d);
    if (layer) layer.push(row);
    else layers.set(d, [row]);
  }

  const nodes: GraphNode[] = [];
  const nodeByKey = new Map<string, GraphNode>();
  let width = 0;
  let maxDepth = 0;
  for (const [d, layer] of [...layers.entries()].sort(([a], [b]) => a - b)) {
    layer.forEach((row, col) => {
      const node: GraphNode = {
        key: row.key,
        id: row.agent.id,
        name: row.agent.name || row.agent.role,
        role: row.agent.role,
        status: row.agent.status,
        depth: d,
        x: MARGIN + col * (NODE_W + GAP_X),
        y: MARGIN + d * LAYER_H,
        usage: usageByAgentId?.[row.agent.id] ?? null,
        task: row.agent.task,
      };
      nodes.push(node);
      nodeByKey.set(node.key, node);
    });
    maxDepth = Math.max(maxDepth, d);
    const w = MARGIN * 2 + layer.length * NODE_W + (layer.length - 1) * GAP_X;
    if (w > width) width = w;
  }
  const height = MARGIN * 2 + maxDepth * LAYER_H + NODE_H;

  // Edges aggregate per (kind, from, to) so repeated dispatches or messages
  // between the same pair collapse into one line.
  const edges = new Map<string, GraphEdge>();
  const addEdge = (
    kind: GraphEdge["kind"],
    fromKey: string | null,
    toKey: string | null,
    fromId: string,
    toId: string,
  ) => {
    if (!fromKey || !toKey || fromKey === toKey) return;
    if (!nodeByKey.has(fromKey) || !nodeByKey.has(toKey)) return;
    const key = `${kind}:${fromKey}→${toKey}`;
    const existing = edges.get(key);
    if (existing) existing.count++;
    else edges.set(key, { key, from: fromKey, to: toKey, kind, count: 1, fromId, toId });
  };
  for (const row of rows) {
    if (row.parentKey) addEdge("spawn", row.parentKey, row.key, row.agent.parentAgentId!, row.agent.id);
  }
  for (const row of rows) {
    for (const dep of row.agent.dependsOn ?? []) {
      // A dependency names the task (or agent name) it waits on; the latest
      // match wins, unresolvable names are skipped.
      let target: typeof row | null = null;
      for (const cand of rows) if (cand.agent.task === dep || cand.agent.name === dep) target = cand;
      if (target) addEdge("depends", target.key, row.key, target.agent.id, row.agent.id);
    }
  }
  for (const m of mailbox) {
    // Aggregate per ordered pair of resolved nodes, carrying the raw ids so
    // the view can list the pair's messages.
    addEdge("message", lastKeyById.get(m.from) ?? null, lastKeyById.get(m.to) ?? null, m.from, m.to);
  }

  return { nodes, edges: [...edges.values()], width, height };
}
