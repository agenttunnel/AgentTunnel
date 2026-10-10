import { describe, expect, it } from "vite-plus/test";

import {
  agentTunnelHarnessOptions,
  agentTunnelHarnessProviderKind,
  agentTunnelRoleHarnessKey,
  agentTunnelRoleModelKey,
  agentTunnelRoleVariantKey,
  applyAgentTunnelRoleHarness,
  applyAgentTunnelRoleModel,
  applyAgentTunnelRoleVariant,
  applyAgentTunnelApprovedModelToggle,
  applyAgentTunnelApprovedModelsDisableAll,
  applyAgentTunnelApprovedModelsEnableAll,
  applyAgentTunnelAutoMode,
  isAgentTunnelModelApproved,
  readAgentTunnelApprovedModels,
  readAgentTunnelAutoMode,
  readAgentTunnelModelConfig,
  readAgentTunnelRoles,
} from "./agentTunnelRoles.ts";

describe("agentTunnelRoles", () => {
  it("maps role ids to the AgentTunnelSettings keys", () => {
    expect(agentTunnelRoleModelKey("orchestrator")).toBe("orchestratorModel");
    expect(agentTunnelRoleModelKey("executor")).toBe("executorModel");
    expect(agentTunnelRoleModelKey("tester")).toBe("testerModel");
    expect(agentTunnelRoleModelKey("planner")).toBe("plannerModel");
    expect(agentTunnelRoleModelKey("reviewer")).toBe("reviewerModel");
    expect(agentTunnelRoleModelKey("unknown")).toBeNull();
    expect(agentTunnelRoleHarnessKey("executor")).toBe("executorHarness");
    expect(agentTunnelRoleHarnessKey("tester")).toBe("testerHarness");
    expect(agentTunnelRoleHarnessKey("planner")).toBe("plannerHarness");
    expect(agentTunnelRoleHarnessKey("unknown")).toBeNull();
    expect(agentTunnelRoleVariantKey("reviewer")).toBe("reviewerVariant");
    expect(agentTunnelRoleVariantKey("planner")).toBe("plannerVariant");
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
        id: "planner",
        label: "Planner",
        model: null,
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
        id: "tester",
        label: "Tester",
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

  it("marks every dispatched role as auto when auto mode is on", () => {
    const roles = readAgentTunnelRoles({ autoMode: true, executorModel: "p/m" });
    expect(roles.map((role) => role.auto)).toEqual([false, true, true, true, true]);
    expect(roles[2]?.model).toBe("p/m");
    expect(readAgentTunnelRoles({ autoMode: false }).map((role) => role.auto)).toEqual([
      false,
      false,
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
    ).toEqual(["codex", "opencode", "opencode", "opencode", "claude"]);
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
      null,
      "high",
      null,
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

  it("snapshots the role config a thread keeps", () => {
    expect(
      readAgentTunnelModelConfig({
        autoMode: true,
        plannerModel: "p/m",
        reviewerHarness: " codex ",
        // Settings that are not thread state: they must not leak into the
        // snapshot, so a thread cannot pin the environment's pass limits.
        maxOrchestratorPasses: 7,
        dangerouslySkipPermissions: false,
        approvedModels: ["a/b"],
      }),
    ).toEqual({
      autoMode: true,
      orchestratorHarness: "opencode",
      plannerHarness: "opencode",
      executorHarness: "opencode",
      testerHarness: "opencode",
      reviewerHarness: "codex",
      orchestratorModel: "",
      plannerModel: "p/m",
      executorModel: "",
      testerModel: "",
      reviewerModel: "",
      orchestratorVariant: "",
      plannerVariant: "",
      executorVariant: "",
      testerVariant: "",
      reviewerVariant: "",
      approvedModels: ["a/b"],
    });
  });

  it("carries auto-mode approval into the thread snapshot only when set", () => {
    expect(readAgentTunnelModelConfig({}).approvedModels).toBeUndefined();
    expect(readAgentTunnelModelConfig({ approvedModels: ["a/b", "c/d"] }).approvedModels).toEqual([
      "a/b",
      "c/d",
    ]);
  });

  it("reads blank role values as unset when snapshotting", () => {
    const snapshot = readAgentTunnelModelConfig({ executorHarness: "   ", executorModel: "" });
    expect(snapshot.executorHarness).toBe("opencode");
    expect(snapshot.executorModel).toBe("");
    expect(readAgentTunnelModelConfig(undefined)).toMatchObject({
      autoMode: false,
      orchestratorHarness: "opencode",
      orchestratorModel: "",
    });
  });

  it("reads and applies auto mode", () => {
    expect(readAgentTunnelAutoMode(undefined)).toBe(false);
    expect(readAgentTunnelAutoMode({ autoMode: true })).toBe(true);
    expect(readAgentTunnelAutoMode({ autoMode: "yes" })).toBe(false);
    expect(applyAgentTunnelAutoMode({ enabled: true }, true)).toEqual({
      enabled: true,
      autoMode: true,
    });
    expect(applyAgentTunnelAutoMode({ enabled: true, autoMode: true }, false)).toEqual({
      enabled: true,
    });
  });

  it("reads approved models as absent (null) or an explicit list", () => {
    expect(readAgentTunnelApprovedModels(undefined)).toBeNull();
    expect(readAgentTunnelApprovedModels({})).toBeNull();
    expect(readAgentTunnelApprovedModels({ approvedModels: [] })).toEqual([]);
    expect(readAgentTunnelApprovedModels({ approvedModels: ["a", "b"] })).toEqual(["a", "b"]);
    // Defensive: a non-array value is treated as absent, not as an empty list.
    expect(readAgentTunnelApprovedModels({ approvedModels: "a/b" })).toBeNull();
    expect(readAgentTunnelApprovedModels({ approvedModels: { slug: "a/b" } })).toBeNull();
  });

  it("treats an absent approved list as every model being approved", () => {
    expect(isAgentTunnelModelApproved(undefined, "a/b")).toBe(true);
    expect(isAgentTunnelModelApproved({}, "a/b")).toBe(true);
    expect(isAgentTunnelModelApproved({ approvedModels: ["a/b"] }, "a/b")).toBe(true);
    expect(isAgentTunnelModelApproved({ approvedModels: ["a/b"] }, "c/d")).toBe(false);
    // Explicit empty approves none.
    expect(isAgentTunnelModelApproved({ approvedModels: [] }, "a/b")).toBe(false);
  });

  it("materializes the approved list when toggling a model off from absent", () => {
    expect(
      applyAgentTunnelApprovedModelToggle({ enabled: true }, "b", false, ["a", "b", "c"]),
    ).toEqual({ enabled: true, approvedModels: ["a", "c"] });
  });

  it("keeps the approved list absent when toggling a model on from absent", () => {
    expect(applyAgentTunnelApprovedModelToggle({ enabled: true }, "b", true, ["a", "b"])).toEqual({
      enabled: true,
    });
  });

  it("adds and removes slugs from an explicit approved list", () => {
    expect(
      applyAgentTunnelApprovedModelToggle({ approvedModels: ["a"] }, "b", true, ["a", "b"]),
    ).toEqual({ approvedModels: ["a", "b"] });
    expect(
      applyAgentTunnelApprovedModelToggle({ approvedModels: ["a", "b"] }, "a", false, ["a", "b"]),
    ).toEqual({ approvedModels: ["b"] });
    // Removing the last approved model keeps the explicit empty list.
    expect(
      applyAgentTunnelApprovedModelToggle({ approvedModels: ["a"] }, "a", false, ["a"]),
    ).toEqual({ approvedModels: [] });
  });

  it("does not duplicate a slug that is already approved", () => {
    expect(
      applyAgentTunnelApprovedModelToggle({ approvedModels: ["a", "b"] }, "a", true, ["a", "b"]),
    ).toEqual({ approvedModels: ["a", "b"] });
  });

  it("enables all models by deleting the key and disables all with []", () => {
    expect(
      applyAgentTunnelApprovedModelsEnableAll({ approvedModels: ["a"], enabled: true }),
    ).toEqual({ enabled: true });
    expect(
      applyAgentTunnelApprovedModelsDisableAll({ approvedModels: ["a"], enabled: true }),
    ).toEqual({ enabled: true, approvedModels: [] });
    // Disable all from absent writes the explicit empty list, not a delete.
    expect(applyAgentTunnelApprovedModelsDisableAll({ enabled: true })).toEqual({
      enabled: true,
      approvedModels: [],
    });
    expect(applyAgentTunnelApprovedModelsEnableAll({ approvedModels: [] })).toEqual({});
  });

  it("ignores unknown roles", () => {
    const config = { executorModel: "p/m" };
    expect(applyAgentTunnelRoleModel(config, "bogus", "x/y")).toBe(config);
    expect(applyAgentTunnelRoleHarness(config, "bogus", "codex")).toBe(config);
  });
});

describe("agentTunnelHarnessOptions", () => {
  it("lists only harnesses whose provider kind is installed", () => {
    expect(agentTunnelHarnessOptions({ providerKinds: ["codex"] })).toEqual([
      { value: "codex", label: "Codex" },
    ]);
    expect(
      agentTunnelHarnessOptions({ providerKinds: ["claudeAgent", "grok"] }).map(
        (option) => option.value,
      ),
    ).toEqual(["claude", "grok"]);
  });

  it("keeps a selected harness visible even when its provider is not installed", () => {
    expect(
      agentTunnelHarnessOptions({
        providerKinds: ["codex"],
        selectedHarnesses: ["opencode"],
      }).map((option) => option.value),
    ).toEqual(["codex", "opencode"]);
  });

  it("labels an unknown selected harness as custom", () => {
    expect(
      agentTunnelHarnessOptions({
        providerKinds: [],
        selectedHarnesses: ["/opt/bin/my-agent"],
      }),
    ).toEqual([{ value: "/opt/bin/my-agent", label: "/opt/bin/my-agent (custom)" }]);
  });

  it("returns nothing when nothing is installed and nothing is selected", () => {
    expect(agentTunnelHarnessOptions({ providerKinds: [] })).toEqual([]);
  });
});
