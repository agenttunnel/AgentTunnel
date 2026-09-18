import { describe, expect, it } from "vite-plus/test";

import { splitThinkingText } from "./ThinkingBlock.logic";

const stripIds = (segments: ReturnType<typeof splitThinkingText>) =>
  segments.map(({ id: _id, ...rest }) => rest);

describe("splitThinkingText", () => {
  it("separates tool lines from surrounding prose", () => {
    const text = [
      "Let me verify the files.",
      "→ bash: find implementations -type f",
      "→ bash: node sum.ts",
      "All three exit 0.",
    ].join("\n");
    expect(stripIds(splitThinkingText(text))).toEqual([
      { kind: "markdown", text: "Let me verify the files." },
      { kind: "tool", text: "→ bash: find implementations -type f" },
      { kind: "tool", text: "→ bash: node sum.ts" },
      { kind: "markdown", text: "All three exit 0." },
    ]);
  });

  it("keeps consecutive prose lines together and trims edges", () => {
    expect(stripIds(splitThinkingText("\nfirst\nsecond\n"))).toEqual([
      { kind: "markdown", text: "first\nsecond" },
    ]);
  });

  it("returns nothing for whitespace-only input", () => {
    expect(splitThinkingText("\n  \n")).toEqual([]);
  });
});
