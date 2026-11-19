// Scroll positions per key (e.g. "review:<threadId>") for panes that unmount
// when their tab is left, so coming back lands where the user was. Session only.

import { useCallback, type UIEvent } from "react";

const positions = new Map<string, number>();

/** A ref callback that restores the key's position on mount, and an onScroll
 * that records it. */
export function useScrollMemory(key: string) {
  const ref = useCallback(
    (el: HTMLElement | null) => {
      const top = positions.get(key);
      if (el && top) el.scrollTop = top;
    },
    [key],
  );
  const onScroll = useCallback(
    (e: UIEvent<HTMLElement>) => {
      positions.set(key, e.currentTarget.scrollTop);
    },
    [key],
  );
  return { ref, onScroll };
}
