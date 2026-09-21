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
