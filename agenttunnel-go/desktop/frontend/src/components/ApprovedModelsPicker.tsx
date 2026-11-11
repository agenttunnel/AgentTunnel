import { useMemo, useState } from "react";
import { fuzzyScore } from "../lib/fuzzy";

/** Rows rendered at once; a long discovery list stays cheap to draw. */
const VISIBLE_LIMIT = 200;

/** One selectable model. `saved` marks an approved model the daemon no longer lists. */
type Entry = { model: string; harness: string; saved: boolean };

/**
 * Approved models for auto mode: a searchable multi-select over the models the
 * daemon reports. The stored value stays a flat list of model names, so harness
 * labels are only for grouping; already-approved models the model list does not
 * know stay visible under "saved" so saving cannot silently drop them.
 */
export function ApprovedModelsPicker({
  value,
  onChange,
  models,
  harnessOrder,
}: {
  value: string[];
  onChange: (next: string[]) => void;
  models: Record<string, string[]> | null;
  harnessOrder: string[];
}) {
  const [query, setQuery] = useState("");

  const approved = useMemo(() => new Set(value), [value]);

  const entries = useMemo(() => {
    const seen = new Set<string>();
    const out: Entry[] = [];
    const order = harnessOrder.length > 0 ? harnessOrder : Object.keys(models ?? {});
    for (const harness of order) {
      for (const model of models?.[harness] ?? []) {
        if (seen.has(model)) continue;
        seen.add(model);
        out.push({ model, harness, saved: false });
      }
    }
    for (const model of value) {
      if (seen.has(model)) continue;
      seen.add(model);
      out.push({ model, harness: "saved", saved: true });
    }
    return out;
  }, [value, models, harnessOrder]);

  const matches = useMemo(() => {
    const q = query.trim();
    if (!q) return entries;
    const scored: { entry: Entry; score: number }[] = [];
    for (const entry of entries) {
      const score = fuzzyScore(q, entry.model);
      if (score !== null) scored.push({ entry, score });
    }
    scored.sort((a, b) => b.score - a.score || a.entry.model.localeCompare(b.entry.model));
    return scored.map((r) => r.entry);
  }, [entries, query]);

  const shown = matches.slice(0, VISIBLE_LIMIT);
  const grouped = query.trim() === "";
  const groups = useMemo(() => {
    if (!grouped) return [];
    const out: { harness: string; entries: Entry[] }[] = [];
    for (const entry of shown) {
      const last = out[out.length - 1];
      if (last && last.harness === entry.harness) last.entries.push(entry);
      else out.push({ harness: entry.harness, entries: [entry] });
    }
    return out;
  }, [grouped, shown]);

  const toggle = (model: string) => {
    if (approved.has(model)) onChange(value.filter((m) => m !== model));
    else onChange([...value, model]);
  };

  const row = (entry: Entry) => {
    const on = approved.has(entry.model);
    return (
      <label key={entry.model} className={`amp-row${on ? " on" : ""}`}>
        <input type="checkbox" checked={on} onChange={() => toggle(entry.model)} />
        <span className="amp-model">{entry.model}</span>
        {!grouped && <span className="amp-harness">{entry.harness}</span>}
      </label>
    );
  };

  return (
    <div className="amp">
      <div className="amp-head">
        <span className="amp-count">{value.length} approved</span>
        {value.length > 0 && (
          <button type="button" className="amp-clear" onClick={() => onChange([])}>
            Clear
          </button>
        )}
      </div>
      <input
        className="amp-search"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder="Search models…"
        aria-label="Search approved models"
      />
      {entries.length === 0 ? (
        <p className="hint amp-empty">
          No models discovered yet. Models added in the Models panel appear here.
        </p>
      ) : matches.length === 0 ? (
        <p className="hint amp-empty">No models match “{query.trim()}”.</p>
      ) : (
        <div className="amp-list" role="group" aria-label="Approved models">
          {grouped
            ? groups.map((group) => (
                <div key={group.harness} className="amp-group">
                  <div className="amp-group-head">
                    {group.harness === "saved" ? "Saved — not in model list" : group.harness}
                  </div>
                  {group.entries.map(row)}
                </div>
              ))
            : shown.map(row)}
        </div>
      )}
      {matches.length > shown.length && (
        <p className="hint amp-more">
          Showing {shown.length} of {matches.length}. Refine the search to see more.
        </p>
      )}
    </div>
  );
}
