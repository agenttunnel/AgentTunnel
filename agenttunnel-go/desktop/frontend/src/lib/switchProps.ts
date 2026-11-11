import type { KeyboardEvent as ReactKeyboardEvent } from "react";

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
