// Escape closes the topmost open overlay. Overlays register while mounted, so a
// dialog opened from a sheet closes first and the sheet stays up behind it.

import { useEffect, useRef } from "react";

type Entry = { close: () => void };
const stack: Entry[] = [];
let listening = false;

function onKeyDown(e: KeyboardEvent) {
  if (e.key !== "Escape" || e.defaultPrevented || stack.length === 0) return;
  e.preventDefault();
  stack[stack.length - 1].close();
}

/** useEscapeClose calls `close` on Escape while this overlay is the topmost one. */
export function useEscapeClose(close: () => void, enabled = true): void {
  const latest = useRef(close);
  latest.current = close;
  useEffect(() => {
    if (!enabled) return;
    if (!listening) {
      window.addEventListener("keydown", onKeyDown);
      listening = true;
    }
    const entry: Entry = { close: () => latest.current() };
    stack.push(entry);
    return () => {
      const i = stack.indexOf(entry);
      if (i >= 0) stack.splice(i, 1);
    };
  }, [enabled]);
}
