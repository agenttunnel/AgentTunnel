import { describe, expect, it } from "@effect/vitest";
import * as Schema from "effect/Schema";

import {
  AgentTunnelDriver,
  catalogHarnesses,
  filterApprovedModels,
  mergeHarnessCatalogs,
} from "./AgentTunnelDriver.ts";

const decodeConfig = Schema.decodeSync(AgentTunnelDriver.configSchema);

function model(slug: string, name = slug) {
  return { slug, name, isCustom: false, capabilities: null };
}

describe("AgentTunnelDriver", () => {
  it("advertises the agenttunnel driver kind and display name", () => {
    expect(AgentTunnelDriver.driverKind).toBe("agenttunnel");
    expect(AgentTunnelDriver.metadata.displayName).toBe("AgentTunnel");
  });

  it("defaults every role to the opencode harness", () => {
    expect(AgentTunnelDriver.defaultConfig()).toMatchObject({
      orchestratorHarness: "opencode",
      executorHarness: "opencode",
      reviewerHarness: "opencode",
      customModels: [],
    });
  });

  it("probes each configured harness plus the enumerable presets, coordinator first", () => {
    expect(
      catalogHarnesses(
        decodeConfig({
          orchestratorHarness: "opencode",
          executorHarness: "pi",
          reviewerHarness: "/opt/homebrew/bin/pi",
        }),
      ),
    ).toEqual(["opencode", "pi", "/opt/homebrew/bin/pi"]);
  });

  it("skips harnesses without a listing command unless they coordinate the workflow", () => {
    expect(
      catalogHarnesses(decodeConfig({ orchestratorHarness: "pi", executorHarness: "claude" })),
    ).toEqual(["pi", "opencode"]);
    expect(
      catalogHarnesses(
        decodeConfig({ orchestratorHarness: "claude", executorHarness: "opencode" }),
      ),
    ).toEqual(["claude", "opencode", "pi"]);
  });
});

describe("mergeHarnessCatalogs", () => {
  it("keeps one row per slug and tags every harness that reported it", () => {
    expect(
      mergeHarnessCatalogs(
        [
          { harness: "opencode", models: [model("opencode-go/glm-5", "GLM 5")] },
          {
            harness: "pi",
            models: [model("opencode-go/glm-5", "GLM 5 (pi)"), model("google/gemini-2.5-flash")],
          },
        ],
        [],
      ),
    ).toEqual([
      {
        slug: "opencode-go/glm-5",
        name: "GLM 5",
        isCustom: false,
        capabilities: null,
        harnesses: ["opencode", "pi"],
      },
      {
        slug: "google/gemini-2.5-flash",
        name: "google/gemini-2.5-flash",
        isCustom: false,
        capabilities: null,
        harnesses: ["pi"],
      },
    ]);
  });

  it("carries custom models once, untagged so every harness keeps them", () => {
    expect(
      mergeHarnessCatalogs(
        [{ harness: "pi", models: [model("google/gemini-2.5-flash")] }],
        [" custom/one ", "google/gemini-2.5-flash", ""],
      ),
    ).toEqual([
      {
        slug: "google/gemini-2.5-flash",
        name: "google/gemini-2.5-flash",
        isCustom: false,
        capabilities: null,
        harnesses: ["pi"],
      },
      {
        slug: "custom/one",
        name: "custom/one",
        isCustom: true,
        capabilities: null,
      },
    ]);
  });

  it("falls back to custom models when no harness answered", () => {
    expect(mergeHarnessCatalogs([], ["custom/one"])).toEqual([
      { slug: "custom/one", name: "custom/one", isCustom: true, capabilities: null },
    ]);
  });
});

describe("filterApprovedModels", () => {
  const models = ["opencode-go/glm-5", "google/gemini-2.5-flash", "openai/gpt-5.4"];

  it("treats an absent list as every model approved", () => {
    expect(filterApprovedModels(models, undefined)).toEqual(models);
  });

  it("keeps only approved slugs, preserving the catalog order", () => {
    expect(filterApprovedModels(models, ["openai/gpt-5.4", "opencode-go/glm-5"])).toEqual([
      "opencode-go/glm-5",
      "openai/gpt-5.4",
    ]);
  });

  it("treats a present empty list as no models approved", () => {
    expect(filterApprovedModels(models, [])).toEqual([]);
  });

  it("ignores approved slugs the catalog does not offer", () => {
    expect(filterApprovedModels(models, ["not/a/model", "google/gemini-2.5-flash"])).toEqual([
      "google/gemini-2.5-flash",
    ]);
  });

  it("does not duplicate output when the approved list repeats a slug", () => {
    expect(filterApprovedModels(models, ["openai/gpt-5.4", "openai/gpt-5.4"])).toEqual([
      "openai/gpt-5.4",
    ]);
  });
});

describe("AgentTunnelSettings approvedModels", () => {
  it("leaves the key absent when the setting was never written", () => {
    expect(decodeConfig({}).approvedModels).toBeUndefined();
  });

  it("preserves an empty list instead of collapsing it to absent", () => {
    expect(decodeConfig({ approvedModels: [] }).approvedModels).toEqual([]);
  });

  it("preserves an explicit allowlist", () => {
    expect(decodeConfig({ approvedModels: ["opencode-go/glm-5"] }).approvedModels).toEqual([
      "opencode-go/glm-5",
    ]);
  });
});
