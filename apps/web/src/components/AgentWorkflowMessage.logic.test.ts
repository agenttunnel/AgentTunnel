import { describe, expect, it } from "vite-plus/test";

import {
  groupAgentOutputSegments,
  parseAgentOutputText,
  parseAgentWorkflowMessage,
  summarizeAgentOutput,
  summarizeToolRun,
  type AgentOutputToolSegment,
} from "./AgentWorkflowMessage.logic";

const stripIds = <T extends { readonly id: string }>(items: ReadonlyArray<T>) =>
  items.map(({ id: _id, ...rest }) => rest);

describe("parseAgentWorkflowMessage", () => {
  it("splits prose, thinking, and output blocks with their labels", () => {
    const text = [
      "## AgentTunnel workflow\n\n**Task:** sum in three languages\n",
      "\n<details>\n<summary>🧠 Orchestrator thinking</summary>\n\nPlanning the split.\n\n</details>\n\n",
      "📝 **Context updated:** `goal` — sums\n",
      "\n<details>\n<summary>📋 Output · Write the TypeScript sum</summary>\n\n```\nexport const sum = 1;\n```\n\n</details>\n\n",
      "✅ **executor · Write the TypeScript sum** completed in 3s\n",
    ].join("");
    const blocks = stripIds(parseAgentWorkflowMessage(text));
    expect(blocks).toEqual([
      { kind: "markdown", text: "## AgentTunnel workflow\n\n**Task:** sum in three languages" },
      { kind: "thinking", text: "Planning the split.", streaming: false },
      { kind: "markdown", text: "📝 **Context updated:** `goal` — sums" },
      {
        kind: "output",
        label: "Write the TypeScript sum",
        text: "export const sum = 1;",
        streaming: false,
      },
      { kind: "markdown", text: "✅ **executor · Write the TypeScript sum** completed in 3s" },
    ]);
  });

  it("marks an unterminated details block as streaming and keeps its body", () => {
    const text = "intro\n\n<details>\n<summary>🧠 Orchestrator thinking</summary>\n\nStill going";
    const blocks = stripIds(parseAgentWorkflowMessage(text));
    expect(blocks).toEqual([
      { kind: "markdown", text: "intro" },
      { kind: "thinking", text: "Still going", streaming: true },
    ]);
  });

  it("renders a plain assistant message as a single markdown block", () => {
    const text = "Just a normal answer with **bold**.";
    const blocks = stripIds(parseAgentWorkflowMessage(text));
    expect(blocks).toEqual([{ kind: "markdown", text }]);
  });

  it("keeps the orchestrator final report visible as one ordered markdown block", () => {
    const finalReport = [
      "\n---\n",
      "\n## ✅ Workflow completed · 12.4s\n",
      "\n### What happened\n",
      "\nImplemented the final report end to end and verified it against a real workflow.\n",
      "\n### Next steps\n",
      "\n1. Review the generated diff.\n",
      "2. Run the focused tests.\n",
      "\n### Pipelines\n",
      "\n| Pipeline | Agents | Status | Time |\n",
      "| --- | --- | --- | --- |\n",
      "| `impl` | 1/1 | ✅ completed | 12.4s |\n",
    ].join("");
    const blocks = stripIds(parseAgentWorkflowMessage(finalReport));

    expect(blocks).toEqual([{ kind: "markdown", text: finalReport.trim() }]);

    const text = blocks[0]?.kind === "markdown" ? blocks[0].text : "";
    expect(text.indexOf("### What happened")).toBeLessThan(text.indexOf("### Next steps"));
    expect(text.indexOf("### Next steps")).toBeLessThan(text.indexOf("### Pipelines"));
    expect(text).toContain("| Pipeline | Agents | Status | Time |");
  });
});

