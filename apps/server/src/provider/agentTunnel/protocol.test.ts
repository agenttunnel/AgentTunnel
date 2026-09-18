import * as NodeAssert from "node:assert/strict";
import { it } from "@effect/vitest";

import { parseAgentTunnelToolCall } from "./protocol.ts";

it("parses a single-line AgentTunnel action", () => {
  NodeAssert.deepEqual(
    parseAgentTunnelToolCall(
      '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor"}}',
    ),
    { name: "dispatch_task", arguments: { role: "executor" } },
  );
});

it("ignores malformed or non-protocol output", () => {
  NodeAssert.equal(parseAgentTunnelToolCall("plain assistant output"), null);
  NodeAssert.equal(parseAgentTunnelToolCall('__AGENTTUNNEL_TOOL__ {"name":"broken"}'), null);
  NodeAssert.equal(parseAgentTunnelToolCall("__AGENTTUNNEL_TOOL__ {not json}"), null);
});
