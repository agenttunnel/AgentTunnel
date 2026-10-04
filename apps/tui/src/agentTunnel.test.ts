import { describe, expect, it } from "vite-plus/test";

import {
  agentTunnelHarnessItems,
  agentTunnelModelItems,
  agentTunnelNumberItems,
  agentTunnelRows,
  agentTunnelVariantItems,
  parseAgentTunnelItem,
} from "./agentTunnel.ts";
import { AGENT_TUNNEL_ROLES } from "@t3tools/client-runtime/state/agentTunnelRoles";

import type { TuiAgentTunnelCatalog } from "./format.ts";

const catalog: TuiAgentTunnelCatalog = {
  instanceId: "agenttunnel",
  models: [
    { slug: "gpt-5", name: "GPT-5", harnesses: ["codex", "opencode"] },
    { slug: "sonnet-5", name: "Sonnet 5", harnesses: ["claude"] },
    { slug: "local-1", name: "Local 1", harnesses: null },
  ],
};

const labels = (rows: ReturnType<typeof agentTunnelRows>): ReadonlyArray<string> =>
  rows.map((row) => row.label);

describe("agentTunnelRows", () => {
  it("lists the workflow switches, then each role's model, harness, and reasoning", () => {
    const rows = agentTunnelRows(null);
    expect(labels(rows).slice(0, 2)).toEqual(["auto mode", "skip harness permissions"]);
    expect(labels(rows).slice(2, -3)).toEqual(
      AGENT_TUNNEL_ROLES.flatMap((role) => {
        const name = role.label.toLowerCase();
        return [`${name} model`, `${name} harness`, `${name} reasoning`];
      }),
    );
    expect(labels(rows).slice(-3)).toEqual([
      "max orchestrator passes",
      "pass timeout",
      "recap interval",
    ]);
  });

  it("covers every role the workflow dispatches to", () => {
    const roles = agentTunnelRows(null)
      .map((row) => row.role)
      .filter((role): role is string => role !== null);
    expect(new Set(roles)).toEqual(new Set(AGENT_TUNNEL_ROLES.map((role) => role.id)));
    for (const role of AGENT_TUNNEL_ROLES) {
      expect(roles.filter((id) => id === role.id)).toHaveLength(3);
    }
  });

  it("shows the stored values, with the defaults the workflow falls back to", () => {
    const rows = agentTunnelRows({
      autoMode: true,
      dangerouslySkipPermissions: false,
      executorModel: "gpt-5",
      reviewerHarness: "claude",
      reviewerVariant: "high",
      maxOrchestratorPasses: 8,
    });
    const byKey = new Map(rows.map((row) => [row.key, row]));
    expect(byKey.get("plannerModel")?.value).toBe("use the orchestrator model");
    expect(byKey.get("testerHarness")?.value).toBe("opencode");
    expect(byKey.get("autoMode")?.value).toBe("on");
    expect(byKey.get("dangerouslySkipPermissions")?.value).toBe("off");
    expect(byKey.get("orchestratorModel")?.value).toBe("use the selected model");
    expect(byKey.get("executorModel")?.value).toBe("gpt-5");
    expect(byKey.get("executorModel")?.role).toBe("executor");
    expect(byKey.get("reviewerModel")?.value).toBe("use the orchestrator model");
    expect(byKey.get("reviewerHarness")?.value).toBe("claude");
    expect(byKey.get("reviewerVariant")?.value).toBe("high");
    expect(byKey.get("maxOrchestratorPasses")?.value).toBe("8");
    expect(byKey.get("orchestratorPassTimeoutMinutes")?.value).toBe("15m");
    expect(byKey.get("orchestratorRecapIntervalSeconds")?.value).toBe("30s");
  });

  it("marks toggles as toggles and role rows as pickers", () => {
    const rows = agentTunnelRows(null);
    const kinds = new Map(rows.map((row) => [row.key, row.kind]));
    expect(kinds.get("autoMode")).toBe("toggle");
    expect(kinds.get("orchestratorModel")).toBe("model");
    expect(kinds.get("orchestratorHarness")).toBe("harness");
    expect(kinds.get("orchestratorVariant")).toBe("variant");
    expect(kinds.get("orchestratorRecapIntervalSeconds")).toBe("number");
  });
});

describe("parseAgentTunnelItem", () => {
  it("splits the field from the value", () => {
    expect(parseAgentTunnelItem("executorModel::gpt-5")).toEqual({
      field: "executorModel",
      value: "gpt-5",
    });
  });

  it("keeps a numeric value numeric, so the patch matches the schema", () => {
    expect(parseAgentTunnelItem("maxOrchestratorPasses::8")).toEqual({
      field: "maxOrchestratorPasses",
      value: 8,
    });
  });

  it("reads an empty value as clearing the override", () => {
    expect(parseAgentTunnelItem("reviewerVariant::")).toEqual({
      field: "reviewerVariant",
      value: "",
    });
  });

  it("rejects ids that carry no field", () => {
    expect(parseAgentTunnelItem("::8")).toBeNull();
    expect(parseAgentTunnelItem("gpt-5")).toBeNull();
  });
});

describe("agentTunnelModelItems", () => {
  it("offers the fallback first, then the models the role's harness reported", () => {
    const items = agentTunnelModelItems(null, catalog, "executor");
    expect(items[0]?.label).toBe("use the orchestrator model");
    expect(items.map((item) => item.label)).toEqual([
      "use the orchestrator model",
      "GPT-5",
      "Local 1",
    ]);
  });

  it("follows the harness the role is configured with", () => {
    const items = agentTunnelModelItems({ reviewerHarness: "claude" }, catalog, "reviewer");
    expect(items.map((item) => item.label)).toEqual([
      "use the orchestrator model",
      "Sonnet 5",
      "Local 1",
    ]);
  });

  it("labels the orchestrator's fallback as the session's model", () => {
    expect(agentTunnelModelItems(null, catalog, "orchestrator")[0]?.label).toBe(
      "use the selected model",
    );
  });

  it("offers only the fallback when the catalog is empty", () => {
    const items = agentTunnelModelItems(null, { instanceId: null, models: [] }, "executor");
    expect(items).toHaveLength(1);
    expect(items[0]?.id).toBe("executorModel::");
  });
});

describe("option lists", () => {
  it("offers the harness CLIs a role can run on", () => {
    const values = agentTunnelHarnessItems("executor").map(
      (item) => parseAgentTunnelItem(item.id)?.value,
    );
    expect(values).toContain("codex");
    expect(values).toContain("opencode");
    expect(values).toHaveLength(new Set(values).size);
  });

  it("offers reasoning effort including the harness default", () => {
    const values = agentTunnelVariantItems("reviewer").map(
      (item) => parseAgentTunnelItem(item.id)?.value,
    );
    expect(values[0]).toBe("");
    expect(values).toContain("high");
  });

  it("offers values inside the ranges the server accepts", () => {
    const passes = agentTunnelNumberItems("maxOrchestratorPasses").map(
      (item) => parseAgentTunnelItem(item.id)?.value,
    );
    expect(passes).toContain(4);
    expect(passes.every((value) => typeof value === "number" && value >= 1 && value <= 12)).toBe(
      true,
    );
    expect(agentTunnelNumberItems("nothing").length).toBe(0);
  });
});
