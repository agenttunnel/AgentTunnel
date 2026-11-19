import type { DrawerSide } from "../lib/useDrawers";

/**
 * DrawerToggle opens a side panel that is a slide-over drawer at the current
 * width. It renders always and CSS shows it only while that side is a drawer,
 * so resizing never remounts anything.
 */
export function DrawerToggle({
  side,
  open,
  onToggle,
}: {
  side: DrawerSide;
  open: boolean;
  onToggle: (side: DrawerSide) => void;
}) {
  const label = side === "sidebar" ? "Threads" : "Thread details";
  return (
    <button
      type="button"
      className={`drawer-toggle drawer-toggle-${side === "sidebar" ? "left" : "right"}`}
      aria-label={open ? `Close ${label.toLowerCase()}` : `Open ${label.toLowerCase()}`}
      aria-expanded={open}
      title={label}
      onClick={() => onToggle(side)}
    >
      <svg
        viewBox="0 0 24 24"
        width="16"
        height="16"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <rect x="3" y="4" width="18" height="16" rx="2.5" />
        {side === "sidebar" ? <line x1="9" y1="4" x2="9" y2="20" /> : <line x1="15" y1="4" x2="15" y2="20" />}
      </svg>
    </button>
  );
}
