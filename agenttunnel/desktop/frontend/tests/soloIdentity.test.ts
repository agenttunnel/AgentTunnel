// The solo identity hue is a pure function of the thread id.
// Run with: npm test (bun test tests, no extra dependencies).
import { expect, test } from "bun:test";
import { soloHue } from "../src/lib/soloIdentity";

test("the same thread id always gets the same hue", () => {
  expect(soloHue("thread-abc")).toBe(soloHue("thread-abc"));
});

test("a hue is one of the six harness classes", () => {
  for (let i = 0; i < 200; i++) {
    expect(soloHue(`thread-${i}`)).toMatch(/^solo-hue-[0-5]$/);
  }
});

test("ids spread across all six hues", () => {
  const seen = new Set<string>();
  for (let i = 0; i < 200; i++) seen.add(soloHue(`0a1b2c-${i}`));
  expect(seen.size).toBe(6);
});
