import { memo, useCallback, useEffect, useState } from "react";
import type { Rpc } from "../lib/rpc";

/** The environment's cross-project memory: guide, learnings log, rules. */
type InheritedView = {
  dir: string;
  guide: string;
  log: string;
  rules: { name: string; body: string }[] | null;
  rendered: string;
};

// The last view loaded, so reopening the panel shows it while it refreshes.
let lastView: InheritedView | null = null;

/**
 * The Inherited aside panel. Its drafts are local state, so typing in the
 * guide or log re-renders this panel and not App. It loads when opened, and
 * again on thread changes since agents can record learnings mid-turn.
 */
export const InheritedPanel = memo(function InheritedPanel({
  rpcRef,
  activeId,
  onError: setError,
}: {
  rpcRef: { readonly current: Rpc | null };
  activeId: string | null;
  onError: (message: string) => void;
}) {
  // Inherited context panel state: the environment's shared memory.
  const [inherited, setInherited] = useState<InheritedView | null>(lastView);
  const [inheritedGuide, setInheritedGuide] = useState(lastView?.guide ?? "");
  const [inheritedLog, setInheritedLog] = useState(lastView?.log ?? "");
  const [inheritedDirty, setInheritedDirty] = useState(false);
  const [inheritedRules, setInheritedRules] = useState<{ name: string; body: string }[]>(
    lastView?.rules ?? [],
  );
  const [newRuleName, setNewRuleName] = useState("");

  /** Loads the inherited-context panel: guide, log, and rules. */
  const loadInherited = useCallback(async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      const view = await rpc.call<InheritedView>("context.inherited.get");
      lastView = view;
      setInherited(view);
      setInheritedGuide(view.guide);
      setInheritedLog(view.log);
      setInheritedRules(view.rules ?? []);
      setInheritedDirty(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    loadInherited();
  }, [activeId, loadInherited]);

  /** Saves the guide and log drafts. */
  const saveInherited = async () => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      await rpc.call("context.inherited.save", {
        guide: inheritedGuide,
        log: inheritedLog,
      });
      setInheritedDirty(false);
      await loadInherited();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const saveInheritedRule = async (name: string, body: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      await rpc.call("context.inherited.rule.save", { name, body });
      await loadInherited();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const deleteInheritedRule = async (name: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      await rpc.call("context.inherited.rule.delete", { name });
      await loadInherited();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <div className="section inherited-section">
      <h3>Inherited context</h3>
      <p className="hint">
        Cross-project memory injected into every agent prompt. Agents append learnings with{" "}
        <code>tool learn</code>; everything here is yours to edit.
      </p>
      {!inherited ? (
        <p className="hint">Loading…</p>
      ) : (
        <>
          <div className="inh-block">
            <span className="field-label">GUIDE.md — standing guidance</span>
            <textarea
              className="inh-edit"
              rows={6}
              value={inheritedGuide}
              onChange={(e) => {
                setInheritedGuide(e.target.value);
                setInheritedDirty(true);
              }}
            />
          </div>
          <div className="inh-block">
            <span className="field-label">LOG.md — learnings log</span>
            <textarea
              className="inh-edit"
              rows={6}
              value={inheritedLog}
              onChange={(e) => {
                setInheritedLog(e.target.value);
                setInheritedDirty(true);
              }}
            />
          </div>
          <button className="primary" disabled={!inheritedDirty} onClick={saveInherited}>
            Save guide &amp; log
          </button>
          <div className="inh-block" style={{ marginTop: 14 }}>
            <span className="field-label">Rules (rules/*.md)</span>
            {inheritedRules.map((rule) => (
              <div className="inh-rule" key={rule.name}>
                <div className="cm-row">
                  <span className="inh-rule-name">{rule.name}</span>
                  <button
                    className="cm-remove"
                    title="Delete this rule"
                    onClick={() => deleteInheritedRule(rule.name)}
                  >
                    ×
                  </button>
                </div>
                <textarea
                  className="inh-edit"
                  rows={4}
                  defaultValue={rule.body}
                  onBlur={(e) => {
                    if (e.target.value !== rule.body) {
                      saveInheritedRule(rule.name, e.target.value);
                    }
                  }}
                />
              </div>
            ))}
            <div className="cm-row">
              <input
                className="cm-name"
                placeholder="new rule name"
                value={newRuleName}
                onChange={(e) => setNewRuleName(e.target.value)}
              />
              <button
                className="cm-add"
                disabled={!newRuleName.trim()}
                onClick={() => {
                  if (!newRuleName.trim()) return;
                  saveInheritedRule(newRuleName.trim(), "").then(() => setNewRuleName(""));
                }}
              >
                + Add rule
              </button>
            </div>
          </div>
        </>
      )}
    </div>
  );
});
