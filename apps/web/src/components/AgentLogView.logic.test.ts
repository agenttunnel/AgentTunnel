import type { OrchestrationAgentLogEntry } from "@t3tools/contracts";
import { describe, expect, it } from "vite-plus/test";

import { foldAgentLogEntries, type AgentLogBlock } from "./AgentLogView.logic";

const stripIds = (blocks: ReadonlyArray<AgentLogBlock>) =>
  blocks.map(({ id: _id, ...rest }) => rest);

const text = (value: string): OrchestrationAgentLogEntry => ({ kind: "text", text: value });
const reasoning = (value: string): OrchestrationAgentLogEntry => ({
  kind: "reasoning",
  text: value,
});
const tool = (
  callId: string,
  status: "running" | "completed",
  output?: string,
): OrchestrationAgentLogEntry => ({
  kind: "tool",
  tool: "bash",
  title: "echo hello",
  status,
  callId,
  ...(output === undefined ? {} : { output }),
});

describe("foldAgentLogEntries", () => {
  it("merges consecutive prose of the same kind", () => {
    const blocks = foldAgentLogEntries([
      text("one"),
      text("two"),
      reasoning("think"),
      reasoning("more"),
      text("three"),
    ]);
    expect(stripIds(blocks)).toEqual([
      { kind: "markdown", text: "one\n\ntwo" },
      { kind: "reasoning", text: "think\n\nmore" },
      { kind: "markdown", text: "three" },
    ]);
  });

  it("collapses repeated updates for the same tool call to the newest state", () => {
    const blocks = foldAgentLogEntries([
      tool("call-1", "running"),
      text("interleaved"),
      tool("call-1", "completed", "hello\n"),
    ]);
    expect(stripIds(blocks)).toEqual([
      tool("call-1", "completed", "hello\n"),
      { kind: "markdown", text: "interleaved" },
    ]);
  });

  it("keeps raw and error entries in order", () => {
    const blocks = foldAgentLogEntries([
      { kind: "raw", text: "stderr banner" },
      { kind: "error", text: "rate limited" },
    ]);
    expect(stripIds(blocks)).toEqual([
      { kind: "raw", text: "stderr banner" },
      { kind: "error", text: "rate limited" },
    ]);
  });
});
