import { describe, expect, it } from "vite-plus/test";

import {
  ACCENTS,
  ACCENT_NAMES,
  DARK_PALETTE,
  LIGHT_PALETTE,
  accentName,
  applyAccent,
  applyPaletteOverrides,
  applyTerminalTheme,
  chip,
  fleetHue,
  isLightBackground,
  keycap,
  nextAccent,
  pad,
  paint,
  palette,
  parseBackgroundReply,
  surfaces,
  terminalTheme,
  truncate,
  visibleWidth,
} from "./theme.ts";

const RESET = "\u001b[0m";
const ERROR_FOREGROUND = "\u001b[38;2;255;107;129m";
// eslint-disable-next-line no-control-regex -- ANSI escapes are control characters by construction.
const strip = (text: string): string => text.replace(/\u001b\[[0-9;]*m/gu, "");
/** A hex colour as one number, only ever used to compare two of them. */
const numberOf = (hex: string): number => Number.parseInt(hex.slice(1), 16);

describe("palette", () => {
  it("keeps the app's colors", () => {
    expect(palette).toEqual({
      primary: "#d97757",
      accent: "#c9a2ff",
      info: "#8ad4ff",
      text: "#f2f2f7",
      muted: "#a8a8c0",
      faint: "#74748c",
      line: "#42425c",
      ok: "#7fd88f",
      warn: "#f5a742",
      err: "#ff6b81",
    });
  });
});

describe("paint", () => {
  it("wraps text in the color's truecolor foreground and resets", () => {
    expect(paint("hello", "primary")).toBe("\u001b[38;2;217;119;87mhello\u001b[0m");
    expect(paint("boom", "err")).toBe(`${ERROR_FOREGROUND}boom${RESET}`);
    expect(paint("", "ok")).toBe("\u001b[38;2;127;216;143m\u001b[0m");
  });

  it("adds the bold attribute when asked", () => {
    expect(paint("hi", "err", { bold: true })).toBe(`\u001b[1;38;2;255;107;129mhi${RESET}`);
    expect(paint("hi", "err", { bold: false })).toBe(paint("hi", "err"));
  });

  it("is safe on text that already carries SGR", () => {
    const nested = paint(paint("x", "ok"), "err");
    expect(nested).toBe(`${ERROR_FOREGROUND}\u001b[38;2;127;216;143mx${RESET}${RESET}`);
  });
});

describe("visibleWidth", () => {
  it("counts ASCII by character", () => {
    expect(visibleWidth("")).toBe(0);
    expect(visibleWidth("thread 1: ready")).toBe(15);
  });

  it("gives the app's glyph set one column each, box drawing included", () => {
    expect(visibleWidth("›·⏺◍◌●✗⊘○…※")).toBe(11);
    expect(visibleWidth("─│┌┐└┘├┤")).toBe(8);
    for (const glyph of "›·⏺◍◌●✗⊘○…※") {
      expect(visibleWidth(glyph)).toBe(1);
    }
  });

  it("gives East Asian wide and fullwidth characters two columns", () => {
    expect(visibleWidth("日本語")).toBe(6);
    expect(visibleWidth("한국어")).toBe(6);
    expect(visibleWidth("ＡＢ")).toBe(4);
    expect(visibleWidth("a日b")).toBe(4);
  });

  it("gives combining marks none", () => {
    expect(visibleWidth("e\u0301")).toBe(1);
    expect(visibleWidth("a\u0301\u0302b")).toBe(2);
    expect(visibleWidth("a\u200db")).toBe(2);
  });

  it("ignores ANSI escapes", () => {
    expect(visibleWidth(paint("hello", "err"))).toBe(5);
    expect(visibleWidth(`${ERROR_FOREGROUND}\u001b[1mhi${RESET}`)).toBe(2);
  });
});

describe("truncate", () => {
  it("returns the text untouched when it fits", () => {
    expect(truncate("abc", 5)).toBe("abc");
    expect(truncate("abcde", 5)).toBe("abcde");
    const painted = paint("abc", "ok");
    expect(truncate(painted, 3)).toBe(painted);
    expect(truncate(painted, 9)).toBe(painted);
  });

  it("fills the last column with an ellipsis when it cuts", () => {
    expect(truncate("abcdefgh", 5)).toBe("abcd…");
    expect(truncate("abcdefgh", 1)).toBe("…");
    expect(truncate("abcdefgh", 0)).toBe("");
    expect(truncate("abc", -3)).toBe("");
  });

  it("never exceeds the width, wide characters included", () => {
    expect(truncate("日本語", 6)).toBe("日本語");
    expect(truncate("日本語", 3)).toBe("日…");
    expect(truncate("日本語", 4)).toBe("日…");
    const painted = paint("日本語", "primary");
    expect(visibleWidth(truncate(painted, 3))).toBe(3);
  });

  it("keeps escapes intact rather than slicing through them", () => {
    const painted = paint("hello world", "primary");
    const cut = truncate(painted, 6);
    expect(cut).toBe(`\u001b[38;2;217;119;87mhello…${RESET}`);
    expect(visibleWidth(cut)).toBe(6);
    // An escape that lands past the cut point is dropped whole, never half-written.
    expect(truncate(`ab${ERROR_FOREGROUND}cd`, 2)).toBe(`a…${RESET}`);
    expect(truncate(`ab${ERROR_FOREGROUND}cd`, 2)).not.toContain("[38;2");
  });

  it("leaves exactly `width` columns for every width up to the text length", () => {
    const painted = paint("AgentTunnel thread recap 日本語", "text");
    for (let width = 1; width <= visibleWidth(painted) + 2; width += 1) {
      const line = truncate(painted, width);
      expect(visibleWidth(line)).toBeLessThanOrEqual(width);
      expect(visibleWidth(pad(line, width))).toBe(width);
    }
  });
});

describe("pad", () => {
  it("fills to exactly the requested columns", () => {
    expect(pad("ab", 5)).toBe("ab   ");
    expect(pad("", 2)).toBe("  ");
    expect(pad("abcde", 5)).toBe("abcde");
  });

  it("never truncates", () => {
    expect(pad("abcdef", 3)).toBe("abcdef");
  });

  it("measures the columns, not the escape bytes", () => {
    const painted = paint("ab", "warn");
    expect(pad(painted, 5)).toBe(`${painted}   `);
    expect(visibleWidth(pad(painted, 5))).toBe(5);
  });
});

describe("surfaces", () => {
  it("paints the background and the foreground in one sequence", () => {
    expect(paint("hi", "primary", { bg: "raised" })).toBe(
      "\u001b[38;2;217;119;87;48;2;23;23;37mhi\u001b[0m",
    );
  });

  it("keeps bold in front of both colours", () => {
    expect(paint("hi", "text", { bold: true, bg: "high" })).toBe(
      "\u001b[1;38;2;242;242;247;48;2;32;32;47mhi\u001b[0m",
    );
  });

  it("never lets a surface widen what the row measures", () => {
    expect(visibleWidth(chip("main", "info"))).toBe(6);
    expect(visibleWidth(paint("hi", "text", { bg: "raised" }))).toBe(2);
  });

  it("shows a binding as a key and a quiet verb", () => {
    expect(strip(keycap("enter", "send"))).toBe("enter send");
  });
});

describe("terminal theme", () => {
  it("reads the terminal's background out of an OSC 11 reply", () => {
    expect(parseBackgroundReply("\u001b]11;rgb:1e1e/1e1e/2e2e\u0007")).toBe("#1e1e2e");
    expect(parseBackgroundReply("\u001b]11;rgb:1e/1e/2e\u0007")).toBe("#1e1e2e");
    // xterm and kitty terminate with ST rather than BEL; both must work.
    expect(parseBackgroundReply("\u001b]11;rgb:1e1e/1e1e/2e2e\u001b\\")).toBe("#1e1e2e");
    // Anything that is not that reply must fall through to the key parser.
    expect(parseBackgroundReply("hello")).toBeNull();
    expect(parseBackgroundReply("\u001b[11~")).toBeNull();
  });

  it("decides light or dark from the background's own luminance", () => {
    expect(isLightBackground("#ffffff")).toBe(true);
    expect(isLightBackground("#fafafa")).toBe(true);
    expect(isLightBackground("#1e1e2e")).toBe(false);
    expect(isLightBackground("#0a0a0c")).toBe(false);
  });

  it("derives surfaces from the terminal instead of assuming black", () => {
    applyTerminalTheme({ background: "#1e1e2e" });
    expect(terminalTheme().mode).toBe("dark");
    // Chrome has to be visible on the user's background, whatever it is.
    expect(surfaces.raised).not.toBe("#1e1e2e");
    expect(numberOf(surfaces.high)).toBeGreaterThan(numberOf(surfaces.raised));

    applyTerminalTheme({ background: "#fafafa" });
    expect(terminalTheme().mode).toBe("light");
    expect(palette.text).toBe(LIGHT_PALETTE.text);
    // On a light terminal the ramp goes darker, not lighter.
    expect(numberOf(surfaces.high)).toBeLessThan(numberOf(surfaces.raised));

    applyTerminalTheme({ background: "#0a0a0c" });
    expect(palette.text).toBe(DARK_PALETTE.text);
  });

  it("gives the user's own theme.json the last word", () => {
    applyTerminalTheme({ background: "#1e1e2e" });
    applyPaletteOverrides({ primary: "#ff0000" });
    expect(palette.primary).toBe("#ff0000");
    // Re-deriving for the terminal must not undo what the user chose.
    applyTerminalTheme({ background: "#fafafa" });
    expect(palette.primary).toBe("#ff0000");
    applyPaletteOverrides({ primary: DARK_PALETTE.primary });
    expect(palette.primary).toBe(DARK_PALETTE.primary);
  });

  it("paints nothing at all when the environment forbids colour", () => {
    applyTerminalTheme({ mode: "none" });
    expect(paint("hi", "primary", { bg: "raised" })).toBe("hi");
    expect(strip(chip("main", "info"))).toBe(" main ");
    applyTerminalTheme({ background: "#0a0a0c" });
    expect(paint("hi", "primary")).toContain("\u001b[");
  });
});

describe("accent", () => {
  it("takes a built-in name and follows the terminal's mode", () => {
    expect(applyAccent("mint")).toBe(true);
    expect(accentName()).toBe("mint");
    applyTerminalTheme({ background: "#1e1e2e" });
    expect(palette.primary).toBe(ACCENTS.mint.dark);
    applyTerminalTheme({ background: "#fafafa" });
    expect(palette.primary).toBe(ACCENTS.mint.light);
  });

  it("takes a hex, and refuses anything else", () => {
    expect(applyAccent("#ff00ff")).toBe(true);
    expect(palette.primary).toBe("#ff00ff");
    expect(applyAccent("chartreuse")).toBe(false);
    expect(applyAccent("#nope")).toBe(false);
    // A refused value leaves the accent alone rather than blanking it.
    expect(palette.primary).toBe("#ff00ff");
  });

  it("cycles through the built-in names, wrapping at the end", () => {
    const first = ACCENT_NAMES[0] ?? "clay";
    const last = ACCENT_NAMES.at(-1) ?? "clay";
    expect(nextAccent(first)).toBe(ACCENT_NAMES[1]);
    expect(nextAccent(last)).toBe(first);
  });

  it("clears a theme.json override of the same role, keeping the others", () => {
    // Dark explicitly: an earlier test left the terminal on a light background.
    applyTerminalTheme({ background: "#1e1e2e" });
    applyPaletteOverrides({ primary: "#00ff00", muted: "#123456" });
    expect(palette.primary).toBe("#00ff00");
    applyAccent("clay");
    // The accent is the more deliberate choice, so it wins the primary role...
    expect(palette.primary).toBe(ACCENTS.clay.dark);
    // ...and leaves every other role the user named alone.
    expect(palette.muted).toBe("#123456");
    applyPaletteOverrides({ muted: DARK_PALETTE.muted });
  });
});

describe("fleetHue", () => {
  it("gives the same agent the same hue every time", () => {
    expect(fleetHue("task_1")).toBe(fleetHue("task_1"));
  });

  it("lands on one of the fleet's colours", () => {
    for (const id of ["a", "task_1", "orchestrator", "3f9a", ""]) {
      expect(["primary", "accent", "info", "ok", "warn", "err"]).toContain(fleetHue(id));
    }
  });
});
