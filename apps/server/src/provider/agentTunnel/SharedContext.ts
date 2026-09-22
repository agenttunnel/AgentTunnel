export type AgentTunnelContextSource = "orchestrator" | "sub-agent";

export interface AgentTunnelContextEntry {
  readonly key: string;
  readonly value: string;
  readonly source: AgentTunnelContextSource;
  readonly authorId?: string;
}

const KEY_PATTERN = /^[a-z][a-z0-9_]*$/;

/**
 * One block of a key's value: the notebook value an orchestrator set, or the
 * text one sub-agent contributed. Kept per block because replacing an author's
 * contribution has to be exact — a retried agent overwrites its earlier
 * attempt instead of stacking a second copy into the same key.
 */
interface AgentTunnelContextBlock {
  readonly value: string;
  readonly authorId?: string;
}

/** Structured, bounded-by-reference memory shared by the workflow agents. */
export class AgentTunnelSharedContext {
  private readonly entries = new Map<string, AgentTunnelContextEntry>();
  private readonly blocks = new Map<string, Array<AgentTunnelContextBlock>>();

  set(key: string, value: string, source: AgentTunnelContextSource = "orchestrator"): void {
    this.assertKey(key);
    this.blocks.set(key, [{ value: value.trim() }]);
    this.entries.set(key, {
      key,
      value: value.trim(),
      source,
    });
  }

  append(key: string, value: string, authorId: string): void {
    this.assertKey(key);
    const blocks = this.blocks.get(key) ?? [];
    blocks.push({ value: value.trim(), authorId });
    this.write(key, blocks, authorId);
  }

  /**
   * Replace one author's contribution, keeping every other block in place. The
   * first block of a key renders bare; the rest carry their author stamp, so a
   * replaced block reads exactly like the append it supersedes.
   */
  replaceAuthor(key: string, value: string, authorId: string): void {
    this.assertKey(key);
    const blocks = this.blocks.get(key) ?? [];
    const index = blocks.findIndex((block) => block.authorId === authorId);
    if (index < 0) {
      this.append(key, value, authorId);
      return;
    }
    this.write(
      key,
      blocks.map((block, position) =>
        position === index ? { value: value.trim(), authorId } : block,
      ),
      authorId,
    );
  }

  list(): ReadonlyArray<AgentTunnelContextEntry> {
    return [...this.entries.values()].sort((left, right) => left.key.localeCompare(right.key));
  }

  /** Renders the blocks and stores the joined value as this author's entry. */
  private write(
    key: string,
    blocks: ReadonlyArray<AgentTunnelContextBlock>,
    authorId: string,
  ): void {
    this.blocks.set(key, [...blocks]);
    const rendered = blocks
      .map((block, position) =>
        block.authorId === undefined || position === 0
          ? block.value
          : `\n\n---\n(from sub-agent ${block.authorId})\n\n${block.value}`,
      )
      .join("");
    this.entries.set(key, { key, value: rendered, source: "sub-agent", authorId });
  }

  renderForPrompt(keys?: ReadonlyArray<string>): string {
    const selected = keys === undefined ? this.list() : keys.map((key) => this.entries.get(key));
    const blocks: string[] = [];

    for (const entry of selected) {
      if (!entry) continue;
      const author = entry.source === "sub-agent" ? ` (from ${entry.authorId ?? "sub-agent"})` : "";
      blocks.push(`### ${entry.key}${author}\n\n${entry.value}`);
    }

    return blocks.length > 0 ? blocks.join("\n\n") : "(no shared context yet)";
  }

  private assertKey(key: string): void {
    if (!KEY_PATTERN.test(key)) {
      throw new Error(`invalid shared context key: ${JSON.stringify(key)}`);
    }
  }
}

const SECTION_PATTERN = /^### (\S+)(?: \(from .+\))?$/u;
const AUTHOR_SUFFIX = / \(from .+\)$/u;

/**
 * Reads a rendered notebook back into its keys. A thread that resumes rehydrates
 * the notebook the previous turn ended with instead of nesting that dump as one
 * opaque blob, so the orchestrator reads its own past keys directly.
 *
 * A section header is a `### key` line followed by a blank line — the shape
 * `renderForPrompt` writes — so markdown headings inside a value cannot split
 * it. Sections whose key is not a valid notebook key are skipped rather than
 * throwing: a notebook written by an older build must not break a new turn.
 */
export function parseSharedContextDump(dump: string): ReadonlyMap<string, string> {
  const sections = new Map<string, string>();
  const lines = dump.split("\n");
  let key: string | null = null;
  let value: string[] = [];
  const flush = () => {
    if (key === null) return;
    const rendered = value.join("\n").trim();
    if (rendered.length > 0) sections.set(key, rendered);
    value = [];
  };

  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index] ?? "";
    const isHeader = SECTION_PATTERN.test(line) && (lines[index + 1] ?? "").trim().length === 0;
    if (isHeader) {
      flush();
      const candidate = line.slice("### ".length).replace(AUTHOR_SUFFIX, "").trim();
      key = KEY_PATTERN.test(candidate) ? candidate : null;
      continue;
    }
    if (key !== null) value.push(line);
  }
  flush();
  return sections;
}
