// @effect-diagnostics nodeBuiltinImport:off - the suite builds real files in a temp directory, with no Effect runtime involved.
import { afterEach, describe, expect, it } from "vite-plus/test";
import * as NodeFS from "node:fs";
import * as NodeOS from "node:os";
import * as NodePath from "node:path";

import {
  defaultPreferences,
  loadPreferences,
  parsePreferences,
  preferencesPath,
  savePreferences,
  serializePreferences,
  type TuiPreferences,
} from "./preferences.ts";

const settings: TuiPreferences = {
  defaultProvider: "anthropic",
  defaultModel: "claude-sonnet-4",
  showAgentDetails: false,
};

const temporaryDirectories: string[] = [];

/** A fresh directory per test, so nothing here touches the real user config. */
function temporaryDirectory(): string {
  const directory = NodeFS.mkdtempSync(NodePath.join(NodeOS.tmpdir(), "tui-preferences-"));
  temporaryDirectories.push(directory);
  return directory;
}

afterEach(() => {
  for (const directory of temporaryDirectories.splice(0)) {
    NodeFS.rmSync(directory, { recursive: true, force: true });
  }
});

describe("preferencesPath", () => {
  it("prefers XDG_CONFIG_HOME", () => {
    expect(preferencesPath({ XDG_CONFIG_HOME: "/xdg", HOME: "/home/ada" })).toBe(
      "/xdg/agenttunnel/tui.json",
    );
  });

  it("falls back to HOME/.config when XDG_CONFIG_HOME is unset or empty", () => {
    expect(preferencesPath({ HOME: "/home/ada" })).toBe("/home/ada/.config/agenttunnel/tui.json");
    expect(preferencesPath({ XDG_CONFIG_HOME: "", HOME: "/home/ada" })).toBe(
      "/home/ada/.config/agenttunnel/tui.json",
    );
  });

  it("says so when the environment gives it no home at all", () => {
    expect(() => preferencesPath({})).toThrow(/XDG_CONFIG_HOME or HOME/u);
  });
});

describe("parsePreferences", () => {
  it("reads every field", () => {
    expect(parsePreferences(JSON.stringify(settings))).toEqual(settings);
  });

  it("falls back to the defaults for unreadable text", () => {
    expect(parsePreferences("{ not json")).toEqual(defaultPreferences);
    expect(parsePreferences("")).toEqual(defaultPreferences);
    expect(parsePreferences("null")).toEqual(defaultPreferences);
    expect(parsePreferences("[1, 2]")).toEqual(defaultPreferences);
  });

  it("keeps the good fields and defaults only the bad ones", () => {
    expect(parsePreferences('{"defaultProvider":"openai"}')).toEqual({
      defaultProvider: "openai",
      defaultModel: null,
      showAgentDetails: true,
    });
    expect(parsePreferences('{"defaultModel":42,"showAgentDetails":"yes"}')).toEqual(
      defaultPreferences,
    );
    expect(
      parsePreferences('{"defaultProvider":"  ","defaultModel":"gpt-5","extra":true}'),
    ).toEqual({ defaultProvider: null, defaultModel: "gpt-5", showAgentDetails: true });
  });

  it("round-trips through serialization, trailing newline included", () => {
    const text = serializePreferences(settings);
    expect(text.endsWith("\n")).toBe(true);
    expect(parsePreferences(text)).toEqual(settings);
    expect(JSON.parse(text)).toEqual({
      defaultProvider: "anthropic",
      defaultModel: "claude-sonnet-4",
      showAgentDetails: false,
    });
    expect(Object.keys(JSON.parse(serializePreferences(defaultPreferences)))).toEqual([
      "defaultProvider",
      "defaultModel",
      "showAgentDetails",
    ]);
  });
});

describe("loadPreferences and savePreferences", () => {
  it("returns the defaults when the file is missing", async () => {
    const path = NodePath.join(temporaryDirectory(), "agenttunnel", "tui.json");
    expect(await loadPreferences(path)).toEqual(defaultPreferences);
  });

  it("returns the defaults for a corrupt file instead of throwing", async () => {
    const directory = temporaryDirectory();
    const path = NodePath.join(directory, "tui.json");
    NodeFS.writeFileSync(path, '{"defaultProvider":');
    expect(await loadPreferences(path)).toEqual(defaultPreferences);
  });

  it("creates parent directories and leaves valid JSON behind", async () => {
    const path = NodePath.join(temporaryDirectory(), "nested", "config", "tui.json");
    await savePreferences(settings, path);
    expect(await loadPreferences(path)).toEqual(settings);
    expect(JSON.parse(NodeFS.readFileSync(path, "utf8"))).toEqual(settings);
    expect(NodeFS.readdirSync(NodePath.dirname(path))).toEqual(["tui.json"]);
  });

  it("replaces the previous settings", async () => {
    const path = NodePath.join(temporaryDirectory(), "tui.json");
    await savePreferences(settings, path);
    await savePreferences(defaultPreferences, path);
    expect(await loadPreferences(path)).toEqual(defaultPreferences);
  });
});
