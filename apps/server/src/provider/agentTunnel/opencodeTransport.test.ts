import * as NodeAssert from "node:assert/strict";
import { it } from "@effect/vitest";

import type { AgentTunnelProcessSpec } from "./Orchestrator.ts";
import {
  buildAgentTunnelSpawnArgs,
  isOpenCodeHarness,
  parseOpenCodeJsonLine,
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
    "do the thing",
  ]);
});

it("buildAgentTunnelSpawnArgs omits --auto when permissions are not bypassed", () => {
  NodeAssert.deepEqual(
    buildAgentTunnelSpawnArgs(spec({ dangerouslySkipPermissions: false, model: "m1" })),
    ["run", "-m", "m1", "--format", "json", "do the thing"],
  );
});

it("buildAgentTunnelSpawnArgs keeps --format json even without a model", () => {
  NodeAssert.deepEqual(buildAgentTunnelSpawnArgs(spec({ dangerouslySkipPermissions: true })), [
    "run",
    "--auto",
    "--format",
    "json",
    "do the thing",
  ]);
});

it("buildAgentTunnelSpawnArgs keeps --format json without a model or bypass", () => {
  NodeAssert.deepEqual(buildAgentTunnelSpawnArgs(spec({ dangerouslySkipPermissions: false })), [
    "run",
    "--format",
    "json",
    "do the thing",
  ]);
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
