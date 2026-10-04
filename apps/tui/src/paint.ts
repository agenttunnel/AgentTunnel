/**
 * The terminal painter: the only module that talks to the screen.
 *
 * It owns three things and nothing else. Terminal state transitions (`enter`
 * takes over the alternate screen, `exit` hands it back untouched), the
 * absolute-addressed frame writes, and the diff between consecutive frames, so
 * a redraw only touches rows whose text actually changed. Components render
 * lines; this module decides how those lines reach the terminal.
 *
 * Frames arrive as plain strings that may already carry SGR. Every line is
 * normalized to exactly the terminal width before it is compared or written, so
 * a stale character from the previous frame can never survive underneath a
 * shorter one.
 */

import { pad, truncate } from "./theme.ts";

export interface PainterOptions {
  /** Sink for escape sequences; injected so tests can capture a frame. */
  readonly write: (chunk: string) => void;
  /** Current terminal geometry, read on every frame rather than cached. */
  readonly size: () => { readonly rows: number; readonly cols: number };
  /** Subscribe to resize notifications; returns the unsubscribe function. */
  readonly onResize: (handler: () => void) => () => void;
}

export interface Painter {
  readonly size: { readonly rows: number; readonly cols: number };
  /** Enter the alternate screen, hide the cursor, enable bracketed paste. */
  enter(): void;
  /** Restore the terminal exactly as it was. */
  exit(): void;
  /** Draw a full frame; only lines that changed since the last frame are written. */
  paint(lines: ReadonlyArray<string>): void;
  /** Forget the previous frame (next paint writes everything). */
  invalidate(): void;
}

const ENTER_ALTERNATE_SCREEN = "\x1b[?1049h";
const LEAVE_ALTERNATE_SCREEN = "\x1b[?1049l";
const HIDE_CURSOR = "\x1b[?25l";
const SHOW_CURSOR = "\x1b[?25h";
const ENABLE_BRACKETED_PASTE = "\x1b[?2004h";
const DISABLE_BRACKETED_PASTE = "\x1b[?2004l";
const RESET_SGR = "\x1b[0m";
const BEGIN_SYNCHRONIZED_UPDATE = "\x1b[?2026h";
const END_SYNCHRONIZED_UPDATE = "\x1b[?2026l";
const ERASE_LINE = "\x1b[2K";

export function createPainter(options: PainterOptions): Painter {
  let entered = false;
  let previous: ReadonlyArray<string> | null = null;

  /** Every row of the frame, cut and padded to the terminal width. */
  const normalize = (lines: ReadonlyArray<string>): string[] => {
    const { rows, cols } = options.size();
    const height = Math.max(0, rows);
    const width = Math.max(0, cols);
    const frame: string[] = [];
    for (let row = 0; row < height; row += 1) {
      const line = lines[row] ?? "";
      frame.push(width === 0 ? "" : pad(truncate(line, width), width));
    }
    return frame;
  };

  const invalidate = (): void => {
    previous = null;
  };

  // A resize can move every row; the next frame must repaint in full.
  options.onResize(invalidate);

  const enter = (): void => {
    if (entered) return;
    entered = true;
    options.write(`${ENTER_ALTERNATE_SCREEN}${HIDE_CURSOR}${ENABLE_BRACKETED_PASTE}`);
  };

  const exit = (): void => {
    if (!entered) return;
    entered = false;
    invalidate();
    options.write(`${DISABLE_BRACKETED_PASTE}${SHOW_CURSOR}${LEAVE_ALTERNATE_SCREEN}${RESET_SGR}`);
  };

  const paint = (lines: ReadonlyArray<string>): void => {
    const frame = normalize(lines);
    const last = previous;
    let update = "";
    for (let row = 0; row < frame.length; row += 1) {
      const line = frame[row] ?? "";
      if (last !== null && last[row] === line) continue;
      update += `\x1b[${row + 1};1H${ERASE_LINE}${line}`;
    }
    previous = frame;
    if (update.length === 0) return;
    options.write(`${BEGIN_SYNCHRONIZED_UPDATE}${update}${END_SYNCHRONIZED_UPDATE}`);
  };

  return {
    get size() {
      return options.size();
    },
    enter,
    exit,
    paint,
    invalidate,
  };
}
