import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { filterSections, parseScope, type PaletteItem, type PaletteSection } from "../lib/palette";

/** Rows rendered per pane. Filtering is cheap; the DOM is what costs. */
const PANE_CAP = 60;

/**
 * CommandPalette: a two-pane Cmd/Ctrl+P overlay. The left pane holds commands;
 * the right pane holds threads and projects, or the composer's features when
 * the palette opened from the composer. Arrow keys walk every visible row in
 * reading order; Enter runs, Esc or Cmd/Ctrl+P closes. Mounted only while open,
 * so each open starts with an empty query.
 */
export function CommandPalette({
  left,
  right,
  rightTitle,
  onRun,
  onClose,
}: {
  left: PaletteSection[];
  right: PaletteSection[];
  rightTitle: string;
  onRun: (item: PaletteItem) => void;
  onClose: () => void;
}) {
  const [query, setQuery] = useState("");
  const [sel, setSel] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);

  const view = useMemo(() => {
    const { scope, text } = parseScope(query);
    const l = filterSections(left, scope, text, PANE_CAP);
    const r = filterSections(right, scope, text, PANE_CAP);
    const rows: PaletteItem[] = [];
    for (const s of l.sections) rows.push(...s.items);
    const leftCount = rows.length;
    for (const s of r.sections) rows.push(...s.items);
    return { l, r, rows, leftCount, hidden: l.hidden + r.hidden };
  }, [query, left, right]);

  const n = view.rows.length;
  const cur = n === 0 ? -1 : Math.min(sel, n - 1);

  useEffect(() => {
    listRef.current?.querySelector(".palette-item.on")?.scrollIntoView({ block: "nearest" });
  }, [cur]);

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "p") {
      e.preventDefault();
      onClose();
      return;
    }
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      onClose();
      return;
    }
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (n === 0) return;
      setSel((cur + (e.key === "ArrowDown" ? 1 : -1) + n) % n);
      return;
    }
    if (e.key === "Enter" && cur >= 0 && !(e.nativeEvent.isComposing || e.keyCode === 229)) {
      e.preventDefault();
      onRun(view.rows[cur]);
    }
  };

  const renderSections = (sections: PaletteSection[], offset: number) => {
    let index = offset;
    return sections.map((s) => (
      <div className="palette-group" key={s.title}>
        <div className="palette-group-title">{s.title}</div>
        {s.items.map((item) => {
          const i = index++;
          return (
            <button
              key={item.id}
              id={`palette-opt-${i}`}
              type="button"
              role="option"
              aria-selected={i === cur}
              className={`palette-item${i === cur ? " on" : ""}`}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => onRun(item)}
            >
              <span className="palette-label">{item.label}</span>
              {item.detail && <span className="palette-detail">{item.detail}</span>}
              {item.hint && <span className="ctx-hint">{item.hint}</span>}
            </button>
          );
        })}
      </div>
    ));
  };

  return (
    <div className="overlay palette-overlay" onMouseDown={onClose}>
      <div className="palette" role="dialog" aria-label="Command palette" onMouseDown={(e) => e.stopPropagation()}>
        <input
          className="palette-input"
          autoFocus
          placeholder="Search threads, projects and commands  ( > commands   @ threads & projects )"
          value={query}
          role="combobox"
          aria-expanded={n > 0}
          aria-controls="palette-list"
          aria-activedescendant={cur >= 0 ? `palette-opt-${cur}` : undefined}
          onChange={(e) => {
            setQuery(e.target.value);
            setSel(0);
          }}
          onKeyDown={onKeyDown}
        />
        <div className="palette-body" id="palette-list" role="listbox" aria-label="Results" ref={listRef}>
          <div className="palette-pane">
            {renderSections(view.l.sections, 0)}
            {n === 0 && <div className="empty palette-empty">No matches</div>}
          </div>
          <div className="palette-pane">
            <div className="palette-pane-title">{rightTitle}</div>
            {renderSections(view.r.sections, view.leftCount)}
          </div>
        </div>
        <div className="palette-foot">
          {view.hidden > 0 && <span>{view.hidden} more; keep typing to narrow</span>}
          <span className="palette-keys">↑↓ move · Enter run · Esc close</span>
        </div>
      </div>
    </div>
  );
}
