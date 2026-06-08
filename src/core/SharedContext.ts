/**
 * SharedContext is the orchestrator's structured notebook. The orchestrator LLM
 * writes to it with the `update_context` tool and reads from it (by reference)
 * when dispatching a task. Sub-agents can append findings with `note_finding`.
 *
 * Keys are short, lower-snake-case identifiers. Values are arbitrary markdown
 * text. Values have a source (orchestrator / sub-agent) and timestamps.
 */

export type ContextSource = 'orchestrator' | 'sub-agent' | 'user';

export interface ContextEntry {
  key: string;
  value: string;
  source: ContextSource;
  updatedAt: number;
  /** Sub-agent id that wrote this, if source === 'sub-agent'. */
  authorId?: string;
}

const KEY_PATTERN = /^[a-z][a-z0-9_]*$/;

export class SharedContext {
  private readonly entries = new Map<string, ContextEntry>();

  set(key: string, value: string, source: ContextSource = 'orchestrator', authorId?: string): void {
    if (!KEY_PATTERN.test(key)) {
      throw new Error(
        `invalid context key: ${JSON.stringify(key)} — must match ${KEY_PATTERN.source}`,
      );
    }
    this.entries.set(key, {
      key,
      value,
      source,
      updatedAt: Date.now(),
      authorId,
    });
  }

  get(key: string): ContextEntry | undefined {
    return this.entries.get(key);
  }

  has(key: string): boolean {
    return this.entries.has(key);
  }

  delete(key: string): boolean {
    return this.entries.delete(key);
  }

  list(): ContextEntry[] {
    return [...this.entries.values()].sort((a, b) => a.key.localeCompare(b.key));
  }

  /** Render the given keys as a markdown block suitable for injection into a sub-agent prompt. */
  renderForPrompt(keys: string[]): string {
    const blocks: string[] = [];
    for (const k of keys) {
      const e = this.entries.get(k);
      if (!e) {
        blocks.push(`> ⚠ referenced context key "${k}" was not set; ignoring.`);
        continue;
      }
      const author = e.source === 'sub-agent' ? ` (from sub-agent ${e.authorId ?? '?'})` : '';
      blocks.push(`### ${e.key}${author}\n\n${e.value.trim()}`);
    }
    return blocks.join('\n\n');
  }

  /** Render the full context as a compact summary (for showing to the user). */
  renderSummary(): string {
    if (this.entries.size === 0) return '(no shared context yet)';
    return this.list()
      .map((e) => {
        const firstLine = e.value.trim().split('\n')[0] ?? '';
        const preview = firstLine.length > 80 ? firstLine.slice(0, 79) + '…' : firstLine;
        return `  ${e.key.padEnd(24)} [${e.source}] ${preview}`;
      })
      .join('\n');
  }

  /** Append a finding to an existing entry, or create a new one. */
  append(key: string, value: string, authorId: string): void {
    if (!KEY_PATTERN.test(key)) {
      throw new Error(
        `invalid context key: ${JSON.stringify(key)} — must match ${KEY_PATTERN.source}`,
      );
    }
    const existing = this.entries.get(key);
    const stamped = `\n\n---\n*(from sub-agent ${authorId} at ${new Date().toISOString()})*\n\n${value.trim()}`;
    if (existing) {
      this.entries.set(key, {
        ...existing,
        value: existing.value + stamped,
        source: 'sub-agent',
        authorId,
        updatedAt: Date.now(),
      });
    } else {
      this.entries.set(key, {
        key,
        value: value.trim(),
        source: 'sub-agent',
        authorId,
        updatedAt: Date.now(),
      });
    }
  }
}
