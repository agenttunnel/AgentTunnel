import { ProviderDriverKind } from "@t3tools/contracts";
import { describe, expect, it } from "vite-plus/test";

import { modelMatchesHarnessFilter, resolveModelPickerHarnessFilter } from "./modelPickerHarness";

const tagged = (
  driverKind: "agenttunnel" | "pi" | "opencode" | "codex",
  harnesses?: ReadonlyArray<string>,
) => {
  const kind = ProviderDriverKind.make(driverKind);
  return harnesses === undefined ? { driverKind: kind } : { driverKind: kind, harnesses };
};

describe("resolveModelPickerHarnessFilter", () => {
  it("has nothing to narrow when no workflow harness is in play", () => {
    expect(resolveModelPickerHarnessFilter({ harness: null, models: [] })).toBeNull();
    expect(resolveModelPickerHarnessFilter({ harness: "  ", models: [] })).toBeNull();
  });

  it("narrows to a harness preset's provider even before its catalog arrives", () => {
    expect(resolveModelPickerHarnessFilter({ harness: "pi", models: [] })).toEqual({
      harness: "pi",
      driverKind: "pi",
    });
  });

  it("resolves a configured harness path to the CLI it runs", () => {
    expect(
      resolveModelPickerHarnessFilter({ harness: "/opt/homebrew/bin/pi", models: [] }),
    ).toEqual({ harness: "/opt/homebrew/bin/pi", driverKind: "pi" });
  });

  it("keeps the full list when the harness cannot be mapped to any model source", () => {
    expect(
      resolveModelPickerHarnessFilter({
        harness: "/usr/local/bin/mystery-agent",
        models: [tagged("agenttunnel", ["opencode"])],
      }),
    ).toBeNull();
  });

  it("still narrows an unmapped harness that the workflow catalog reported", () => {
    expect(
      resolveModelPickerHarnessFilter({
        harness: "/usr/local/bin/mystery-agent",
        models: [tagged("agenttunnel", ["/usr/local/bin/mystery-agent"])],
      }),
    ).toEqual({ harness: "/usr/local/bin/mystery-agent", driverKind: null });
  });
});

describe("modelMatchesHarnessFilter", () => {
  it("keeps every row without a filter", () => {
    expect(modelMatchesHarnessFilter(tagged("pi"), null)).toBe(true);
    expect(modelMatchesHarnessFilter(tagged("agenttunnel", ["opencode"]), null)).toBe(true);
  });

  it("matches only the harnesses that reported a workflow model", () => {
    const filter = resolveModelPickerHarnessFilter({ harness: "pi", models: [] });
    expect(modelMatchesHarnessFilter(tagged("agenttunnel", ["opencode", "pi"]), filter)).toBe(true);
    expect(modelMatchesHarnessFilter(tagged("agenttunnel", ["opencode"]), filter)).toBe(false);
  });

  it("keeps custom workflow models available under every harness", () => {
    const filter = resolveModelPickerHarnessFilter({ harness: "pi", models: [] });
    expect(modelMatchesHarnessFilter(tagged("agenttunnel"), filter)).toBe(true);
  });

  it("accepts another provider's rows only when that provider runs the harness", () => {
    const piFilter = resolveModelPickerHarnessFilter({ harness: "pi", models: [] });
    expect(modelMatchesHarnessFilter(tagged("pi"), piFilter)).toBe(true);
    expect(modelMatchesHarnessFilter(tagged("codex"), piFilter)).toBe(false);
  });
});
