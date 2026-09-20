import { ProviderDriverKind } from "@t3tools/contracts";
import { describe, expect, it } from "vite-plus/test";

import { keepsUnavailableModelSelection } from "./providerModels";

describe("keepsUnavailableModelSelection", () => {
  it("preserves unavailable selections for catalog-backed drivers", () => {
    expect(keepsUnavailableModelSelection(ProviderDriverKind.make("opencode"))).toBe(true);
    expect(keepsUnavailableModelSelection(ProviderDriverKind.make("pi"))).toBe(true);
  });

  it("falls back to the catalog for static drivers", () => {
    expect(keepsUnavailableModelSelection(ProviderDriverKind.make("codex"))).toBe(false);
    expect(keepsUnavailableModelSelection(ProviderDriverKind.make("claudeAgent"))).toBe(false);
  });
});
