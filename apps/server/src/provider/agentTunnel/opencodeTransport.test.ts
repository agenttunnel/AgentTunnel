import * as NodeAssert from "node:assert/strict";
import { it } from "@effect/vitest";

import type { AgentTunnelProcessSpec } from "./Orchestrator.ts";
import {
  agentTunnelHarnessKind,
  buildAgentTunnelSpawnArgs,
  isOpenCodeHarness,
  isPiHarness,
  harnessUsageLogEntry,
  parseClaudeLogLine,
  parseClaudeStepUsage,
  parseCodexLogLine,
  parseCodexStepUsage,
  parseHarnessJsonLines,
  parseHarnessLogLine,
  parseHarnessUsageStep,
  parseOpenCodeJsonLine,
  parseOpenCodeLogLine,
  parseOpenCodeStepUsage,
  parsePiJsonLine,
  parsePiLogLine,
  parsePiStepUsage,
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

it("buildAgentTunnelSpawnArgs streams claude headless with effort and its model", () => {
  NodeAssert.deepEqual(
    buildAgentTunnelSpawnArgs(
      spec({ harness: "claude", model: "claude-sonnet-4-5", variant: "high" }),
    ),
    [
      "--print",
      "--output-format",
      "stream-json",
      "--verbose",
      "--model",
      "claude-sonnet-4-5",
      "--effort",
      "high",
      "--dangerously-skip-permissions",
      "do the thing",
    ],
  );
});

it("buildAgentTunnelSpawnArgs maps claude's unsupported minimal effort to low", () => {
  NodeAssert.deepEqual(
    buildAgentTunnelSpawnArgs(spec({ harness: "claude", variant: "minimal" })).slice(0, 6),
    ["--print", "--output-format", "stream-json", "--verbose", "--effort", "low"],
  );
});

it("buildAgentTunnelSpawnArgs runs codex exec in json mode with bypass sandbox", () => {
  NodeAssert.deepEqual(
    buildAgentTunnelSpawnArgs(spec({ harness: "codex", model: "gpt-5", variant: "high" })),
    [
      "exec",
      "--json",
      "--ephemeral",
      "--skip-git-repo-check",
      "--model",
      "gpt-5",
      "--dangerously-bypass-approvals-and-sandbox",
      "-c",
      'model_reasoning_effort="high"',
      "do the thing",
    ],
  );
});

it("buildAgentTunnelSpawnArgs keeps codex inside a workspace sandbox without the bypass setting", () => {
  NodeAssert.deepEqual(
    buildAgentTunnelSpawnArgs(
      spec({ harness: "codex", dangerouslySkipPermissions: false, model: "gpt-5" }),
    ),
    [
      "exec",
      "--json",
      "--ephemeral",
      "--skip-git-repo-check",
      "--model",
      "gpt-5",
      "--sandbox",
      "workspace-write",
      "do the thing",
    ],
  );
});

it("buildAgentTunnelSpawnArgs keeps the generic run invocation for harnesses without a transport", () => {
  NodeAssert.deepEqual(
    buildAgentTunnelSpawnArgs(spec({ harness: "cursor-agent", model: "gpt-5" })),
    ["run", "-m", "gpt-5", "--dangerously-skip-permissions", "do the thing"],
  );
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

it("parseOpenCodeLogLine leaves token frames to the usage tap", () => {
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
    },
  });
  NodeAssert.deepEqual(parseOpenCodeLogLine(line), []);
  // Usage entries never surface in the orchestrator chat transcript.
  NodeAssert.deepEqual(parseOpenCodeJsonLine(line), []);
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
    costUsd: 0.002,
  });
});

it("parsePiStepUsage normalizes message_end usage, folding reasoning into output", () => {
  const line = JSON.stringify({
    type: "message_end",
    message: {
      role: "assistant",
      usage: {
        input: 51_165,
        output: 5_560,
        cacheRead: 1_000,
        cacheWrite: 500,
        reasoning: 2_924,
        totalTokens: 58_225,
        cost: { input: 0.1, output: 0.2, cacheRead: 0, cacheWrite: 0, total: 0.3 },
      },
    },
  });
  NodeAssert.deepEqual(parsePiStepUsage(line), {
    totalTokens: 58_225,
    inputTokens: 51_165,
    cachedInputTokens: 1_500,
    // pi counts reasoning inside output; the plain remainder is what the
    // shared step shape keeps beside it.
    outputTokens: 5_560 - 2_924,
    reasoningOutputTokens: 2_924,
    costUsd: 0.3,
  });
});

