import { describe, expect, it } from "vite-plus/test";

import { parseAgentWorkflowMessage } from "./AgentWorkflowMessage.logic";

const stripIds = (blocks: ReturnType<typeof parseAgentWorkflowMessage>) =>
  blocks.map(({ id: _id, ...rest }) => rest);

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
