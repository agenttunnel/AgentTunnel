// Pure wire normalization and reducers. No React Native imports here, so the
// bun smoke (scripts/smoke.ts) runs this exact code. Wire shapes mirror
// internal/transport/rpc.go (ThreadSummary, ProjectView, graphEvents) and
// internal/domain/state.go (ThreadState) — the same fields the desktop reads.

export type AgentStatus = "pending" | "running" | "succeeded" | "failed" | "cancelled";

export type WireAgent = {
  id: string;
  parentAgentId?: string;
  name?: string;
  role: string;
  harness: string;
  task: string;
  status: AgentStatus;
};

export type WireMessage = { id: string; author: string; text: string; at: string };

export type WireMailbox = {
  from: string;
  to: string;
  kind: string;
  body: string;
  at: string;
  turn: number;
};

export type ThreadSummary = {
  id: string;
  title: string;
  projectId?: string;
  updatedAt: string;
  messages: number;
  agents: number;
  parentThreadId?: string;
  runningAgents: number;
  stale: boolean;
};

export type ProjectView = { id: string; name: string; path: string; threads: number };

/** The slice of domain.ThreadState the mobile chat renders. */
export type ThreadSnapshot = {
  id: string;
  exists: boolean;
  title: string;
  projectId?: string;
  updatedAt: string;
  turn: number;
  messages: WireMessage[];
  agents: Record<string, WireAgent>;
  agentOrder: string[];
  mailbox: WireMailbox[];
};

/** One line of the live activity log (thread.logs rows and agent.output frames). */
export type LiveLine = { agentId: string; kind: string; text: string };

export const LIVE_LIMIT = 400;

// ---------- thread list ----------

export type ThreadRow = ThreadSummary & { depth: number };
export type ThreadSection = { key: string; name: string; threads: ThreadRow[] };

/**
 * Orders threads so each subthread follows its parent, indented one level. A
 * subthread whose parent is absent shows at the top level; no thread is hidden.
 */
export function arrangeThreads<T extends { id: string; parentThreadId?: string }>(
  list: T[],
): (T & { depth: number })[] {
  const ids = new Set(list.map((t) => t.id));
  const children = new Map<string, T[]>();
  const roots: T[] = [];
  for (const t of list) {
    const parent = t.parentThreadId;
    if (parent && parent !== t.id && ids.has(parent)) {
      const siblings = children.get(parent) ?? [];
      siblings.push(t);
      children.set(parent, siblings);
    } else {
      roots.push(t);
    }
  }
  const out: (T & { depth: number })[] = [];
  const placed = new Set<string>();
  const place = (t: T, depth: number) => {
    if (placed.has(t.id)) return;
    placed.add(t.id);
    out.push({ ...t, depth });
    for (const child of children.get(t.id) ?? []) place(child, depth + 1);
  };
  for (const t of roots) place(t, 0);
  // A parent cycle can leave threads unreached from any root; surface them.
  for (const t of list) place(t, 0);
  return out;
}

/**
 * Groups the daemon's recency-sorted thread list by project, in project order.
 * Threads whose project is unknown land in a trailing "Other" section.
 */
export function groupThreads(threads: ThreadSummary[], projects: ProjectView[]): ThreadSection[] {
  const byProject = new Map<string, ThreadSummary[]>();
  const orphans: ThreadSummary[] = [];
  const known = new Set(projects.map((p) => p.id));
  for (const t of threads) {
    if (t.projectId && known.has(t.projectId)) {
      const list = byProject.get(t.projectId) ?? [];
      list.push(t);
      byProject.set(t.projectId, list);
    } else {
      orphans.push(t);
    }
  }
  const sections: ThreadSection[] = [];
  for (const p of projects) {
    const list = byProject.get(p.id);
    if (list && list.length > 0) {
      sections.push({ key: p.id, name: p.name, threads: arrangeThreads(list) });
    }
  }
  if (orphans.length > 0) {
    sections.push({ key: "", name: "Other", threads: arrangeThreads(orphans) });
  }
  return sections;
}

// ---------- activity log ----------

/** Appends one output line. Harnesses stream text in chunks, so a text or
 *  reasoning chunk from the same agent grows the last block instead of stacking. */
export function appendLiveLine(prev: LiveLine[], line: LiveLine): LiveLine[] {
  const last = prev[prev.length - 1];
  if (
    last &&
    last.agentId === line.agentId &&
    last.kind === line.kind &&
    (line.kind === "text" || line.kind === "reasoning")
  ) {
    return [...prev.slice(0, -1), { ...last, text: last.text + line.text }];
  }
  return [...prev.slice(-(LIVE_LIMIT - 1)), line];
}

/**
 * Frames published while the snapshot loads may already be in the snapshot:
 * the daemon appends a line before publishing it. Drops the longest run at the
 * start of `buffered` that repeats the snapshot's tail, keeping only new lines.
 */
