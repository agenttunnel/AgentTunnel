import { describe, expect, it } from "@effect/vitest";
import * as Schema from "effect/Schema";

import { AgentTunnelDriver, catalogHarnesses, mergeHarnessCatalogs } from "./AgentTunnelDriver.ts";

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
