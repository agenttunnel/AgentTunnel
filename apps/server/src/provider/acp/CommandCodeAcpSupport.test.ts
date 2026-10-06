import { describe, expect, it } from "vite-plus/test";
import * as Effect from "effect/Effect";
import * as EffectAcpErrors from "effect-acp/errors";

import {
  applyCommandCodeAcpModelSelection,
  buildCommandCodeAcpSpawnInput,
  COMMAND_CODE_DEFAULT_MODEL,
  resolveCommandCodeAcpBaseModelId,
} from "./CommandCodeAcpSupport.ts";

describe("buildCommandCodeAcpSpawnInput", () => {
  it("builds the default Command Code ACP command", () => {
    expect(buildCommandCodeAcpSpawnInput(undefined, "/tmp/project")).toEqual({
      command: "command-code",
      args: ["acp"],
      cwd: "/tmp/project",
    });
  });

  it("honours a configured binary path and environment", () => {
    expect(
      buildCommandCodeAcpSpawnInput({ binaryPath: "/usr/local/bin/cmd" }, "/tmp/project", {
        CMD_ZDR: "1",
      }),
    ).toEqual({
      command: "/usr/local/bin/cmd",
      args: ["acp"],
      cwd: "/tmp/project",
      env: { CMD_ZDR: "1" },
    });
  });
});

describe("resolveCommandCodeAcpBaseModelId", () => {
  it("falls back to the catalog default for empty ids", () => {
    expect(resolveCommandCodeAcpBaseModelId(undefined)).toBe(COMMAND_CODE_DEFAULT_MODEL);
    expect(resolveCommandCodeAcpBaseModelId("   ")).toBe(COMMAND_CODE_DEFAULT_MODEL);
  });

  it("trims and preserves Command Code catalog slugs", () => {
    expect(resolveCommandCodeAcpBaseModelId("  deepseek/deepseek-v4-pro  ")).toBe(
      "deepseek/deepseek-v4-pro",
    );
  });
});

describe("applyCommandCodeAcpModelSelection", () => {
  const makeRecordingRuntime = (failure?: EffectAcpErrors.AcpError) => {
    const modelCalls: Array<string> = [];
    const runtime = {
      setSessionModel: (modelId: string) =>
        Effect.gen(function* () {
          modelCalls.push(modelId);
          if (failure) return yield* failure;
          return {};
        }),
    };
    return { runtime, modelCalls };
  };

  it("calls session/set_model when the requested model differs", async () => {
    const { runtime, modelCalls } = makeRecordingRuntime();
    const result = await Effect.runPromise(
      applyCommandCodeAcpModelSelection({
        runtime,
        currentModelId: COMMAND_CODE_DEFAULT_MODEL,
        requestedModelId: "deepseek/deepseek-v4-pro",
        mapError: (cause) => cause.message,
      }),
    );
    expect(modelCalls).toEqual(["deepseek/deepseek-v4-pro"]);
    expect(result).toBe("deepseek/deepseek-v4-pro");
  });

  it("skips session/set_model when the requested model matches", async () => {
    const { runtime, modelCalls } = makeRecordingRuntime();
    const result = await Effect.runPromise(
      applyCommandCodeAcpModelSelection({
        runtime,
        currentModelId: COMMAND_CODE_DEFAULT_MODEL,
        requestedModelId: COMMAND_CODE_DEFAULT_MODEL,
        mapError: (cause) => cause.message,
      }),
    );
    expect(modelCalls).toEqual([]);
    expect(result).toBe(COMMAND_CODE_DEFAULT_MODEL);
  });

  it("skips session/set_model when no model is requested", async () => {
    const { runtime, modelCalls } = makeRecordingRuntime();
    const result = await Effect.runPromise(
      applyCommandCodeAcpModelSelection({
        runtime,
        currentModelId: COMMAND_CODE_DEFAULT_MODEL,
        requestedModelId: undefined,
        mapError: (cause) => cause.message,
      }),
    );
    expect(modelCalls).toEqual([]);
    expect(result).toBe(COMMAND_CODE_DEFAULT_MODEL);
  });

  it("propagates session/set_model failures through mapError", async () => {
    const failure = EffectAcpErrors.AcpRequestError.invalidParams("Unknown model: nope");
    const { runtime } = makeRecordingRuntime(failure);
    const error = await Effect.runPromise(
      Effect.flip(
        applyCommandCodeAcpModelSelection({
          runtime,
          currentModelId: COMMAND_CODE_DEFAULT_MODEL,
          requestedModelId: "nope",
          mapError: (cause) => cause.message,
        }),
      ),
    );
    expect(error).toBe(failure.message);
  });
});
