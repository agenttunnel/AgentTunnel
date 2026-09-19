import { describe, expect, it } from "vite-plus/test";

import {
  agentTunnelHarnessProviderKind,
  agentTunnelRoleHarnessKey,
  agentTunnelRoleModelKey,
  agentTunnelRoleVariantKey,
  applyAgentTunnelRoleHarness,
  applyAgentTunnelRoleModel,
  applyAgentTunnelRoleVariant,
  readAgentTunnelRoles,
} from "./agentTunnelRoles.logic";

describe("agentTunnelRoles", () => {
  it("maps role ids to the AgentTunnelSettings keys", () => {
    expect(agentTunnelRoleModelKey("orchestrator")).toBe("orchestratorModel");
    expect(agentTunnelRoleModelKey("executor")).toBe("executorModel");
    expect(agentTunnelRoleModelKey("reviewer")).toBe("reviewerModel");
    expect(agentTunnelRoleModelKey("unknown")).toBeNull();
    expect(agentTunnelRoleHarnessKey("executor")).toBe("executorHarness");
    expect(agentTunnelRoleHarnessKey("unknown")).toBeNull();
    expect(agentTunnelRoleVariantKey("reviewer")).toBe("reviewerVariant");
    expect(agentTunnelRoleVariantKey("unknown")).toBeNull();
  });

  it("reads role models and treats empty strings as workflow default", () => {
    expect(readAgentTunnelRoles({ orchestratorModel: "a/b", executorModel: "" })).toEqual([
      {
        id: "orchestrator",
        label: "Orchestrator",
        model: "a/b",
        harness: "opencode",
        variant: null,
        auto: false,
      },
      {
        id: "executor",
        label: "Executor",
        model: null,
        harness: "opencode",
        variant: null,
        auto: false,
      },
      {
        id: "reviewer",
        label: "Reviewer",
        model: null,
        harness: "opencode",
        variant: null,
        auto: false,
      },
    ]);
    expect(readAgentTunnelRoles(undefined).every((role) => role.model === null)).toBe(true);
  });

  it("marks executor and reviewer as auto when auto mode is on", () => {
    const roles = readAgentTunnelRoles({ autoMode: true, executorModel: "p/m" });
    expect(roles.map((role) => role.auto)).toEqual([false, true, true]);
    expect(roles[1]?.model).toBe("p/m");
    expect(readAgentTunnelRoles({ autoMode: false }).map((role) => role.auto)).toEqual([
      false,
      false,
      false,
    ]);
  });

  it("reads per-role harnesses", () => {
    expect(
      readAgentTunnelRoles({ orchestratorHarness: "codex", reviewerHarness: " claude " }).map(
        (role) => role.harness,
      ),
    ).toEqual(["codex", "opencode", "claude"]);
  });

  it("sets a role model without disturbing the rest of the config", () => {
    const next = applyAgentTunnelRoleModel({ maxOrchestratorPasses: 4 }, "executor", "p/m");
    expect(next).toEqual({ maxOrchestratorPasses: 4, executorModel: "p/m" });
  });

  it("clears a role override on an empty model", () => {
    const next = applyAgentTunnelRoleModel({ executorModel: "p/m", enabled: true }, "executor", "");
    expect(next).toEqual({ enabled: true });
  });

  it("sets and clears role harnesses", () => {
    expect(applyAgentTunnelRoleHarness({ enabled: true }, "executor", "claude")).toEqual({
      enabled: true,
      executorHarness: "claude",
    });
    expect(
      applyAgentTunnelRoleHarness({ enabled: true, executorHarness: "claude" }, "executor", ""),
    ).toEqual({ enabled: true });
  });

  it("reads and applies per-role reasoning variants", () => {
    expect(readAgentTunnelRoles({ executorVariant: "high" }).map((role) => role.variant)).toEqual([
      null,
      "high",
      null,
    ]);
    expect(applyAgentTunnelRoleVariant({ enabled: true }, "reviewer", " max ")).toEqual({
      enabled: true,
      reviewerVariant: "max",
    });
    expect(applyAgentTunnelRoleVariant({ reviewerVariant: "max" }, "reviewer", "")).toEqual({});
  });

  it("maps harnesses to their provider driver kinds for enablement checks", () => {
    expect(agentTunnelHarnessProviderKind("opencode")).toBe("opencode");
    expect(agentTunnelHarnessProviderKind("claude")).toBe("claudeAgent");
    expect(agentTunnelHarnessProviderKind("/usr/local/bin/codex")).toBeNull();
  });

  it("ignores unknown roles", () => {
    const config = { executorModel: "p/m" };
    expect(applyAgentTunnelRoleModel(config, "bogus", "x/y")).toBe(config);
    expect(applyAgentTunnelRoleHarness(config, "bogus", "codex")).toBe(config);
  });
});
