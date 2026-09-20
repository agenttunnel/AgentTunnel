import { describe, expect, it } from "@effect/vitest";
import * as Schema from "effect/Schema";

import { PiDriver } from "./PiDriver.ts";

const decodeConfig = Schema.decodeUnknownSync(PiDriver.configSchema);

describe("PiDriver", () => {
  it("advertises the pi driver kind and display name", () => {
    expect(PiDriver.driverKind).toBe("pi");
    expect(PiDriver.metadata.displayName).toBe("Pi");
    expect(PiDriver.metadata.supportsMultipleInstances).toBe(true);
  });

  it("defaults to disabled with the pi binary", () => {
    expect(PiDriver.defaultConfig()).toMatchObject({
      enabled: false,
      binaryPath: "pi",
      customModels: [],
    });
  });

  it("decodes enabled and binaryPath overrides", () => {
    expect(decodeConfig({})).toMatchObject({ enabled: false, binaryPath: "pi" });
    expect(decodeConfig({ enabled: true })).toMatchObject({ enabled: true, binaryPath: "pi" });
    expect(decodeConfig({ binaryPath: "/opt/homebrew/bin/pi" })).toMatchObject({
      binaryPath: "/opt/homebrew/bin/pi",
    });
  });
});
