import { describe, expect, it } from "vite-plus/test";

import { AgentTunnelSharedContext, parseSharedContextDump } from "./SharedContext.ts";

describe("parseSharedContextDump", () => {
  it("round-trips what renderForPrompt writes", () => {
    const context = new AgentTunnelSharedContext();
    context.set("user_goal", "Ship the durable thread context.");
    context.append("review_findings", "Found one blocking issue.", "reviewer-1");

    const parsed = parseSharedContextDump(context.renderForPrompt());
    expect([...parsed.keys()].sort()).toEqual(["review_findings", "user_goal"]);
    expect(parsed.get("user_goal")).toBe("Ship the durable thread context.");
    expect(parsed.get("review_findings")).toContain("Found one blocking issue.");
  });

  it("keeps markdown headings inside a value from splitting the section", () => {
    const context = new AgentTunnelSharedContext();
    context.set("plan", "### Step one\n\nDo the thing.\n\n## Not a section\n\nStill the plan.");

    const parsed = parseSharedContextDump(context.renderForPrompt());
    expect(parsed.size).toBe(1);
    expect(parsed.get("plan")).toContain("Still the plan.");
  });

  it("skips sections whose key is not a notebook key", () => {
    const parsed = parseSharedContextDump(
      "### Not A Key\n\nignored\n\n### good_key\n\nkept\n\n### prior_thread_context\n\nnested\n",
    );
    // `prior_thread_context` parses like any other key; skipping it when
    // rehydrating is the orchestrator's call, not the parser's.
    expect([...parsed.keys()]).toEqual(["good_key", "prior_thread_context"]);
  });

  it("returns nothing for a dump that carries no sections", () => {
    expect(parseSharedContextDump("").size).toBe(0);
    expect(parseSharedContextDump("(no shared context yet)").size).toBe(0);
  });
});
