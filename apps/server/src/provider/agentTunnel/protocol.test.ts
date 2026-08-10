import * as assert from "node:assert/strict";
import { it } from "@effect/vitest";

import { parseAgentTunnelToolCall } from "./protocol.ts";

it("parses a single-line AgentTunnel action", () => {
  assert.deepEqual(
    parseAgentTunnelToolCall(
      '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor"}}',
    ),
    { name: "dispatch_task", arguments: { role: "executor" } },
  );
});

it("ignores malformed or non-protocol output", () => {
  assert.equal(parseAgentTunnelToolCall("plain assistant output"), null);
  assert.equal(parseAgentTunnelToolCall('__AGENTTUNNEL_TOOL__ {"name":"broken"}'), null);
  assert.equal(parseAgentTunnelToolCall("__AGENTTUNNEL_TOOL__ {not json}"), null);
});
