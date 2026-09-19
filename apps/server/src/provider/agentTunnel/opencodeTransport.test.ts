import * as NodeAssert from "node:assert/strict";
import { it } from "@effect/vitest";

import type { AgentTunnelProcessSpec } from "./Orchestrator.ts";
import {
  buildAgentTunnelSpawnArgs,
  isOpenCodeHarness,
  parseOpenCodeJsonLine,
  parseOpenCodeLogLine,
  parseOpenCodeStepUsage,
} from "./opencodeTransport.ts";

function spec(overrides: Partial<AgentTunnelProcessSpec> = {}): AgentTunnelProcessSpec {
  return {
    id: "orchestrator_1",
    threadId: "thread-1",
    harness: "opencode",
    cwd: "/tmp",
    prompt: "do the thing",
    dangerouslySkipPermissions: true,
    ...overrides,
  };
}

it("isOpenCodeHarness matches by basename", () => {
  NodeAssert.equal(isOpenCodeHarness("opencode"), true);
  NodeAssert.equal(isOpenCodeHarness("/usr/local/bin/opencode"), true);
  NodeAssert.equal(isOpenCodeHarness("claude"), false);
  NodeAssert.equal(isOpenCodeHarness("/opt/cursor/cursor-agent"), false);
});

it("buildAgentTunnelSpawnArgs uses opencode --auto and --format json for opencode harness", () => {
  NodeAssert.deepEqual(buildAgentTunnelSpawnArgs(spec({ model: "deepseek/deepseek-v4-flash" })), [
    "run",
    "-m",
    "deepseek/deepseek-v4-flash",
    "--auto",
    "--format",
    "json",
    "--thinking",
    "do the thing",
  ]);
});

it("buildAgentTunnelSpawnArgs omits --auto when permissions are not bypassed", () => {
  NodeAssert.deepEqual(
    buildAgentTunnelSpawnArgs(spec({ dangerouslySkipPermissions: false, model: "m1" })),
    ["run", "-m", "m1", "--format", "json", "--thinking", "do the thing"],
  );
});

it("buildAgentTunnelSpawnArgs keeps --format json even without a model", () => {
  NodeAssert.deepEqual(buildAgentTunnelSpawnArgs(spec({ dangerouslySkipPermissions: true })), [
    "run",
    "--auto",
    "--format",
    "json",
    "--thinking",
    "do the thing",
  ]);
});

it("buildAgentTunnelSpawnArgs keeps --format json without a model or bypass", () => {
  NodeAssert.deepEqual(buildAgentTunnelSpawnArgs(spec({ dangerouslySkipPermissions: false })), [
    "run",
    "--format",
    "json",
    "--thinking",
    "do the thing",
  ]);
});

it("buildAgentTunnelSpawnArgs passes the reasoning variant to opencode", () => {
  NodeAssert.deepEqual(
    buildAgentTunnelSpawnArgs(spec({ dangerouslySkipPermissions: false, variant: "high" })),
    ["run", "--format", "json", "--thinking", "--variant", "high", "do the thing"],
  );
});

it("buildAgentTunnelSpawnArgs uses --dangerously-skip-permissions and no json for claude harness", () => {
  NodeAssert.deepEqual(
    buildAgentTunnelSpawnArgs(spec({ harness: "claude", model: "claude-sonnet-4-5" })),
    ["run", "-m", "claude-sonnet-4-5", "--dangerously-skip-permissions", "do the thing"],
  );
});

it("buildAgentTunnelSpawnArgs preserves --dangerously-skip-permissions for unknown harnesses", () => {
  NodeAssert.deepEqual(buildAgentTunnelSpawnArgs(spec({ harness: "codex", model: "gpt-5" })), [
    "run",
    "-m",
    "gpt-5",
    "--dangerously-skip-permissions",
    "do the thing",
  ]);
});

it("parseOpenCodeJsonLine passes non-JSON lines through (stderr banners)", () => {
  NodeAssert.deepEqual(parseOpenCodeJsonLine("> build \u00b7 glm-5.2"), ["> build \u00b7 glm-5.2"]);
  NodeAssert.deepEqual(parseOpenCodeJsonLine("not json at all"), ["not json at all"]);
});

it("parseOpenCodeJsonLine splits text event parts into lines and drops empties", () => {
  const line = JSON.stringify({
    type: "text",
    part: {
      type: "text",
      text: 'Planning now.\n__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{}}\n\n',
    },
  });
  NodeAssert.deepEqual(parseOpenCodeJsonLine(line), [
    "Planning now.",
    '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{}}',
  ]);
});

it("parseOpenCodeJsonLine emits a readable progress line for tool_use events", () => {
  const line = JSON.stringify({
    type: "tool_use",
    part: {
      type: "tool",
      tool: "bash",
      state: {
        status: "completed",
        title: "echo hello-from-bash",
        input: { command: "echo hello-from-bash" },
      },
    },
  });
  NodeAssert.deepEqual(parseOpenCodeJsonLine(line), ["\u2192 bash: echo hello-from-bash"]);
});

it("parseOpenCodeJsonLine falls back to input summary when no title is present", () => {
  const line = JSON.stringify({
    type: "tool_use",
    part: { tool: "edit", state: { status: "completed", input: { path: "/repo/src/index.ts" } } },
  });
  NodeAssert.deepEqual(parseOpenCodeJsonLine(line), ["\u2192 edit: /repo/src/index.ts"]);
});

