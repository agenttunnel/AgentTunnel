/**
 * PiAdapter — shape type for the pi provider adapter.
 *
 * Like the other driver-model adapters this module only retains the shape
 * interface as a naming anchor; the driver bundles one adapter per instance
 * as a captured closure, so there is no Effect service tag.
 *
 * @module PiAdapter
 */
import type { ProviderAdapterError } from "../Errors.ts";
import type { ProviderAdapterShape } from "./ProviderAdapter.ts";

/**
 * PiAdapterShape — per-instance pi adapter contract. Carries a branded driver
 * kind as the nominal discriminant via {@link ProviderAdapterShape}.
 */
export interface PiAdapterShape extends ProviderAdapterShape<ProviderAdapterError> {}
