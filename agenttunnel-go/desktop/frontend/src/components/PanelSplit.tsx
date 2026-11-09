import { useRef, useState, type PointerEvent, type KeyboardEvent, type RefObject } from "react";

/** Widths a side panel can be dragged to, in px. The shell also caps each panel
 * with a vw limit in CSS, so the chat column keeps room on narrow windows. */
export const PANEL_BOUNDS = {
  sidebar: { min: 180, max: 420, def: 264 },
  aside: { min: 260, max: 560, def: 360 },
} as const;

export function clampWidth(px: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, Math.round(px)));
}

/** The grid track between two panels. A drag writes the new width straight to
 * the shell's CSS variable, so it costs no React renders; the width is committed
 * to state once, on release. */
export function SplitHandle({
  side,
  width,
  min,
  max,
  shellRef,
  disabled,
  onCommit,
}: {
  side: "left" | "right";
  width: number;
  min: number;
  max: number;
  shellRef: RefObject<HTMLDivElement>;
  disabled?: boolean;
  onCommit: (px: number) => void;
}) {
  const cssVar = side === "left" ? "--sidebar-w" : "--aside-w";
  const [dragging, setDragging] = useState(false);
  const live = useRef(width);

  const apply = (px: number) => {
    live.current = px;
    shellRef.current?.style.setProperty(cssVar, `${px}px`);
  };

  const widthAt = (clientX: number) => {
    const rect = shellRef.current?.getBoundingClientRect();
    if (!rect) return live.current;
    return clampWidth(side === "left" ? clientX - rect.left : rect.right - clientX, min, max);
  };

  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    if (disabled || e.button !== 0) return;
    e.currentTarget.setPointerCapture(e.pointerId);
    live.current = width;
    document.body.classList.add("is-resizing");
    setDragging(true);
  };

  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    if (!dragging) return;
    apply(widthAt(e.clientX));
  };

  const endDrag = () => {
    if (!dragging) return;
    document.body.classList.remove("is-resizing");
    setDragging(false);
    onCommit(live.current);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (disabled) return;
    const step = e.shiftKey ? 48 : 16;
    const grow = side === "left" ? "ArrowRight" : "ArrowLeft";
    const shrink = side === "left" ? "ArrowLeft" : "ArrowRight";
    let next: number;
    if (e.key === grow) next = width + step;
    else if (e.key === shrink) next = width - step;
    else return;
    e.preventDefault();
    const px = clampWidth(next, min, max);
    apply(px);
    onCommit(px);
  };

  const label = side === "left" ? "Resize threads panel" : "Resize details panel";
  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={label}
      aria-valuemin={min}
      aria-valuemax={max}
      aria-valuenow={width}
      aria-disabled={disabled || undefined}
      tabIndex={disabled ? -1 : 0}
      className={`split split-${side}${dragging ? " dragging" : ""}${disabled ? " is-disabled" : ""}`}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endDrag}
      onPointerCancel={endDrag}
      onKeyDown={onKeyDown}
    />
  );
}

/** Folds a side panel into a narrow rail. The chevron points the way the panel
 * will move on click. */
export function PanelToggle({
  side,
  open,
  onToggle,
}: {
  side: "left" | "right";
  open: boolean;
  onToggle: () => void;
}) {
  const name = side === "left" ? "threads" : "details";
  const label = `${open ? "Collapse" : "Expand"} ${name} panel`;
  const pointsLeft = (side === "left") === open;
  return (
    <button
      type="button"
      className="panel-toggle"
      aria-label={label}
      aria-expanded={open}
      title={label}
      onClick={onToggle}
    >
      <svg
        viewBox="0 0 24 24"
        width="14"
        height="14"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
        style={pointsLeft ? undefined : { transform: "rotate(180deg)" }}
      >
        <path d="M15 18l-6-6 6-6" />
      </svg>
    </button>
  );
}
