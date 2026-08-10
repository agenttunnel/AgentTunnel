export type AgentTunnelContextSource = "orchestrator" | "sub-agent";

export interface AgentTunnelContextEntry {
  readonly key: string;
  readonly value: string;
  readonly source: AgentTunnelContextSource;
  readonly authorId?: string;
}

const KEY_PATTERN = /^[a-z][a-z0-9_]*$/;

/** Structured, bounded-by-reference memory shared by the workflow agents. */
export class AgentTunnelSharedContext {
  private readonly entries = new Map<string, AgentTunnelContextEntry>();

  set(key: string, value: string, source: AgentTunnelContextSource = "orchestrator"): void {
    this.assertKey(key);
    this.entries.set(key, {
      key,
      value: value.trim(),
      source,
    });
  }

  append(key: string, value: string, authorId: string): void {
    this.assertKey(key);
    const existing = this.entries.get(key);
    const stamped = `\n\n---\n(from sub-agent ${authorId})\n\n${value.trim()}`;
    this.entries.set(key, {
      key,
      value: existing ? `${existing.value}${stamped}` : value.trim(),
      source: "sub-agent",
      authorId,
    });
  }

  list(): ReadonlyArray<AgentTunnelContextEntry> {
    return [...this.entries.values()].sort((left, right) => left.key.localeCompare(right.key));
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
