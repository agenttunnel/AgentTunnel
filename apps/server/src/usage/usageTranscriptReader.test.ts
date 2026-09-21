// @effect-diagnostics nodeBuiltinImport:off
import * as NodeFS from "node:fs";
import * as NodeOS from "node:os";
import * as NodePath from "node:path";

import { describe, expect, it } from "vite-plus/test";

import { readTranscriptRecords } from "./usageTranscriptReader.ts";

function withPiSession(lines: readonly unknown[]): string {
  const dir = NodeFS.mkdtempSync(NodePath.join(NodeOS.tmpdir(), "pi-usage-"));
  const file = NodePath.join(dir, "session.jsonl");
  NodeFS.writeFileSync(file, `${lines.map((line) => JSON.stringify(line)).join("\n")}\n`);
  return file;
}

describe("readTranscriptRecords for pi", () => {
  it("carries the model from a model_change into nested tool usage", async () => {
    const file = withPiSession([
      { type: "session", version: 3, id: "sess-1", cwd: "/project" },
      {
        type: "model_change",
        id: "m1",
        parentId: null,
        timestamp: "2026-09-01T22:39:16.141Z",
        provider: "anthropic",
        modelId: "claude-fable-5",
      },
      {
        type: "message",
        id: "t1",
        parentId: "m1",
        timestamp: "2026-09-01T22:40:00.000Z",
        message: {
          role: "toolResult",
          toolName: "bash",
          usage: { input: 10, output: 2, cacheRead: 0, cacheWrite: 0, totalTokens: 12 },
        },
      },
    ]);

    const records = await readTranscriptRecords(file, "pi");
    expect(records).toHaveLength(1);
    expect(records?.[0]).toMatchObject({
      provider: "pi",
      sessionId: "sess-1",
      model: "anthropic/claude-fable-5",
      totals: { uncachedInputTokens: 10, outputTokens: 2 },
    });
  });

  it("parses assistant usage lines", async () => {
    const file = withPiSession([
      { type: "session", version: 3, id: "sess-2", cwd: "/project" },
      {
        type: "message",
        id: "a1",
        parentId: null,
        timestamp: "2026-09-01T22:40:00.000Z",
        message: {
          role: "assistant",
          provider: "opencode-go",
          model: "deepseek-v4-flash",
          usage: {
            input: 10,
            output: 5,
            cacheRead: 0,
            cacheWrite: 0,
            totalTokens: 15,
            cost: { total: 0.01 },
          },
        },
      },
    ]);

    const records = await readTranscriptRecords(file, "pi");
    expect(records).toHaveLength(1);
    expect(records?.[0]).toMatchObject({
      sessionId: "sess-2",
      model: "opencode-go/deepseek-v4-flash",
      reportedCostUsd: 0.01,
    });
  });
});