export function dropSnapshotOverlap(snapshot: LiveLine[], buffered: LiveLine[]): LiveLine[] {
  const same = (a: LiveLine, b: LiveLine) =>
    a.agentId === b.agentId && a.kind === b.kind && a.text === b.text;
  for (let k = Math.min(snapshot.length, buffered.length); k > 0; k--) {
    const tail = snapshot.slice(snapshot.length - k);
    if (tail.every((line, i) => {
      const b = buffered[i];
      return b !== undefined && same(line, b);
    })) return buffered.slice(k);
  }
  return buffered;
}

/** Reads an agent.output frame, or null for any other thread.event. */
export function outputFromFrame(
  raw: unknown,
): { threadId: string; line: LiveLine } | null {
  if (!isRecord(raw) || raw.type !== "agent.output" || !isRecord(raw.payload)) return null;
  const p = raw.payload;
  return {
    threadId: String(raw.threadId ?? ""),
    line: {
      agentId: String(p.agentId ?? ""),
      kind: String(p.kind ?? "text"),
      text: String(p.text ?? ""),
    },
  };
}

/** Every thread.event except agent output means the snapshot may have changed. */
export function isStateFrame(raw: unknown): boolean {
  return isRecord(raw) && typeof raw.type === "string" && raw.type !== "agent.output";
}

// ---------- turn + agents ----------

export function isTurnActive(snapshot: Pick<ThreadSnapshot, "agents" | "agentOrder">): boolean {
  return snapshot.agentOrder.some((id) => {
    const s = snapshot.agents[id]?.status;
    return s === "running" || s === "pending";
  });
}

export function agentLabel(agent: WireAgent | undefined, fallbackId: string): string {
  if (!agent) return fallbackId;
  return agent.name && agent.name.trim() ? agent.name : agent.role || fallbackId;
}

/** Resolves a mailbox endpoint id to a readable label; "orchestrator" stays as is. */
export function mailboxLine(
  entry: WireMailbox,
  agents: Record<string, WireAgent>,
): { from: string; to: string } {
  return {
    from: agentLabel(agents[entry.from], entry.from),
    to: agentLabel(agents[entry.to], entry.to),
  };
}

// ---------- markdown-lite ----------

export type Span = { kind: "text" | "code" | "bold"; text: string };
export type MdBlock =
  | { kind: "para"; spans: Span[] }
  | { kind: "heading"; level: number; spans: Span[] }
  | { kind: "bullet"; spans: Span[] }
  | { kind: "code"; lang: string; text: string };

/**
 * Parses the subset agents actually write: fenced code, headings, bullets,
 * paragraphs, inline `code` and **bold**. Anything else stays plain text.
 */
export function parseMarkdownLite(source: string): MdBlock[] {
  const blocks: MdBlock[] = [];
  const lines = source.replace(/\r\n?/g, "\n").split("\n");
  let para: string[] = [];
  const flush = () => {
    if (para.length > 0) {
      blocks.push({ kind: "para", spans: inlineSpans(para.join(" ")) });
      para = [];
    }
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? "";
    const fence = /^\s*```\s*([\w+-]*)\s*$/.exec(line);
    if (fence) {
      flush();
      const body: string[] = [];
      i++;
      while (i < lines.length && !/^\s*```\s*$/.test(lines[i] ?? "")) body.push(lines[i++] ?? "");
      blocks.push({ kind: "code", lang: fence[1] ?? "", text: body.join("\n") });
      continue;
    }
    const heading = /^(#{1,3})\s+(.*)$/.exec(line);
    if (heading) {
      flush();
      blocks.push({ kind: "heading", level: (heading[1] ?? "").length, spans: inlineSpans(heading[2] ?? "") });
      continue;
    }
    const bullet = /^\s*[-*]\s+(.*)$/.exec(line);
    if (bullet) {
      flush();
      blocks.push({ kind: "bullet", spans: inlineSpans(bullet[1] ?? "") });
      continue;
    }
    if (line.trim() === "") {
      flush();
      continue;
    }
    para.push(line.trim());
  }
  flush();
  return blocks;
}

export function inlineSpans(text: string): Span[] {
  const spans: Span[] = [];
  const re = /`([^`]+)`|\*\*([^*]+)\*\*/g;
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) spans.push({ kind: "text", text: text.slice(last, m.index) });
    if (m[1] !== undefined) spans.push({ kind: "code", text: m[1] });
    else spans.push({ kind: "bold", text: m[2] ?? "" });
    last = m.index + m[0].length;
  }
  if (last < text.length) spans.push({ kind: "text", text: text.slice(last) });
  return spans;
}

// ---------- helpers ----------

/** Short relative label for "last activity", e.g. "now", "5m", "3h", "2d". */
export function relativeTime(iso: string, nowMs: number): string {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return "";
  const s = Math.max(0, Math.floor((nowMs - then) / 1000));
  if (s < 60) return "now";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

/** Message text for a caught error. Never includes request params or tokens. */
export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : "Something went wrong";
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

export { isRecord };