it("parsePiStepUsage drops unpriced cost and non-usage frames", () => {
  const unpriced = JSON.stringify({
    type: "message_end",
    message: {
      role: "assistant",
      usage: { input: 10, output: 5, totalTokens: 15, cost: { total: 0 } },
    },
  });
  NodeAssert.deepEqual(parsePiStepUsage(unpriced), {
    totalTokens: 15,
    inputTokens: 10,
    cachedInputTokens: 0,
    outputTokens: 5,
    reasoningOutputTokens: 0,
  });
  NodeAssert.equal(parsePiStepUsage(JSON.stringify({ type: "message_update" })), undefined);
  NodeAssert.equal(
    parsePiStepUsage(
      JSON.stringify({ type: "message_end", message: { role: "user", usage: { totalTokens: 5 } } }),
    ),
    undefined,
  );
  NodeAssert.equal(parsePiStepUsage("not json"), undefined);
});

it("parsePiLogLine keeps the prose while usage rides the usage tap", () => {
  const line = JSON.stringify({
    type: "message_end",
    message: {
      role: "assistant",
      content: [{ type: "text", text: "done" }],
      usage: { input: 10, output: 5, reasoning: 2, totalTokens: 15, cost: { total: 0.01 } },
    },
  });
  NodeAssert.deepEqual(parsePiLogLine(line), [{ kind: "text", text: "done" }]);
  NodeAssert.deepEqual(harnessUsageLogEntry(parsePiStepUsage(line)!), {
    kind: "usage",
    totalTokens: 15,
    inputTokens: 10,
    cachedInputTokens: 0,
    outputTokens: 3,
    reasoningOutputTokens: 2,
    costUsd: 0.01,
  });
});

it("parseClaudeStepUsage reads assistant-message usage with cache and thinking split out", () => {
  const line = JSON.stringify({
    type: "assistant",
    message: {
      role: "assistant",
      id: "msg_011CfH2Y9w",
      content: [{ type: "text", text: "ok" }],
      usage: {
        input_tokens: 2,
        cache_creation_input_tokens: 10_954,
        cache_read_input_tokens: 19_001,
        output_tokens: 4,
        output_tokens_details: { thinking_tokens: 3 },
      },
    },
  });
  NodeAssert.deepEqual(parseClaudeStepUsage(line), {
    totalTokens: 2 + 10_954 + 19_001 + 1 + 3,
    inputTokens: 2,
    cachedInputTokens: 10_954 + 19_001,
    // Claude folds thinking into output_tokens; the plain remainder sits
    // beside it like every other transport.
    outputTokens: 1,
    reasoningOutputTokens: 3,
    stepId: "msg_011CfH2Y9w",
  });
});

it("parseClaudeStepUsage turns the closing result frame into a cost-only step", () => {
  NodeAssert.deepEqual(
    parseClaudeStepUsage(JSON.stringify({ type: "result", total_cost_usd: 0.0486172 })),
    {
      totalTokens: 0,
      inputTokens: 0,
      cachedInputTokens: 0,
      outputTokens: 0,
      reasoningOutputTokens: 0,
      costUsd: 0.0486172,
    },
  );
  // Unpriced runs report 0, which is "not priced", not "free".
  NodeAssert.equal(
    parseClaudeStepUsage(JSON.stringify({ type: "result", total_cost_usd: 0 })),
    undefined,
  );
  NodeAssert.equal(
    parseClaudeStepUsage(JSON.stringify({ type: "system", subtype: "init" })),
    undefined,
  );
  NodeAssert.equal(parseClaudeStepUsage("not json"), undefined);
});