describe("parseAgentOutputText", () => {
  it("keeps prose, tool calls and errors in document order", () => {
    const body = [
      "**Sum** written for all three languages.",
      "→ bash: cd implementations && node typescript/sum.ts",
      "→ read: /repo/src/sum.ts",
      "Now checking the exit codes.",
      "✗ bash failed: exit 1",
      "Retrying with the pinned runtime.",
    ].join("\n");

    expect(stripIds(parseAgentOutputText(body, undefined))).toEqual([
      { kind: "prose", text: "**Sum** written for all three languages." },
      {
        kind: "tool",
        tool: "bash",
        detail: "cd implementations && node typescript/sum.ts",
        displayDetail: "cd implementations && node typescript/sum.ts",
      },
      { kind: "tool", tool: "read", detail: "/repo/src/sum.ts", displayDetail: "/repo/src/sum.ts" },
      { kind: "prose", text: "Now checking the exit codes." },
      { kind: "error", text: "✗ bash failed: exit 1" },
      { kind: "prose", text: "Retrying with the pinned runtime." },
    ]);
  });

  it("splits the first colon of a tool line and tolerates a missing detail", () => {
    const segments = parseAgentOutputText(
      ["→ read: /a/b", "→ bash", "→ frobnicate: unknown tool", "→grep: needle"].join("\n"),
      undefined,
    );

    expect(stripIds(segments)).toEqual([
      { kind: "tool", tool: "read", detail: "/a/b", displayDetail: "/a/b" },
      { kind: "tool", tool: "bash", detail: "", displayDetail: "" },
      { kind: "tool", tool: "frobnicate", detail: "unknown tool", displayDetail: "unknown tool" },
      { kind: "tool", tool: "grep", detail: "needle", displayDetail: "needle" },
    ]);
  });

  it("shortens workspace paths against the cwd but leaves commands alone", () => {
    const segments = parseAgentOutputText(
      ["→ read: /repo/src/sum.ts", "→ bash: cd /repo && node sum.ts"].join("\n"),
      "/repo",
    );

    expect(segments).toMatchObject([
      { tool: "read", detail: "/repo/src/sum.ts", displayDetail: "repo/src/sum.ts" },
      {
        tool: "bash",
        detail: "cd /repo && node sum.ts",
        displayDetail: "cd /repo && node sum.ts",
      },
    ]);
  });

  it("joins prose runs with newlines and drops blank runs around them", () => {
    const body = "\n\nIntro line.\nsecond line.\n\n\n→ bash: ls\n\ntail\n\n";

    expect(stripIds(parseAgentOutputText(body, undefined))).toEqual([
      { kind: "prose", text: "Intro line.\nsecond line." },
      { kind: "tool", tool: "bash", detail: "ls", displayDetail: "ls" },
      { kind: "prose", text: "tail" },
    ]);
  });

  it("unwraps a fenced body and copes with CRLF and a trailing newline", () => {
    const body = "```\r\n→ bash: ls -la\r\n\r\nAll three exit 0.\r\n```\r\n";

    expect(stripIds(parseAgentOutputText(body, undefined))).toEqual([
      { kind: "tool", tool: "bash", detail: "ls -la", displayDetail: "ls -la" },
      { kind: "prose", text: "All three exit 0." },
    ]);
  });

  it("keeps malformed arrow lines as prose instead of dropping them", () => {
    const body = ["→", "→ : nameless", "plain"].join("\n");

    expect(stripIds(parseAgentOutputText(body, undefined))).toEqual([
      { kind: "prose", text: "→\n→ : nameless\nplain" },
    ]);
  });

  it("returns nothing for an empty or whitespace-only body", () => {
    expect(parseAgentOutputText("", undefined)).toEqual([]);
    expect(parseAgentOutputText("\n  \n", undefined)).toEqual([]);
  });
});

describe("groupAgentOutputSegments", () => {
  it("keeps one or two consecutive tool calls inline", () => {
    const segments = parseAgentOutputText("→ bash: one\n→ read: /a\n", undefined);

    expect(groupAgentOutputSegments(segments)).toEqual(segments);
  });

  it("collapses a run of three or more tool calls into one summarised row", () => {
    const segments = parseAgentOutputText(
      ["start", "→ bash: one", "→ bash: two", "→ read: /a", "→ read: /b", "end"].join("\n"),
      undefined,
    );
    const groups = groupAgentOutputSegments(segments);

    expect(groups.map((group) => group.kind)).toEqual(["prose", "toolRun", "prose"]);
    const run = groups[1];
    expect(run?.kind === "toolRun" ? run.summary : "").toBe("4 tool calls · 2 files read");
    expect(run?.kind === "toolRun" ? stripIds(run.tools).length : 0).toBe(4);
  });

  it("counts distinct files and says touched when writes are involved", () => {
    const tools = parseAgentOutputText(
      ["→ read: /a", "→ read: /a", "→ write: /b", "→ bash: ls"].join("\n"),
      undefined,
    ).filter((segment): segment is AgentOutputToolSegment => segment.kind === "tool");

    expect(summarizeToolRun(tools)).toBe("4 tool calls · 2 files touched");
  });
});

describe("summarizeAgentOutput", () => {
  it("counts content lines, tool calls and errors", () => {
    const body = ["A.", "B.", "", "→ bash: ls", "→ read: /a", "→ bash: two", "✗ failed"].join("\n");

    expect(summarizeAgentOutput(parseAgentOutputText(body, undefined))).toEqual({
      lines: 6,
      toolCalls: 3,
      errors: 1,
    });
    expect(summarizeAgentOutput(parseAgentOutputText("", undefined))).toEqual({
      lines: 0,
      toolCalls: 0,
      errors: 0,
    });
  });
});
