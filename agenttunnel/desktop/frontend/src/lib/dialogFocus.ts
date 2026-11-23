// Focus handling for modal dialogs: move focus into the dialog on open, keep
// Tab inside it while open, and hand focus back to the opener on close.

import { useEffect, useRef, type RefObject } from "react";

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export type FocusCandidate = { disabled: boolean; tabIndex: number; rendered: boolean };

/**
 * Whether the Tab trap may land on a candidate. A candidate that cannot take
 * focus (disabled, negative tabindex, display:none, visibility:hidden, inert)
 * makes `focus()` a silent no-op, which would strand the user on one element.
 */
export function isTabbable(c: FocusCandidate): boolean {
  return !c.disabled && c.tabIndex >= 0 && c.rendered;
}

function isRendered(el: HTMLElement): boolean {
  // getClientRects is empty for display:none and detached nodes. It is used
  // instead of offsetParent, which is null for position:fixed elements.
  if (el.getClientRects().length === 0) return false;
  if (getComputedStyle(el).visibility === "hidden") return false;
  return el.closest("[inert]") === null;
}

function tabbableWithin(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter((el) =>
    isTabbable({ disabled: el.matches(":disabled"), tabIndex: el.tabIndex, rendered: isRendered(el) }),
  );
}

/**
 * Index to focus next within a list of `count` focusable elements, wrapping at
 * both ends. `current` is -1 when focus is outside the list, so Tab lands on the
 * first element and Shift+Tab on the last. Returns -1 when the list is empty.
 */
export function nextFocusIndex(current: number, count: number, backwards: boolean): number {
  if (count <= 0) return -1;
  if (current < 0 || current >= count) return backwards ? count - 1 : 0;
  return (current + (backwards ? -1 : 1) + count) % count;
}

/**
 * Returns a ref for the dialog's box. While `open`, focus moves into the box
 * unless a child already took it (an autoFocus input), Tab and Shift+Tab wrap
 * inside the box, and closing restores focus to the element that held it when
 * the dialog rendered.
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
    const onKey = (e: KeyboardEvent) => {
      // A shortcut that already handled Tab (SuperView's cycle) keeps it.
      if (e.key !== "Tab" || !el || e.defaultPrevented) return;
      // Stacked dialogs each hold a listener; only the one owning focus acts.
      const active = document.activeElement;
      if (active && active !== document.body && !el.contains(active)) return;
      const items = tabbableWithin(el);
      e.preventDefault();
      if (items.length === 0) {
        el.focus({ preventScroll: true });
        return;
      }
      const next = nextFocusIndex(items.indexOf(active as HTMLElement), items.length, e.shiftKey);
      items[next]?.focus({ preventScroll: true });
    };
    document.addEventListener("keydown", onKey);
    const back = opener.current;
    return () => {
      document.removeEventListener("keydown", onKey);
      if (back?.isConnected) back.focus({ preventScroll: true });
    };
  }, [open]);
  return box;
}
