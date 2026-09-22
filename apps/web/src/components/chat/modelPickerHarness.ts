import { ProviderDriverKind } from "@t3tools/contracts";

import { agentTunnelHarnessDriverKind } from "@t3tools/client-runtime/state/agentTunnelRoles";

const AGENT_TUNNEL_DRIVER_KIND = ProviderDriverKind.make("agenttunnel");

/** The model rows a workflow role may choose from, narrowed to its harness. */
export interface ModelPickerHarnessFilter {
  readonly harness: string;
  /**
   * Driver kind whose own catalog this harness runs, when the harness is a
   * CLI we recognize. Model config shows that instance's models too, so a
   * harness stays selectable before the workflow provider has listed it.
   */
  readonly driverKind: ProviderDriverKind | null;
}

/** Minimal shape the filter needs; both picker rows and raw options satisfy it. */
export interface HarnessTaggedModel {
  readonly driverKind: ProviderDriverKind;
  readonly harnesses?: ReadonlyArray<string> | undefined;
}

/**
 * Resolve the harness narrowing for a workflow role's model list.
 *
 * Returns null when the picker should keep showing every catalog: no workflow
 * harness is in play, or the harness names a CLI and configuration we cannot
 * map to any model source. Filtering those would leave an empty list with no
 * way back, which is worse than the unfiltered picker.
 */
export function resolveModelPickerHarnessFilter(input: {
  readonly harness: string | null | undefined;
  readonly models: ReadonlyArray<HarnessTaggedModel>;
}): ModelPickerHarnessFilter | null {
  const harness = input.harness?.trim();
  if (!harness) {
    return null;
  }
  const driverKind = agentTunnelHarnessDriverKind(harness);
  const tagged = input.models.some((model) => model.harnesses?.includes(harness) === true);
  if (driverKind === null && !tagged) {
    return null;
  }
  return { harness, driverKind };
}

/**
 * Whether a row belongs to the harness being configured.
 *
 * AgentTunnel's own catalog carries harness tags, so a tagged row matches only
 * the harnesses that reported it. Custom models arrive untagged because the
 * user typed them for the workflow rather than for one CLI, so they stay
 * available everywhere. Every other provider's rows are untagged too: they
 * belong to the harness whose CLI they come from.
 */
export function modelMatchesHarnessFilter(
  model: HarnessTaggedModel,
  filter: ModelPickerHarnessFilter | null,
): boolean {
  if (filter === null) {
    return true;
  }
  if (model.harnesses !== undefined) {
    return model.harnesses.includes(filter.harness);
  }
  if (model.driverKind === AGENT_TUNNEL_DRIVER_KIND) {
    return true;
  }
  return filter.driverKind !== null && model.driverKind === filter.driverKind;
}
