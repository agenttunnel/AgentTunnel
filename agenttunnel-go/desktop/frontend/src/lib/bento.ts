import { useEffect, useState, type CSSProperties } from "react";

// Tetris-style tiling for the projects and threads screens. Each card picks a
// piece footprint (column/row spans) and a hue from a stable hash of its id,
// so the board looks randomly scattered but never reshuffles between
// renders. The column count tracks the available width and spans clamp to it,
// so pieces always fit the space the grid is given.

/** FNV-1a: tiny, stable hash with enough spread for shape and hue picking. */
function hashId(id: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < id.length; i++) {
    h ^= id.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

// Piece footprints [colSpan, rowSpan] in grid units. Small pieces dominate;
// wide, tall, and square blocks appear regularly so the board reads as
// unequal tiles packed in both directions, not rows of cards.
const SHAPES: readonly (readonly [number, number])[] = [
  [1, 1],
  [1, 1],
  [2, 1],
  [1, 2],
  [2, 2],
  [3, 1],
  [1, 3],
  [2, 3],
];

/** Grid placement + hue for one card. Column spans clamp to the current
 * column count (wide pieces collapse in narrow layouts, never overflow). */
export function bentoTile(id: string, columns: number): CSSProperties {
  const h = hashId(id);
  const [col, row] = SHAPES[h % SHAPES.length];
  return {
    gridColumn: `span ${Math.min(col, columns)}`,
    gridRow: `span ${row}`,
    "--tile-h": String((h >>> 9) % 360),
  } as CSSProperties;
}

/** Measures how many grid columns fit the element's width. The element is
 * tracked as state (callback ref), not a plain ref, because the grids mount
 * long after App does — the app shows a splash while connecting to the
 * daemon, so a mount-time effect would see a null ref and never measure.
 * State-only-on-change keeps a window resize to at most one re-render per
 * column gained or lost. */
export function useBentoColumns(minColumnWidth: number) {
  const [element, setElement] = useState<HTMLDivElement | null>(null);
  const [columns, setColumns] = useState(1);
  useEffect(() => {
    if (!element) return;
    const measure = () => {
      const next = Math.max(1, Math.floor(element.clientWidth / minColumnWidth));
      setColumns((prev) => (prev === next ? prev : next));
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [element, minColumnWidth]);
  return {
    ref: setElement,
    columns,
    /** Column template for the measured count; the class keeps the rest. */
    gridStyle: { gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` } as CSSProperties,
  };
}
