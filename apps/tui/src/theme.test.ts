import { describe, expect, it } from "vite-plus/test";

import { pad, paint, palette, truncate, visibleWidth } from "./theme.ts";

const RESET = "\u001b[0m";
const ERROR_FOREGROUND = "\u001b[38;2;247;118;142m";

describe("palette", () => {
  it("keeps the app's colors", () => {
    expect(palette).toEqual({
      primary: "#7aa2f7",
      text: "#e6e6ef",
      muted: "#8b8b9e",
      faint: "#5a5a6b",
      ok: "#9ece6a",
      warn: "#e0af68",
      err: "#f7768e",
    });
  });
});

describe("paint", () => {
  it("wraps text in the color's truecolor foreground and resets", () => {
    expect(paint("hello", "primary")).toBe("\u001b[38;2;122;162;247mhello\u001b[0m");
    expect(paint("boom", "err")).toBe(`${ERROR_FOREGROUND}boom${RESET}`);
    expect(paint("", "ok")).toBe("\u001b[38;2;158;206;106m\u001b[0m");
  });

  it("adds the bold attribute when asked", () => {
    expect(paint("hi", "err", { bold: true })).toBe(`\u001b[1;38;2;247;118;142mhi${RESET}`);
    expect(paint("hi", "err", { bold: false })).toBe(paint("hi", "err"));
  });

  it("is safe on text that already carries SGR", () => {
    const nested = paint(paint("x", "ok"), "err");
    expect(nested).toBe(`${ERROR_FOREGROUND}\u001b[38;2;158;206;106mx${RESET}${RESET}`);
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
    expect(cut).toBe(`\u001b[38;2;122;162;247mhello…${RESET}`);
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
