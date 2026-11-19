import { useEffect, useId, useRef, useState, type ReactNode } from "react";

/**
 * Popover: a trigger button and a small panel anchored to it. The panel's
 * content mounts only while open, so anything inside that polls stops when it
 * closes. Outside click and Escape close it; focus returns to the trigger.
 */
export function Popover({
  trigger,
  title,
  label,
  className = "",
  placement = "below",
  align = "end",
  children,
}: {
  trigger: ReactNode;
  /** Tooltip on the trigger. */
  title: string;
  /** Accessible name of the panel. */
  label: string;
  className?: string;
  placement?: "below" | "above";
  align?: "start" | "end";
  children: ReactNode | ((close: () => void) => ReactNode);
}) {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  const btnRef = useRef<HTMLButtonElement>(null);
  const id = useId();

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.preventDefault();
      setOpen(false);
      btnRef.current?.focus();
    };
    window.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const close = () => setOpen(false);
  return (
    <div className={`popover-wrap ${className}`} ref={wrapRef}>
      <button
        ref={btnRef}
        type="button"
        className={`popover-trigger${open ? " on" : ""}`}
        title={title}
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        onClick={() => setOpen((v) => !v)}
      >
        {trigger}
      </button>
      {open && (
        <div id={id} className={`popover-panel ${placement} ${align}`} role="dialog" aria-label={label}>
          {typeof children === "function" ? children(close) : children}
        </div>
      )}
    </div>
  );
}
