// Smoke for the pure focus helpers behind useDialogFocus's Tab trap.
// Run with: npm test (bun test tests, no extra dependencies).
import { expect, test } from "bun:test";
import { isTabbable, nextFocusIndex } from "../src/lib/dialogFocus";

test("Tab advances and wraps at the end", () => {
  expect(nextFocusIndex(0, 3, false)).toBe(1);
  expect(nextFocusIndex(1, 3, false)).toBe(2);
  expect(nextFocusIndex(2, 3, false)).toBe(0);
});

test("Shift+Tab steps back and wraps at the start", () => {
  expect(nextFocusIndex(2, 3, true)).toBe(1);
  expect(nextFocusIndex(0, 3, true)).toBe(2);
});

test("focus outside the list enters at the first or last element", () => {
  expect(nextFocusIndex(-1, 3, false)).toBe(0);
  expect(nextFocusIndex(-1, 3, true)).toBe(2);
});

test("an out-of-range current index is treated as outside the list", () => {
  expect(nextFocusIndex(5, 3, false)).toBe(0);
  expect(nextFocusIndex(5, 3, true)).toBe(2);
});

test("a single element stays put and an empty list yields -1", () => {
  expect(nextFocusIndex(0, 1, false)).toBe(0);
  expect(nextFocusIndex(0, 1, true)).toBe(0);
  expect(nextFocusIndex(-1, 0, false)).toBe(-1);
  expect(nextFocusIndex(0, 0, true)).toBe(-1);
});

test("a candidate is tabbable only when enabled, non-negative and rendered", () => {
  const ok = { disabled: false, tabIndex: 0, rendered: true };
  expect(isTabbable(ok)).toBe(true);
  expect(isTabbable({ ...ok, disabled: true })).toBe(false);
  expect(isTabbable({ ...ok, tabIndex: -1 })).toBe(false);
  expect(isTabbable({ ...ok, rendered: false })).toBe(false);
});

test("hidden candidates are dropped so the cycle never lands on them", () => {
  const candidates = [
    { name: "close", disabled: false, tabIndex: 0, rendered: true },
    { name: "hidden-section", disabled: false, tabIndex: 0, rendered: false },
    { name: "save", disabled: false, tabIndex: 0, rendered: true },
    { name: "off", disabled: true, tabIndex: 0, rendered: true },
  ];
  const tabbable = candidates.filter(isTabbable).map((c) => c.name);
  expect(tabbable).toEqual(["close", "save"]);
  // Tab from "close" lands on "save", not on the hidden element between them.
  expect(tabbable[nextFocusIndex(0, tabbable.length, false)]).toBe("save");
  expect(tabbable[nextFocusIndex(1, tabbable.length, false)]).toBe("close");
});
