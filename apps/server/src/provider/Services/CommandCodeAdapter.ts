/**
 * CommandCodeAdapter — shape type for the Command Code provider adapter.
 *
 * The driver model ({@link ../Drivers/CommandCodeDriver}) bundles one adapter
 * per instance as a captured closure, so this module only retains the shape
 * interface as a naming anchor for the driver bundle.
 *
 * @module CommandCodeAdapter
 */
import type { ProviderAdapterError } from "../Errors.ts";
import type { ProviderAdapterShape } from "./ProviderAdapter.ts";

/**
 * CommandCodeAdapterShape — per-instance Command Code adapter contract.
 */
export interface CommandCodeAdapterShape extends ProviderAdapterShape<ProviderAdapterError> {}