it("parseOpenCodeJsonLine drops step_start and step_finish events", () => {
  NodeAssert.deepEqual(
    parseOpenCodeJsonLine(JSON.stringify({ type: "step_start", part: { type: "step-start" } })),
    [],
  );
  NodeAssert.deepEqual(
    parseOpenCodeJsonLine(
      JSON.stringify({ type: "step_finish", part: { type: "step-finish", reason: "stop" } }),
    ),
    [],
  );
});

it("parseOpenCodeJsonLine returns an array of valid JSON arrays/booleans as raw lines", () => {
  NodeAssert.deepEqual(parseOpenCodeJsonLine("[1,2,3]"), ["[1,2,3]"]);
  NodeAssert.deepEqual(parseOpenCodeJsonLine("true"), ["true"]);
});

it("parseOpenCodeJsonLine surfaces harness error events", () => {
  const line = JSON.stringify({
    type: "error",
    error: { name: "ProviderError", data: { message: "rate limited" } },
  });
  NodeAssert.deepEqual(parseOpenCodeJsonLine(line), ["\u2717 rate limited"]);
});

it("parseOpenCodeJsonLine includes reasoning only when requested", () => {
  const line = JSON.stringify({
    type: "reasoning",
    part: { type: "reasoning", text: "Weighing options." },
  });
  NodeAssert.deepEqual(parseOpenCodeJsonLine(line), []);
  NodeAssert.deepEqual(parseOpenCodeJsonLine(line, { includeReasoning: true }), [
    "Weighing options.",
  ]);
});

it("parseOpenCodeLogLine keeps reasoning parts with their raw text", () => {
  const line = JSON.stringify({
    type: "reasoning",
    part: { type: "reasoning", text: "Weighing options." },
  });
  NodeAssert.deepEqual(parseOpenCodeLogLine(line), [
    { kind: "reasoning", text: "Weighing options." },
  ]);
});

it("parseOpenCodeLogLine captures tool calls with status, input, and output", () => {
  const line = JSON.stringify({
    type: "tool_use",
    part: {
      type: "tool",
      callID: "call-1",
      tool: "bash",
      state: {
        status: "completed",
        title: "echo hello",
        input: { command: "echo hello" },
        output: "hello\n",
      },
    },
  });
  NodeAssert.deepEqual(parseOpenCodeLogLine(line), [
    {
      kind: "tool",
      tool: "bash",
      title: "echo hello",
      status: "completed",
      callId: "call-1",
      input: '{\n  "command": "echo hello"\n}',
      output: "hello\n",
    },
  ]);
});

it("parseOpenCodeLogLine captures tool errors and text parts", () => {
  const failed = JSON.stringify({
    type: "tool_use",
    part: {
      type: "tool",
      tool: "read",
      state: { status: "error", error: "file not found", input: { path: "/nope" } },
    },
  });
  NodeAssert.deepEqual(parseOpenCodeLogLine(failed), [
    {
      kind: "tool",
      tool: "read",
      title: "/nope",
      status: "error",
      input: '{\n  "path": "/nope"\n}',
      error: "file not found",
    },
  ]);
  NodeAssert.deepEqual(
    parseOpenCodeLogLine(JSON.stringify({ type: "text", part: { text: "done" } })),
    [{ kind: "text", text: "done" }],
  );
});

it("parseOpenCodeLogLine passes non-JSON through and drops step events", () => {
  NodeAssert.deepEqual(parseOpenCodeLogLine("stderr banner"), [
    { kind: "raw", text: "stderr banner" },
  ]);
  NodeAssert.deepEqual(parseOpenCodeLogLine(JSON.stringify({ type: "step_finish", part: {} })), []);
});

it("parseOpenCodeStepUsage reads step token frames, counting cache as cached input", () => {
  const line = JSON.stringify({
    type: "step_finish",
    part: {
      reason: "tool-calls",
      tokens: {
        total: 24_622,
        input: 10_871,
        output: 213,
        reasoning: 482,
        cache: { write: 0, read: 13_056 },
      },
      cost: 0.002,
    },
  });
  NodeAssert.deepEqual(parseOpenCodeStepUsage(line), {
    totalTokens: 24_622,
    inputTokens: 10_871,
    cachedInputTokens: 13_056,
    outputTokens: 213,
    reasoningOutputTokens: 482,
  });
});

it("parseOpenCodeStepUsage defaults missing breakdown fields to zero", () => {
  const line = JSON.stringify({ type: "step_finish", part: { tokens: { total: 120 } } });
  NodeAssert.deepEqual(parseOpenCodeStepUsage(line), {
    totalTokens: 120,
    inputTokens: 0,
    cachedInputTokens: 0,
    outputTokens: 0,
    reasoningOutputTokens: 0,
  });
});

it("parseOpenCodeStepUsage ignores non-usage lines and malformed frames", () => {
  NodeAssert.equal(parseOpenCodeStepUsage("not json"), undefined);
  NodeAssert.equal(
    parseOpenCodeStepUsage(JSON.stringify({ type: "text", part: { text: "hi" } })),
    undefined,
  );
  NodeAssert.equal(
    parseOpenCodeStepUsage(JSON.stringify({ type: "step_finish", part: { tokens: { total: 0 } } })),
    undefined,
  );
  NodeAssert.equal(
    parseOpenCodeStepUsage(JSON.stringify({ type: "step_finish", part: {} })),
    undefined,
  );
});
