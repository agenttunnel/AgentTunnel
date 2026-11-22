// Focus handling for modal dialogs: move focus into the dialog on open and
// hand it back to the element that opened it on close.

import { useEffect, useRef, type RefObject } from "react";

/**
 * Returns a ref for the dialog's box. While `open`, focus moves into the box
 * unless a child already took it (an autoFocus input), and closing restores
 * focus to the element that held it when the dialog rendered.
 */
export function useDialogFocus<T extends HTMLElement>(open = true): RefObject<T> {
  const box = useRef<T>(null);
  // Read during render: a dialog's autoFocus children take focus before effects run.
  const opener = useRef<HTMLElement | null>(null);
  if (!open) opener.current = null;
  else if (opener.current === null && document.activeElement instanceof HTMLElement) {
    opener.current = document.activeElement;
  }
  useEffect(() => {
    if (!open) return;
    const el = box.current;
    if (el && !el.contains(document.activeElement)) el.focus({ preventScroll: true });
    const back = opener.current;
    return () => {
      if (back?.isConnected) back.focus({ preventScroll: true });
    };
  }, [open]);
  return box;
}
