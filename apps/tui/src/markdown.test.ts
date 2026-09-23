import { describe, expect, it } from "vite-plus/test";

import { renderMarkdown } from "./markdown.ts";
import { visibleWidth } from "./theme.ts";

const strip = (line: string): string => line.replace(/\u001b\[[0-9;]*m/gu, "");

const render = (text: string, cols = 40): string[] => renderMarkdown(text, cols).map(strip);

describe("renderMarkdown", () => {
  it("keeps the line breaks the message was written with", () => {
    expect(render("first line\nsecond line")).toEqual(["first line", "second line"]);
  });

  it("marks headings apart from the body", () => {
    // A heading opens a block: blank line, heading, blank line, body.
    expect(render("# Title\nbody text")).toEqual(["", "Title", "body text"]);
  });

  it("turns list markers into bullets and numbers, indented", () => {
    expect(render("- one\n- two")).toEqual(["  • one", "  • two"]);
    expect(render("1. first\n2. second")).toEqual(["  1. first", "  2. second"]);
  });

  it("gives fenced code its own band", () => {
    const lines = render("```\nconst x = 1;\n```");
    expect(lines[1]).toBe("  const x = 1;");
    expect(lines[0]).toContain("─");
  });

  it("labels a thinking block and dims its body", () => {
    const lines = render(
      "answer\n<details><summary>🧠 Orchestrator thinking</summary>\nreasoning here\n</details>\ntail",
    );
    expect(lines).toContain("  🧠 Orchestrator thinking");
    expect(lines).toContain("    reasoning here");
    // the answer and the tail are outside the block
    expect(lines[0]).toBe("answer");
    expect(lines.at(-1)).toBe("tail");
  });

  it("collapses a long thinking block with a pointer to the log", () => {
    const body = Array.from({ length: 40 }, (_, index) => `thought ${index}`).join("\n");
    const lines = renderMarkdown(`<details><summary>thinking</summary>\n${body}\n</details>`, 40, {
      collapseThinking: 3,
    }).map(strip);
    expect(lines.some((line) => line.includes("thought 2"))).toBe(true);
    expect(lines.some((line) => line.includes("thought 20"))).toBe(false);
    expect(lines.some((line) => line.includes("open the log"))).toBe(true);
  });

  it("strips inline emphasis but keeps the words", () => {
    const lines = render("**bold** and `code` and *italic*");
    expect(lines[0]).toBe("bold and code and italic");
  });

  it("wraps to the width and never exceeds it", () => {
    for (const cols of [20, 33, 80]) {
      const lines = renderMarkdown(
        "## A heading that is far too long for the terminal it is rendered into\n\n- a bullet that also runs on and on past the edge",
        cols,
      );
      for (const line of lines) expect(visibleWidth(line)).toBeLessThanOrEqual(cols);
    }
  });

  it("drops the trailing blank lines a block leaves behind", () => {
    expect(render("text\n<details><summary>t</summary>\nx\n</details>")).toEqual([
      "text",
      "  t",
      "    x",
    ]);
  });
});
