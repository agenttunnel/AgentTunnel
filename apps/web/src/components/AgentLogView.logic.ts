import type { OrchestrationAgentLogEntry } from "@t3tools/contracts";
export type AgentLogBlock =
  | { readonly id: string; readonly kind: "markdown"; readonly text: string }
  | { readonly id: string; readonly kind: "reasoning"; readonly text: string }
  | (Extract<OrchestrationAgentLogEntry, { kind: "tool" }> & { readonly id: string })
  | { readonly id: string; readonly kind: "raw"; readonly text: string }
  | { readonly id: string; readonly kind: "error"; readonly text: string };

/**
 * Presentation fold for a persisted subagent log: consecutive prose becomes
 * one markdown block, and repeated updates for the same tool call collapse to
 * its newest state.
 */
export function foldAgentLogEntries(
  entries: ReadonlyArray<OrchestrationAgentLogEntry>,
): ReadonlyArray<AgentLogBlock> {
  const blocks: AgentLogBlock[] = [];
  const toolIndexByCallId = new Map<string, number>();
  for (const [entryIndex, entry] of entries.entries()) {
    const id = `${entry.kind}-${entryIndex}`;
    const previous = blocks.at(-1);
    if (entry.kind === "text") {
      if (previous?.kind === "markdown") {
        blocks[blocks.length - 1] = {
          id: previous.id,
          kind: "markdown",
          text: `${previous.text}\n\n${entry.text}`,
        };
        continue;
      }
      blocks.push({ id, kind: "markdown", text: entry.text });
      continue;
    }
    if (entry.kind === "reasoning") {
      if (previous?.kind === "reasoning") {
        blocks[blocks.length - 1] = {
          id: previous.id,
          kind: "reasoning",
          text: `${previous.text}\n\n${entry.text}`,
        };
        continue;
      }
      blocks.push({ id, kind: "reasoning", text: entry.text });
      continue;
    }
    if (entry.kind === "tool") {
      const callId = entry.callId;
      const existingIndex = callId === undefined ? undefined : toolIndexByCallId.get(callId);
      if (existingIndex === undefined) {
        if (callId !== undefined) {
          toolIndexByCallId.set(callId, blocks.length);
        }
        blocks.push({ ...entry, id });
      } else {
        blocks[existingIndex] = { ...entry, id: blocks[existingIndex]!.id };
      }
      continue;
    }
    blocks.push({ id, kind: entry.kind, text: entry.text });
  }
  return blocks;
}
