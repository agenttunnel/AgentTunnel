import { describe, expect, it } from "vite-plus/test";

import { resolveCommandCodeModeId } from "./CommandCodeAdapter.ts";
import type { AcpSessionModeState } from "../acp/AcpRuntimeModel.ts";

const modeState: AcpSessionModeState = {
  currentModeId: "default",
  availableModes: [
    { id: "default", name: "Standard", description: "Ask before running tools." },
    { id: "auto-accept", name: "Accept Edits", description: "Auto-accept file edits." },
    { id: "plan", name: "Plan", description: "Research and plan only." },
    { id: "dont-ask", name: "Don't Ask", description: "Run without prompting." },
    { id: "bypass", name: "Bypass Permissions", description: "Skip every permission check." },
  ],
};

describe("resolveCommandCodeModeId", () => {
  it("maps AgentTunnel runtime modes onto Command Code permission modes", () => {
    expect(
      resolveCommandCodeModeId({
        interactionMode: undefined,
        runtimeMode: "approval-required",
        modeState,
      }),
    ).toBe("default");
    expect(
      resolveCommandCodeModeId({
        interactionMode: undefined,
        runtimeMode: "auto-accept-edits",
        modeState,
      }),
    ).toBe("auto-accept");
    expect(
      resolveCommandCodeModeId({ interactionMode: undefined, runtimeMode: "auto", modeState }),
    ).toBe("dont-ask");
    expect(
      resolveCommandCodeModeId({
        interactionMode: undefined,
        runtimeMode: "full-access",
        modeState,
      }),
    ).toBe("bypass");
  });

  it("prefers the plan interaction mode over the runtime mode", () => {
    expect(
      resolveCommandCodeModeId({ interactionMode: "plan", runtimeMode: "full-access", modeState }),
    ).toBe("plan");
  });

  it("returns undefined when the agent advertises no modes", () => {
    expect(
      resolveCommandCodeModeId({
        interactionMode: undefined,
        runtimeMode: "auto",
        modeState: undefined,
      }),
    ).toBeUndefined();
  });

  it("returns undefined when the requested mode is not advertised", () => {
    const partialModeState: AcpSessionModeState = {
      currentModeId: "default",
      availableModes: [{ id: "default", name: "Standard" }],
    };
    expect(
      resolveCommandCodeModeId({
        interactionMode: undefined,
        runtimeMode: "full-access",
        modeState: partialModeState,
      }),
    ).toBeUndefined();
  });
});