it("parseClaudeLogLine logs prose and reasoning, leaving usage to the usage tap", () => {
  const line = JSON.stringify({
    type: "assistant",
    message: {
      role: "assistant",
      id: "msg_011CfH2Y9w",
      content: [
        { type: "thinking", thinking: "Weighing options.", signature: "sig" },
        { type: "text", text: "done" },
      ],
      usage: { input_tokens: 10, output_tokens: 5 },
    },
  });
  NodeAssert.deepEqual(parseClaudeLogLine(line), [
    { kind: "reasoning", text: "Weighing options." },
    { kind: "text", text: "done" },
  ]);
  NodeAssert.deepEqual(
    parseClaudeLogLine(JSON.stringify({ type: "result", total_cost_usd: 0.02 })),
    [],
  );
  NodeAssert.deepEqual(
    parseClaudeLogLine(JSON.stringify({ type: "result", is_error: true, result: "rate limited" })),
    [{ kind: "error", text: "rate limited" }],
  );
});

it("parseCodexStepUsage subtracts the cached and reasoning subsets of a turn", () => {
  const line = JSON.stringify({
    type: "turn.completed",
    usage: {
      input_tokens: 24_763,
      cached_input_tokens: 24_448,
      output_tokens: 122,
      reasoning_output_tokens: 20,
    },
  });
  NodeAssert.deepEqual(parseCodexStepUsage(line), {
    totalTokens: 24_763 + 122,
    inputTokens: 24_763 - 24_448,
    cachedInputTokens: 24_448,
    outputTokens: 102,
    reasoningOutputTokens: 20,
  });
  NodeAssert.equal(parseCodexStepUsage(JSON.stringify({ type: "turn.started" })), undefined);
  NodeAssert.equal(
    parseCodexStepUsage(JSON.stringify({ type: "turn.completed", usage: {} })),
    undefined,
  );
});

it("parseCodexLogLine maps Codex items to text, tool rows, and usage", () => {
  NodeAssert.deepEqual(
    parseCodexLogLine(
      JSON.stringify({
        type: "item.completed",
        item: { id: "item_3", type: "agent_message", text: "Repo contains docs." },
      }),
    ),
    [{ kind: "text", text: "Repo contains docs." }],
  );
  NodeAssert.deepEqual(
    parseCodexLogLine(
      JSON.stringify({
        type: "item.started",
        item: {
          id: "item_1",
          type: "command_execution",
          command: "bash -lc ls",
          status: "in_progress",
        },
      }),
    ),
    [
      {
        kind: "tool",
        tool: "shell",
        title: "bash -lc ls",
        status: "running",
        callId: "item_1",
        input: "bash -lc ls",
      },
    ],
  );
  NodeAssert.deepEqual(
    parseCodexLogLine(
      JSON.stringify({
        type: "item.completed",
        item: {
          id: "item_1",
          type: "command_execution",
          command: "bash -lc ls",
          aggregated_output: "README.md",
          exit_code: 0,
          status: "completed",
        },
      }),
    ),
    [
      {
        kind: "tool",
        tool: "shell",
        title: "bash -lc ls",
        status: "completed",
        callId: "item_1",
        input: "bash -lc ls",
        output: "README.md",
      },
    ],
  );
  // Codex reports the turn's tokens on its own frame; the usage tap folds it
  // once, so the log itself carries only prose and tool rows.
  NodeAssert.deepEqual(
    parseCodexLogLine(
      JSON.stringify({
        type: "turn.completed",
        usage: { input_tokens: 100, cached_input_tokens: 40, output_tokens: 10 },
      }),
    ),
    [],
  );
  NodeAssert.deepEqual(
    parseCodexLogLine(JSON.stringify({ type: "turn.failed", error: { message: "boom" } })),
    [{ kind: "error", text: "boom" }],
  );
});

