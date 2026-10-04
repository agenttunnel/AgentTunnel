import { describe, expect, it } from "vite-plus/test";

import {
  DRAFT_THREAD,
  HISTORY_LIMIT,
  entriesFor,
  historyPath,
  parseHistory,
  remember,
  renameThread,
  serializeHistory,
} from "./history.ts";

describe("historyPath", () => {
  it("sits beside the settings, not on top of them", () => {
    expect(historyPath({ XDG_CONFIG_HOME: "/tmp/config", HOME: "/home/xpn" })).toBe(
      "/tmp/config/agenttunnel/history.json",
    );
    expect(historyPath({ HOME: "/home/xpn" })).toBe("/home/xpn/.config/agenttunnel/history.json");
  });
});

describe("parseHistory", () => {
  it("keeps the prompts each thread was sent", () => {
    expect(parseHistory('{"t1":["one","two"],"t2":["three"]}')).toEqual({
      t1: ["one", "two"],
      t2: ["three"],
    });
  });

  it("drops anything that is not a usable prompt", () => {
    expect(parseHistory("{ not json")).toEqual({});
    expect(parseHistory("null")).toEqual({});
    expect(parseHistory('["flat","legacy"]')).toEqual({});
    expect(parseHistory('{"t1":"nope","t2":["ok", 3, null, "   ", "  padded  "]}')).toEqual({
      t2: ["ok", "padded"],
    });
  });

  it("never hands back more than the cap, per thread", () => {
    const many = JSON.stringify({
      t1: Array.from({ length: HISTORY_LIMIT + 20 }, (_, i) => `p${i}`),
    });
    const entries = parseHistory(many).t1 ?? [];
    expect(entries).toHaveLength(HISTORY_LIMIT);
    // The newest survive; the oldest fall off the front.
    expect(entries.at(0)).toBe("p20");
    expect(entries.at(-1)).toBe(`p${HISTORY_LIMIT + 19}`);
  });
});

describe("entriesFor", () => {
  it("answers for the thread asked about, and nothing for the others", () => {
    const map = { t1: ["a"], t2: ["b"] };
    expect(entriesFor(map, "t1")).toEqual(["a"]);
    expect(entriesFor(map, "t2")).toEqual(["b"]);
    expect(entriesFor(map, "unknown")).toEqual([]);
    // A thread that does not exist yet keeps its own bucket.
    expect(entriesFor(map, null)).toEqual([]);
  });
});

describe("remember", () => {
  it("files the prompt under the thread that was sent it", () => {
    expect(remember({}, "t1", "hello")).toEqual({ t1: ["hello"] });
    expect(remember({ t1: ["a"] }, "t2", "b")).toEqual({ t1: ["a"], t2: ["b"] });
  });

  it("keeps a brand-new thread's first prompt in the draft bucket", () => {
    expect(remember({}, null, "start me off")).toEqual({ [DRAFT_THREAD]: ["start me off"] });
  });

  it("does not record the same prompt twice in a row in one thread", () => {
    expect(remember({ t1: ["a"] }, "t1", "a")).toEqual({ t1: ["a"] });
    // ...but the same prompt in another thread is its own history.
    expect(remember({ t1: ["a"] }, "t2", "a")).toEqual({ t1: ["a"], t2: ["a"] });
  });

  it("ignores blank input and trims what it keeps", () => {
    expect(remember({}, "t1", "   ")).toEqual({});
    expect(remember({}, "t1", "  hey  ")).toEqual({ t1: ["hey"] });
  });

  it("caps one thread's log without touching another's", () => {
    let map = {};
    for (let index = 0; index < HISTORY_LIMIT + 10; index += 1) {
      map = remember(map, "t1", `p${index}`);
    }
    map = remember(map, "t2", "kept");
    expect(entriesFor(map, "t1")).toHaveLength(HISTORY_LIMIT);
    expect(entriesFor(map, "t1").at(-1)).toBe(`p${HISTORY_LIMIT + 9}`);
    expect(entriesFor(map, "t2")).toEqual(["kept"]);
  });
});

describe("renameThread", () => {
  it("moves the draft prompt onto the thread that was created", () => {
    const map = renameThread({ [DRAFT_THREAD]: ["start me off"] }, DRAFT_THREAD, "tui-1");
    expect(map).toEqual({ "tui-1": ["start me off"] });
    expect(entriesFor(map, DRAFT_THREAD)).toEqual([]);
  });

  it("keeps whatever the target thread already had, newest last", () => {
    const map = renameThread({ "": ["draft"], "tui-1": ["earlier"] }, "", "tui-1");
    expect(entriesFor(map, "tui-1")).toEqual(["earlier", "draft"]);
  });

  it("is a no-op when there is nothing to move", () => {
    const map = { "tui-1": ["a"] };
    expect(renameThread(map, "", "tui-2")).toBe(map);
    expect(renameThread(map, "tui-1", "tui-1")).toBe(map);
  });
});

describe("serializeHistory", () => {
  it("round-trips, with stable key order and a trailing newline", () => {
    const map = { t2: ["b"], t1: ["a", "a 'quoted' one"] };
    const text = serializeHistory(map);
    expect(text.endsWith("\n")).toBe(true);
    expect(text.indexOf('"t1"')).toBeLessThan(text.indexOf('"t2"'));
    expect(parseHistory(text)).toEqual({ t1: ["a", "a 'quoted' one"], t2: ["b"] });
  });
});
