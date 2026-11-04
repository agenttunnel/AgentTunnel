import { useEffect, useLayoutEffect, useRef, useState } from "react";

export type MenuItem = {
  label: string;
  danger?: boolean;
  /** Right-aligned key hint, e.g. the single-letter shortcut in a panel menu. */
  hint?: string;
  onSelect: () => void;
};

/**
 * ContextMenu is a fixed-position menu opened at a right-click point. It
 * clamps itself inside the viewport, closes on outside click, Escape, or
 * window blur, and never lets a right-click inside it pop the native menu.
 */
export function ContextMenu({
  x,
  y,
  items,
  onClose,
}: {
  x: number;
  y: number;
  items: MenuItem[];
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement | null>(null);
  const [pos, setPos] = useState({ left: x, top: y });

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    setPos({
      left: Math.max(8, Math.min(x, window.innerWidth - r.width - 8)),
      top: Math.max(8, Math.min(y, window.innerHeight - r.height - 8)),
    });
  }, [x, y]);

  useEffect(() => {
    const onDown = (e: PointerEvent) => {
      if (ref.current && e.target instanceof Node && ref.current.contains(e.target)) return;
      onClose();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("pointerdown", onDown);
    window.addEventListener("keydown", onKey);
    window.addEventListener("blur", onClose);
    return () => {
      window.removeEventListener("pointerdown", onDown);
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("blur", onClose);
    };
  }, [onClose]);

  return (
    <div className="ctx-menu" style={pos} ref={ref} onContextMenu={(e) => e.preventDefault()}>
      {items.map((item) => (
        <button
          key={item.label}
          className={`ctx-item${item.danger ? " danger" : ""}`}
          onClick={() => {
            onClose();
            item.onSelect();
          }}
        >
          <span>{item.label}</span>
          {item.hint && <span className="ctx-hint">{item.hint}</span>}
        </button>
      ))}
    </div>
  );
}
