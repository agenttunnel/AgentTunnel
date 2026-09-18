import {
  OpenCodeSettings,
  type AgentTunnelSettings,
  type ModelSelection,
} from "@t3tools/contracts";
import * as Effect from "effect/Effect";
import * as Schema from "effect/Schema";
import * as Scope from "effect/Scope";

import { ServerConfig } from "../config.ts";
import { OpenCodeRuntime } from "../provider/opencodeRuntime.ts";
import * as OpenCodeServerOwner from "../provider/OpenCodeServerOwner.ts";
import { makeOpenCodeTextGeneration } from "./OpenCodeTextGeneration.ts";
import type * as TextGeneration from "./TextGeneration.ts";

const decodeOpenCodeSettings = Schema.decodeSync(OpenCodeSettings);

export interface AgentTunnelTextGenerationOptions {
  /**
   * Model slugs discovered for the AgentTunnel harness. When a requested
   * text-generation model is not available in the spawned opencode server,
   * operations fall back to the first available model instead of failing.
   */
  readonly availableModels?: ReadonlyArray<string>;
}

interface ModelSelectionInput {
  readonly modelSelection: ModelSelection;
}

/**
 * Reuse OpenCode's structured text-generation helpers for AgentTunnel.
 *
 * The requested model comes from the thread's model selection, which may not
 * exist in the spawned server's (empty-config) inventory. Resolve it against
 * the harness's discovered models so title, branch, commit, and PR generation
 * always target a model the user actually has configured.
 */
export const makeAgentTunnelTextGeneration = Effect.fn("makeAgentTunnelTextGeneration")(function* (
  settings: AgentTunnelSettings,
  _environment?: NodeJS.ProcessEnv,
  options?: AgentTunnelTextGenerationOptions,
): Effect.fn.Return<
  TextGeneration.TextGeneration["Service"],
  never,
  OpenCodeRuntime | ServerConfig | OpenCodeServerOwner.OpenCodeServerOwner | Scope.Scope
> {
  const openCodeSettings = decodeOpenCodeSettings({
    binaryPath: settings.orchestratorHarness,
    customModels: settings.customModels,
  });
  const base = yield* makeOpenCodeTextGeneration(openCodeSettings);

  const availableModels = options?.availableModels ?? [];
  const available = new Set(availableModels);
  const resolveModel = (model: string): string => {
    if (available.size === 0 || available.has(model)) return model;
    return availableModels[0] ?? model;
  };
  const resolveSelection = <Input extends ModelSelectionInput>(input: Input): Input => ({
    ...input,
    modelSelection: {
      ...input.modelSelection,
      model: resolveModel(input.modelSelection.model),
    },
  });

  return {
    generateCommitMessage: (input) => base.generateCommitMessage(resolveSelection(input)),
    generatePrContent: (input) => base.generatePrContent(resolveSelection(input)),
    generateBranchName: (input) => base.generateBranchName(resolveSelection(input)),
    generateThreadTitle: (input) => base.generateThreadTitle(resolveSelection(input)),
  } satisfies TextGeneration.TextGeneration["Service"];
});
