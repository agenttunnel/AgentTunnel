import { describe, expect, it } from "vite-plus/test";

import { createPainter } from "./paint.ts";

const BEGIN_SYNC = "\x1b[?2026h";
const END_SYNC = "\x1b[?2026l";
const ERASE_LINE = "\x1b[2K";

const createHarness = (rows = 3, cols = 5) => {
  const writes: string[] = [];
  const resizes = new Set<() => void>();
  let geometry = { rows, cols };
  const painter = createPainter({
    write: (chunk) => {
      writes.push(chunk);
    },
    size: () => geometry,
    onResize: (handler) => {
      resizes.add(handler);
      return () => {
        resizes.delete(handler);
      };
    },
  });
  return {
    painter,
    writes,
    output: () => writes.join(""),
    clear: () => {
      writes.length = 0;
    },
    setSize: (nextRows: number, nextCols: number) => {
      geometry = { rows: nextRows, cols: nextCols };
    },
    resize: () => {
      for (const handler of resizes) handler();
    },
    resizeHandlers: resizes,
  };
};

describe("createPainter", () => {
  it("writes every row on the first frame, inside one synchronized update", () => {
    const harness = createHarness(3, 5);

    harness.painter.paint(["a", "bb", "ccc"]);

    expect(harness.writes).toEqual([
      `${BEGIN_SYNC}\x1b[1;1H${ERASE_LINE}a    \x1b[2;1H${ERASE_LINE}bb   \x1b[3;1H${ERASE_LINE}ccc  ${END_SYNC}`,
    ]);
  });

  it("writes only the row that changed on the next frame", () => {
    const harness = createHarness(3, 5);
    harness.painter.paint(["a", "bb", "ccc"]);
    harness.clear();

    harness.painter.paint(["a", "BB", "ccc"]);

    expect(harness.writes).toEqual([`${BEGIN_SYNC}\x1b[2;1H${ERASE_LINE}BB   ${END_SYNC}`]);
  });

  it("writes nothing when the frame is unchanged", () => {
    const harness = createHarness(3, 5);
    harness.painter.paint(["a", "bb", "ccc"]);
    harness.clear();

    harness.painter.paint(["a", "bb", "ccc"]);

    expect(harness.writes).toEqual([]);
  });

  it("blanks the rows a shrinking frame no longer uses", () => {
    const harness = createHarness(3, 5);
    harness.painter.paint(["a", "bb", "ccc"]);
    harness.clear();

    harness.painter.paint(["a"]);

    expect(harness.writes).toEqual([
      `${BEGIN_SYNC}\x1b[2;1H${ERASE_LINE}     \x1b[3;1H${ERASE_LINE}     ${END_SYNC}`,
    ]);
  });

  it("ignores lines past the last row", () => {
    const harness = createHarness(2, 4);

    harness.painter.paint(["a", "b", "c"]);

    expect(harness.writes).toEqual([
      `${BEGIN_SYNC}\x1b[1;1H${ERASE_LINE}a   \x1b[2;1H${ERASE_LINE}b   ${END_SYNC}`,
    ]);
    expect(harness.output()).not.toContain("c");
  });

  it("truncates an oversized line to the terminal width instead of wrapping", () => {
    const harness = createHarness(1, 5);

    harness.painter.paint(["abcdefgh"]);

    expect(harness.writes).toEqual([`${BEGIN_SYNC}\x1b[1;1H${ERASE_LINE}abcd…${END_SYNC}`]);
  });

  it("keeps SGR in a line that fits and pads by visible width", () => {
    const harness = createHarness(1, 6);

    harness.painter.paint(["\x1b[31mred\x1b[0m"]);

    expect(harness.writes).toEqual([
      `${BEGIN_SYNC}\x1b[1;1H${ERASE_LINE}\x1b[31mred\x1b[0m   ${END_SYNC}`,
    ]);
  });

  it("enters and leaves the terminal once, no matter how often it is asked", () => {
    const harness = createHarness();

    harness.painter.enter();
    harness.painter.enter();
    harness.painter.exit();
    harness.painter.exit();

    expect(harness.writes).toEqual([
      "\x1b[?1049h\x1b[?25l\x1b[?2004h",
      "\x1b[?2004l\x1b[?25h\x1b[?1049l\x1b[0m",
    ]);
  });

  it("leaves the terminal untouched when it never entered", () => {
    const harness = createHarness();

    harness.painter.exit();

    expect(harness.writes).toEqual([]);
  });

  it("repaints every row after a resize notification", () => {
    const harness = createHarness(2, 4);
    harness.painter.paint(["a", "b"]);
    harness.clear();

    harness.resize();
    harness.painter.paint(["a", "b"]);

    expect(harness.output()).toBe(
      `${BEGIN_SYNC}\x1b[1;1H${ERASE_LINE}a   \x1b[2;1H${ERASE_LINE}b   ${END_SYNC}`,
    );
  });

  it("honours a size change and reports the current geometry", () => {
    const harness = createHarness(2, 4);
    harness.painter.paint(["a", "b"]);
    harness.clear();
    harness.setSize(3, 6);

    expect(harness.painter.size).toEqual({ rows: 3, cols: 6 });
    harness.painter.paint(["a", "b", "c"]);

    expect(harness.output()).toBe(
      `${BEGIN_SYNC}\x1b[1;1H${ERASE_LINE}a     \x1b[2;1H${ERASE_LINE}b     \x1b[3;1H${ERASE_LINE}c     ${END_SYNC}`,
    );
  });

  it("repaints every row after an explicit invalidate", () => {
    const harness = createHarness(2, 4);
    harness.painter.paint(["a", "b"]);
    harness.clear();

    harness.painter.invalidate();
    harness.painter.paint(["a", "b"]);

    expect(harness.output()).toBe(
      `${BEGIN_SYNC}\x1b[1;1H${ERASE_LINE}a   \x1b[2;1H${ERASE_LINE}b   ${END_SYNC}`,
    );
  });
});
