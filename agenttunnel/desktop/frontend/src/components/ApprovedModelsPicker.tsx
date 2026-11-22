import { useMemo, useState } from "react";
import { fuzzyScore } from "../lib/fuzzy";

/** Rows rendered at once; a long discovery list stays cheap to draw. */
const VISIBLE_LIMIT = 200;

/** One selectable model under one harness. `saved` marks an approved model the daemon no longer lists. */
type Entry = { model: string; harness: string; saved: boolean };

/**
 * Approved models for auto mode, per harness: a searchable multi-select over the
 * models each harness reports. The same model name under two harnesses is two
 * separate approvals. Already-approved models a harness no longer lists stay
 * visible as "saved" so saving cannot silently drop them.
 */
export function ApprovedModelsPicker({
  value,
  onChange,
  models,
  harnessOrder,
}: {
  value: Record<string, string[]>;
  onChange: (next: Record<string, string[]>) => void;
  models: Record<string, string[]> | null;
  harnessOrder: string[];
}) {
  const [query, setQuery] = useState("");

  const approved = useMemo(() => {
    const out = new Map<string, Set<string>>();
    for (const [harness, list] of Object.entries(value)) out.set(harness, new Set(list));
    return out;
  }, [value]);

  const total = useMemo(
    () => Object.values(value).reduce((n, list) => n + list.length, 0),
    [value],
  );

  const entries = useMemo(() => {
    const harnesses = [...harnessOrder];
    for (const h of [...Object.keys(models ?? {}), ...Object.keys(value)]) {
      if (!harnesses.includes(h)) harnesses.push(h);
    }
    const out: Entry[] = [];
    for (const harness of harnesses) {
      const listed = models?.[harness] ?? [];
      const seen = new Set<string>();
      for (const model of listed) {
        if (seen.has(model)) continue;
        seen.add(model);
        out.push({ model, harness, saved: false });
      }
      for (const model of value[harness] ?? []) {
        if (seen.has(model)) continue;
        seen.add(model);
        out.push({ model, harness, saved: true });
      }
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

  const toggle = (harness: string, model: string) => {
    const current = approved.get(harness);
    const list = current?.has(model)
      ? (value[harness] ?? []).filter((m) => m !== model)
      : [...(value[harness] ?? []), model];
    onChange({ ...value, [harness]: list });
  };

  const clearAll = () => {
    const next: Record<string, string[]> = {};
    for (const harness of Object.keys(value)) next[harness] = [];
    onChange(next);
  };

  const row = (entry: Entry) => {
    const on = approved.get(entry.harness)?.has(entry.model) ?? false;
    return (
      <label key={`${entry.harness}/${entry.model}`} className={`amp-row${on ? " on" : ""}`}>
        <input type="checkbox" checked={on} onChange={() => toggle(entry.harness, entry.model)} />
        <span className="amp-model">{entry.model}</span>
        {entry.saved && <span className="amp-harness">saved — not in model list</span>}
        {!grouped && !entry.saved && <span className="amp-harness">{entry.harness}</span>}
      </label>
    );
  };

  return (
    <div className="amp">
      <div className="amp-head">
        <span className="amp-count">{total} approved</span>
        {total > 0 && (
          <button type="button" className="amp-clear" onClick={clearAll}>
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
          No models yet. Add a model from a harness picker in Settings; it appears here.
        </p>
      ) : matches.length === 0 ? (
        <p className="hint amp-empty">No models match “{query.trim()}”.</p>
      ) : (
        <div className="amp-list" role="group" aria-label="Approved models">
          {grouped
            ? groups.map((group) => (
                <div key={group.harness} className="amp-group">
                  <div className="amp-group-head">{group.harness}</div>
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
