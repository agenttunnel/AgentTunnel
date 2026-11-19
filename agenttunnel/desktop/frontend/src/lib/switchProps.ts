import type { KeyboardEvent as ReactKeyboardEvent } from "react";

/** Clickable cards and rows are divs, so give them the button contract and
 * leave clicks on nested controls to those controls. */
export function clickable(onActivate: () => void) {
  return {
    role: "button" as const,
    tabIndex: 0,
    onClick: onActivate,
    onKeyDown: (e: ReactKeyboardEvent<HTMLElement>) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        onActivate();
      }
    },
  };
}

/** Same treatment for the pill switches. */
export function switchProps(on: boolean, onToggle: () => void) {
  return {
    role: "switch" as const,
    "aria-checked": on,
    tabIndex: 0,
    onClick: onToggle,
    onKeyDown: (e: ReactKeyboardEvent<HTMLDivElement>) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        onToggle();
      }
    },
  };
}
