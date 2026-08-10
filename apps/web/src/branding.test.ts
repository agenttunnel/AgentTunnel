import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import {
  resolveServerBackedAppDisplayName,
  resolveServerBackedAppStageLabel,
} from "./branding.logic";

const originalWindow = globalThis.window;

afterEach(() => {
  vi.resetModules();

  if (originalWindow === undefined) {
    Reflect.deleteProperty(globalThis, "window");
    return;
  }

  globalThis.window = originalWindow;
});

describe("branding", () => {
  it("uses injected desktop branding when available", async () => {
    Object.defineProperty(globalThis, "window", {
      configurable: true,
      value: {
        desktopBridge: {
          getAppBranding: () => ({
            baseName: "AgentTunnel",
            stageLabel: "Nightly",
            displayName: "AgentTunnel (Nightly)",
          }),
        },
      },
    });

    const branding = await import("./branding");

    expect(branding.APP_BASE_NAME).toBe("AgentTunnel");
    expect(branding.APP_STAGE_LABEL).toBe("Nightly");
    expect(branding.APP_DISPLAY_NAME).toBe("AgentTunnel (Nightly)");
  });

  it("normalizes hosted app channel metadata", async () => {
    vi.stubEnv("VITE_HOSTED_APP_CHANNEL", "nightly");

    const branding = await import("./branding");

    expect(branding.HOSTED_APP_CHANNEL).toBe("nightly");
    expect(branding.HOSTED_APP_CHANNEL_LABEL).toBe("Nightly");
    expect(branding.APP_STAGE_LABEL).toBe("Nightly");
    expect(branding.APP_DISPLAY_NAME).toBe("AgentTunnel (Nightly)");
  });

  it("does not label the latest hosted app channel", async () => {
    vi.stubEnv("VITE_HOSTED_APP_CHANNEL", "latest");

    const branding = await import("./branding");

    expect(branding.HOSTED_APP_CHANNEL).toBe("latest");
    expect(branding.HOSTED_APP_CHANNEL_LABEL).toBe("Latest");
    expect(branding.APP_STAGE_LABEL).toBe("Latest");
    expect(branding.APP_DISPLAY_NAME).toBe("AgentTunnel");
  });

  it("ignores unknown hosted app channels", async () => {
    vi.stubEnv("VITE_HOSTED_APP_CHANNEL", "preview");

    const branding = await import("./branding");

    expect(branding.HOSTED_APP_CHANNEL).toBeNull();
    expect(branding.HOSTED_APP_CHANNEL_LABEL).toBeNull();
  });
});

describe("branding logic", () => {
  it("returns Nightly for nightly primary server versions", () => {
    expect(
      resolveServerBackedAppStageLabel({
        primaryServerVersion: "0.0.28-nightly.20260616.12",
        fallbackStageLabel: "Alpha",
      }),
    ).toBe("Nightly");
  });

  it("updates the display name for nightly primary server versions", () => {
    expect(
      resolveServerBackedAppDisplayName({
        baseName: "AgentTunnel",
        fallbackDisplayName: "AgentTunnel (Alpha)",
        fallbackStageLabel: "Alpha",
        primaryServerVersion: "0.0.28-nightly.20260616.12",
      }),
    ).toBe("AgentTunnel (Nightly)");
  });

  it("keeps the fallback display name for stable primary server versions", () => {
    expect(
      resolveServerBackedAppDisplayName({
        baseName: "AgentTunnel",
        fallbackDisplayName: "AgentTunnel (Alpha)",
        fallbackStageLabel: "Alpha",
        primaryServerVersion: "0.0.27",
      }),
    ).toBe("AgentTunnel (Alpha)");
  });

  it("keeps the fallback display name for malformed nightly primary server versions", () => {
    expect(
      resolveServerBackedAppDisplayName({
        baseName: "AgentTunnel",
        fallbackDisplayName: "AgentTunnel (Alpha)",
        fallbackStageLabel: "Alpha",
        primaryServerVersion: "0.0.28-nightly.20260616",
      }),
    ).toBe("AgentTunnel (Alpha)");
  });
});
