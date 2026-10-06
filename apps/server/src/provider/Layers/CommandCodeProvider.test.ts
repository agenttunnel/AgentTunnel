import { describe, expect, it } from "vite-plus/test";
import type * as EffectAcpSchema from "effect-acp/schema";

import {
  buildCommandCodeModelsFromSessionModelState,
  parseCommandCodeAuthStatusJson,
} from "./CommandCodeProvider.ts";

describe("parseCommandCodeAuthStatusJson", () => {
  it("reads the signed-in user from the status document", () => {
    expect(
      parseCommandCodeAuthStatusJson('{"authenticated":true,"userName":"Ada","version":"1.74.1"}'),
    ).toEqual({ authenticated: true, userName: "Ada" });
  });

  it("reports an unauthenticated status without a user", () => {
    expect(parseCommandCodeAuthStatusJson('{"authenticated":false}')).toEqual({
      authenticated: false,
    });
  });

  it("finds the JSON line when the CLI prints a leading warning", () => {
    expect(
      parseCommandCodeAuthStatusJson('Warning: update available\n{"authenticated":true}\n'),
    ).toEqual({ authenticated: true });
  });

  it("returns undefined for non-JSON or unrelated output", () => {
    expect(parseCommandCodeAuthStatusJson("not json")).toBeUndefined();
    expect(parseCommandCodeAuthStatusJson('{"ok":true}')).toBeUndefined();
  });
});

describe("buildCommandCodeModelsFromSessionModelState", () => {
  it("maps the ACP model state onto server provider models", () => {
    const modelState = {
      currentModelId: "deepseek/deepseek-v4-flash",
      availableModels: [
        { modelId: "deepseek/deepseek-v4-flash", name: "DeepSeek V4 Flash" },
        { modelId: "kimi-k2.5", name: "Kimi K2.5", description: "fast" },
      ],
    } satisfies EffectAcpSchema.SessionModelState;

    expect(buildCommandCodeModelsFromSessionModelState(modelState)).toEqual([
      {
        slug: "deepseek/deepseek-v4-flash",
        name: "DeepSeek V4 Flash",
        isCustom: false,
        isDefault: true,
        capabilities: { optionDescriptors: [] },
      },
      {
        slug: "kimi-k2.5",
        name: "Kimi K2.5",
        isCustom: false,
        isDefault: false,
        capabilities: { optionDescriptors: [] },
      },
    ]);
  });

  it("drops duplicate and blank model ids", () => {
    const modelState = {
      currentModelId: "a",
      availableModels: [
        { modelId: "a", name: "A" },
        { modelId: "a", name: "A duplicate" },
        { modelId: "   ", name: "Blank" },
      ],
    } satisfies EffectAcpSchema.SessionModelState;

    expect(buildCommandCodeModelsFromSessionModelState(modelState)).toHaveLength(1);
  });

  it("returns no models when the agent reports none", () => {
    expect(buildCommandCodeModelsFromSessionModelState(undefined)).toEqual([]);
    expect(
      buildCommandCodeModelsFromSessionModelState({
        currentModelId: "a",
        availableModels: [],
      }),
    ).toEqual([]);
  });
});
