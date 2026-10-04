import { describe, expect, it } from "vite-plus/test";

import { parseKeys } from "./keys.ts";

describe("parseKeys", () => {
  it("reads printable input one character at a time", () => {
    expect(parseKeys("abc")).toEqual([{ text: "a" }, { text: "b" }, { text: "c" }]);
  });

  it("reads the control bytes the reducer knows", () => {
    expect(parseKeys("\r")).toEqual([{ name: "enter" }]);
    expect(parseKeys("\u001b")).toEqual([{ name: "escape" }]);
    expect(parseKeys("\u007f")).toEqual([{ name: "backspace" }]);
    expect(parseKeys("\u0003")).toEqual([{ name: "ctrl-c" }]);
    expect(parseKeys("\u0012")).toEqual([{ name: "ctrl-r" }]);
    expect(parseKeys("\u0001")).toEqual([{ name: "ctrl-a" }]);
    expect(parseKeys("\u0010")).toEqual([{ name: "ctrl-p" }]);
  });

  it("reads the arrow keys as one key each, not as escape sequences", () => {
    expect(parseKeys("\u001b[A")).toEqual([{ name: "up" }]);
    expect(parseKeys("\u001b[B\u001b[C\u001b[D")).toEqual([
      { name: "down" },
      { name: "right" },
      { name: "left" },
    ]);
  });

  it("reads the tilde keys, which the composer leaves for scrolling", () => {
    expect(parseKeys("\u001b[5~")).toEqual([{ name: "pageup" }]);
    expect(parseKeys("\u001b[6~")).toEqual([{ name: "pagedown" }]);
    // The delete key shares the shape and must keep working.
    expect(parseKeys("\u001b[3~")).toEqual([{ name: "delete" }]);
  });

  it("tells ctrl-i from tab through the kitty protocol", () => {
    expect(parseKeys("\u0009")).toEqual([{ name: "tab" }]);
    expect(parseKeys("\u001b[105;5u")).toEqual([{ name: "ctrl-i" }]);
  });

  it("passes pasted text through as one key and drops the markers", () => {
    expect(parseKeys("\u001b[200~def foo\r\nbar\u001b[201~")).toEqual([{ text: "def foo\nbar" }]);
  });

  it("keeps keys in order across a mixed chunk", () => {
    expect(parseKeys("/new\u001b[A\r")).toEqual([
      { text: "/" },
      { text: "n" },
      { text: "e" },
      { text: "w" },
      { name: "up" },
      { name: "enter" },
    ]);
  });

  it("ignores sequences it does not know instead of typing them", () => {
    // A paste marker on its own carries no text, and an unknown CSI is dropped.
    expect(parseKeys("\u001b[200~")).toEqual([]);
    expect(parseKeys("\u001b[999z")).toEqual([]);
    // Text after an unterminated paste marker is still the user typing.
    expect(parseKeys("\u001b[200~x")).toEqual([{ text: "x" }]);
  });
});
