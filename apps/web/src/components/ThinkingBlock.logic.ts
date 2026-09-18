export type ThinkingSegment =
  | { readonly id: string; readonly kind: "markdown"; readonly text: string }
  | { readonly id: string; readonly kind: "tool"; readonly text: string };

const TOOL_LINE_PATTERN = /^\s*(?:→|✗)\s/;

/**
 * Splits a thinking stream into prose and harness tool lines. The orchestrator
 * thinking body interleaves model text with `→ tool: …` / `✗ …` display lines;
 * the tools render as compact rows instead of dissolving into the paragraph.
 */
export function splitThinkingText(text: string): ReadonlyArray<ThinkingSegment> {
  const segments: ThinkingSegment[] = [];
  let buffer: string[] = [];
  const flush = () => {
    const value = buffer.join("\n").trim();
    buffer = [];
    if (value.length > 0) {
      segments.push({ id: `markdown-${segments.length}`, kind: "markdown", text: value });
    }
  };
  for (const line of text.split("\n")) {
    if (TOOL_LINE_PATTERN.test(line)) {
      flush();
      segments.push({ id: `tool-${segments.length}`, kind: "tool", text: line.trim() });
      continue;
    }
    buffer.push(line);
  }
  flush();
  return segments;
}