it("parseHarnessUsageStep and parseHarnessLogLine route by harness kind", () => {
  const claudeUsage = JSON.stringify({
    type: "assistant",
    message: { role: "assistant", usage: { input_tokens: 7, output_tokens: 3 } },
  });
  NodeAssert.deepEqual(parseHarnessUsageStep(claudeUsage, "claude"), {
    totalTokens: 10,
    inputTokens: 7,
    cachedInputTokens: 0,
    outputTokens: 3,
    reasoningOutputTokens: 0,
  });
  NodeAssert.deepEqual(parseHarnessLogLine("plain text", "other"), [
    { kind: "raw", text: "plain text" },
  ]);
  NodeAssert.deepEqual(
    parseHarnessJsonLines(
      JSON.stringify({ type: "item.completed", item: { type: "agent_message", text: "hi" } }),
      "codex",
    ),
    ["hi"],
  );
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

it("parseOpenCodeStepUsage tolerates step_finish text in non-frame lines", () => {
  const text = JSON.stringify({ type: "text", part: { text: "the step_finish event is next" } });
  NodeAssert.equal(parseOpenCodeStepUsage(text), undefined);
});

it("detects harness kind by basename including pi", () => {
  NodeAssert.equal(isPiHarness("pi"), true);
  NodeAssert.equal(isPiHarness("/usr/local/bin/pi"), true);
  NodeAssert.equal(isPiHarness("PI.exe"), true);
  NodeAssert.equal(isPiHarness("opencode"), false);
  NodeAssert.equal(agentTunnelHarnessKind("opencode"), "opencode");
  NodeAssert.equal(agentTunnelHarnessKind("/opt/bin/pi"), "pi");
  NodeAssert.equal(agentTunnelHarnessKind("/usr/local/bin/claude"), "claude");
  NodeAssert.equal(agentTunnelHarnessKind("codex.exe"), "codex");
  NodeAssert.equal(agentTunnelHarnessKind("cursor-agent"), "other");
});

it("buildAgentTunnelSpawnArgs uses pi's json one-shot flags", () => {
  NodeAssert.deepEqual(
    buildAgentTunnelSpawnArgs(
      spec({ harness: "pi", model: "google/gemini-3-pro", variant: "high" }),
    ),
    [
      "--mode",
      "json",
      "-p",
      "do the thing",
      "--no-session",
      "--model",
      "google/gemini-3-pro",
      "--thinking",
      "high",
    ],
  );
});

it("buildAgentTunnelSpawnArgs omits pi flags that are unset", () => {
  NodeAssert.deepEqual(
    buildAgentTunnelSpawnArgs(spec({ harness: "pi", dangerouslySkipPermissions: true })),
    ["--mode", "json", "-p", "do the thing", "--no-session"],
  );
});

it("parsePiJsonLine splits assistant text from message_end and keeps tool markers", () => {
  const line = JSON.stringify({
    type: "message_end",
    message: {
      role: "assistant",
      content: [
        {
          type: "text",
          text: 'Planning now.\n__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{}}\n\n',
        },
      ],
    },
  });
  NodeAssert.deepEqual(parsePiJsonLine(line), [
    "Planning now.",
    '__AGENTTUNNEL_TOOL__ {"name":"done","arguments":{}}',
  ]);
});

it("parsePiJsonLine only surfaces thinking when reasoning is requested", () => {
  const line = JSON.stringify({
    type: "message_end",
    message: { role: "assistant", content: [{ type: "thinking", text: "Weighing options." }] },
  });
  NodeAssert.deepEqual(parsePiJsonLine(line), []);
  NodeAssert.deepEqual(parsePiJsonLine(line, { includeReasoning: true }), ["Weighing options."]);
});

it("parsePiJsonLine accepts a plain string message body", () => {
  const line = JSON.stringify({
    type: "message_end",
    message: { role: "assistant", content: "All done." },
  });
  NodeAssert.deepEqual(parsePiJsonLine(line), ["All done."]);
});

it("parsePiLogLine passes non-JSON through and drops unrelated frames", () => {
  NodeAssert.deepEqual(parsePiLogLine("pi banner"), [{ kind: "raw", text: "pi banner" }]);
  NodeAssert.deepEqual(parsePiLogLine(JSON.stringify({ type: "agent_start" })), []);
  NodeAssert.deepEqual(parsePiLogLine(JSON.stringify({ type: "message_end", message: {} })), []);
});

it("parsePiLogLine surfaces pi error frames", () => {
  const line = JSON.stringify({ type: "error", error: { message: "rate limited" } });
  NodeAssert.deepEqual(parsePiLogLine(line), [{ kind: "error", text: "rate limited" }]);
  NodeAssert.deepEqual(parsePiJsonLine(line), ["\u2717 rate limited"]);
});
