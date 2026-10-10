/**
 * Static harness catalogs for the AgentTunnel workflow driver.
 *
 * Wiring a harness CLI into the workflow model config means the driver's
 * registry probe can list models for it. OpenCode and pi list their models
 * over their own CLI. Claude gates a static catalog on its `--version`
 * output. The harnesses here — codex, grok, cursor — have no cheap listing
 * subcommand (codex's model list lives behind the app-server handshake,
 * grok and cursor report over ACP), so their workflow catalogs are the
 * static sets their own provider snapshots fall back to, gated on a
 * `--version` probe to confirm the CLI exists. Their provider instances
 * still show the live, authenticated lists; a workflow role only needs a
 * dispatchable slug to run.
 */
import type { ServerProviderModel } from "@t3tools/contracts";
import { createModelCapabilities } from "@t3tools/shared/model";

const EMPTY_CAPABILITIES = createModelCapabilities({
  optionDescriptors: [],
});

function model(slug: string, name: string): ServerProviderModel {
  return { slug, name, isCustom: false, capabilities: EMPTY_CAPABILITIES };
}

/** Codex's workflow catalog: the default-model preference line from contracts. */
export const CODEX_HARNESS_CATALOG: ReadonlyArray<ServerProviderModel> = [
  { ...model("gpt-5.6-sol", "GPT-5.6 Sol"), isDefault: true },
  model("gpt-5.6-terra", "GPT-5.6 Terra"),
  model("gpt-5.6-luna", "GPT-5.6 Luna"),
];

/** Grok's workflow catalog mirrors its provider snapshot's built-in set. */
export const GROK_HARNESS_CATALOG: ReadonlyArray<ServerProviderModel> = [
  model("grok-build", "Grok Build"),
];

/** Cursor's workflow catalog: the parameterless model its CLI exposes. */
export const CURSOR_HARNESS_CATALOG: ReadonlyArray<ServerProviderModel> = [
  model("auto", "Cursor Auto"),
];

export function getBuiltInHarnessModelsForKind(
  kind: "codex" | "grok" | "cursor",
): ReadonlyArray<ServerProviderModel> {
  switch (kind) {
    case "codex":
      return CODEX_HARNESS_CATALOG;
    case "grok":
      return GROK_HARNESS_CATALOG;
    case "cursor":
      return CURSOR_HARNESS_CATALOG;
  }
}
