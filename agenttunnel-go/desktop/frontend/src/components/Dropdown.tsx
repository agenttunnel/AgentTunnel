import { useEffect, useMemo, useRef, useState } from "react";

export type DropdownOption = {
  value: string;
  label: string;
  group?: string;
  hint?: string;
};

type Props = {
  value: string;
  options: DropdownOption[];
  onChange: (value: string) => void;
  placeholder?: string;
  searchable?: boolean;
  /** When set, offers an "add" row for a query that matches nothing. */
  onAdd?: (value: string) => void;
  addLabel?: string;
  /** When set, shows a footer with a refresh action. */
  onRefresh?: () => void;
  footNote?: string;
};

/** A styled, optionally searchable dropdown with grouped options and keyboard nav. */
export function Dropdown({
  value,
  options,
  onChange,
  placeholder = "Select…",
  searchable = false,
  onAdd,
  addLabel = "Add",
  onRefresh,
  footNote,
}: Props) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const rootRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return options;
    return options.filter(
      (o) => o.label.toLowerCase().includes(q) || o.value.toLowerCase().includes(q),
    );
  }, [options, query]);

  const groups = useMemo(() => {
    const out: { group: string | null; items: DropdownOption[] }[] = [];
    for (const o of filtered) {
      const g = o.group ?? "";
      const last = out[out.length - 1];
      if (last && (last.group ?? "") === g) last.items.push(o);
      else out.push({ group: o.group ?? null, items: [o] });
    }
    return out;
  }, [filtered]);

  const selected = options.find((o) => o.value === value);
  const exact = filtered.some((o) => o.value === query.trim() || o.label === query.trim());
  const canAdd = searchable && !!onAdd && query.trim() !== "" && !exact;
  const addIndex = filtered.length;

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    setQuery("");
    setActive(0);
    if (searchable) window.setTimeout(() => inputRef.current?.focus(), 0);
  }, [open, searchable]);

  const commit = (v: string) => {
    onChange(v);
    setOpen(false);
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((a) => Math.min(a + 1, canAdd ? addIndex : filtered.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((a) => Math.max(a - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (canAdd && active === addIndex) {
        onAdd!(query.trim());
        setOpen(false);
      } else if (filtered[active]) {
        commit(filtered[active].value);
      }
    } else if (e.key === "Escape") {
      e.preventDefault();
      setOpen(false);
    }
  };

  return (
    <div className="dd" ref={rootRef}>
      <button
        type="button"
        className={`dd-trigger ${open ? "open" : ""}`}
        onClick={() => setOpen((o) => !o)}
      >
        <span className={`dd-label ${selected ? "" : "placeholder"}`}>
          {selected ? selected.label : placeholder}
        </span>
        <svg
          className="dd-chev"
          viewBox="0 0 24 24"
          width="14"
          height="14"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden="true"
        >
          <polyline points="6 9 12 15 18 9" />
        </svg>
      </button>

      {open && (
        <div className="dd-pop" onKeyDown={onKeyDown}>
          {searchable && (
            <div className="dd-search">
              <input
                ref={inputRef}
                value={query}
                placeholder="Search"
                onChange={(e) => {
                  setQuery(e.target.value);
                  setActive(0);
                }}
              />
            </div>
          )}
          <div className="dd-list">
            {groups.map((g, gi) => (
              <div key={gi}>
                {g.group && <div className="dd-group">{g.group}</div>}
                {g.items.map((o) => {
                  const idx = filtered.indexOf(o);
                  const isSel = o.value === value;
                  return (
                    <div
                      key={`${o.group ?? ""}:${o.value}`}
                      className={`dd-item ${idx === active ? "active" : ""} ${isSel ? "sel" : ""}`}
                      onMouseEnter={() => setActive(idx)}
                      onClick={() => commit(o.value)}
                    >
                      <span className="dd-check">{isSel ? "✓" : ""}</span>
                      <span className="dd-item-label">{o.label}</span>
                      {o.hint && <span className="dd-hint">{o.hint}</span>}
                    </div>
                  );
                })}
              </div>
            ))}
            {canAdd && (
              <div
                className={`dd-item add ${active === addIndex ? "active" : ""}`}
                onMouseEnter={() => setActive(addIndex)}
                onClick={() => {
                  onAdd!(query.trim());
                  setOpen(false);
                }}
              >
                <span className="dd-check">＋</span>
                <span className="dd-item-label">
                  {addLabel} “{query.trim()}”
                </span>
              </div>
            )}
            {filtered.length === 0 && !canAdd && <div className="dd-empty">No matches</div>}
          </div>
          {(footNote || onRefresh) && (
            <div className="dd-foot">
              <span className="dd-note">{footNote}</span>
              {onRefresh && (
                <button type="button" onClick={() => onRefresh()}>
                  Refresh
                </button>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
