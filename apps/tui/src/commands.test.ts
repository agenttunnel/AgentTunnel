import { describe, expect, it } from "vite-plus/test";

import { commandByName, matchCommands, parseCommandInput, TUI_COMMANDS } from "./commands.ts";

describe("parseCommandInput", () => {
  it("splits a command from its argument", () => {
    expect(parseCommandInput("/model gpt-5")).toEqual({ name: "model", argument: "gpt-5" });
  });

  it("reports an empty argument for a command typed on its own", () => {
    expect(parseCommandInput("/quit")).toEqual({ name: "quit", argument: "" });
  });

  it("treats a bare slash as the user starting to type, not a command", () => {
    expect(parseCommandInput("/")).toBeNull();
    expect(parseCommandInput("/ ")).toBeNull();
    expect(parseCommandInput("   ")).toBeNull();
  });

  it("ignores text that is not a command", () => {
    expect(parseCommandInput("")).toBeNull();
    expect(parseCommandInput("hello")).toBeNull();
    expect(parseCommandInput("hello /quit")).toBeNull();
  });

  it("tolerates whitespace around the input and between name and argument", () => {
    expect(parseCommandInput("  /model   gpt-5  ")).toEqual({ name: "model", argument: "gpt-5" });
    expect(parseCommandInput("/model gpt 5 ")).toEqual({ name: "model", argument: "gpt 5" });
  });

  it("keeps the case it was given", () => {
    expect(parseCommandInput("/HELP")).toEqual({ name: "HELP", argument: "" });
    expect(parseCommandInput("/model GPT-5")).toEqual({ name: "model", argument: "GPT-5" });
  });
});

describe("matchCommands", () => {
  it("offers every command in both views but the view-scoped ones", () => {
    const threads = matchCommands("", "threads").map((command) => command.name);
    const thread = matchCommands("", "thread").map((command) => command.name);
    expect(threads).not.toContain("threads");
    expect(threads).not.toContain("interrupt");
    expect(thread).toEqual([
      "new",
      "threads",
      "model",
      "provider",
      "settings",
      "agenttunnel",
      "agents",
      "token-usage",
      "help",
      "interrupt",
      "refresh",
      "quit",
    ]);
    expect(threads).toEqual([
      "new",
      "model",
      "provider",
      "settings",
      "agenttunnel",
      "help",
      "refresh",
      "quit",
    ]);
  });

  it("matches the query as a case-insensitive prefix", () => {
    expect(matchCommands("mod", "threads").map((command) => command.name)).toEqual(["model"]);
    expect(matchCommands("Pro", "thread").map((command) => command.name)).toEqual(["provider"]);
    expect(matchCommands("TH", "thread").map((command) => command.name)).toEqual(["threads"]);
  });

  it("trims the query and returns nothing when it matches no command", () => {
    expect(matchCommands("  he ", "thread").map((command) => command.name)).toEqual(["help"]);
    expect(matchCommands("nope", "thread")).toEqual([]);
  });
});

describe("commandByName", () => {
  it("resolves a known name case-insensitively", () => {
    expect(commandByName("quit")?.name).toBe("quit");
    expect(commandByName("SETTINGS")?.name).toBe("settings");
    expect(commandByName("Interrupt")?.scope).toBe("thread");
  });

  it("returns null for an unknown name", () => {
    expect(commandByName("nope")).toBeNull();
    expect(commandByName("")).toBeNull();
  });

  it("resolves every command in the table", () => {
    for (const command of TUI_COMMANDS) {
      expect(commandByName(command.name)).toBe(command);
    }
  });

  it("only marks the argument-taking commands", () => {
    const withArgument = TUI_COMMANDS.filter((command) => command.argument !== undefined).map(
      (command) => command.name,
    );
    expect(withArgument).toEqual(["model", "provider"]);
  });
});
