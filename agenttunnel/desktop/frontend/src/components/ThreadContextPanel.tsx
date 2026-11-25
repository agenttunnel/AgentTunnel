import { memo, useEffect, useState } from "react";
import type { Rpc } from "../lib/rpc";

/** One entry of the thread's shared notebook, in the order the orchestrator reads it. */
type ContextEntry = { index: number; author: string; at: string; value: string };
type ContextKey = { key: string; entries: ContextEntry[] };

/**
 * The Context aside: the thread's shared notebook, which the orchestrator, the
 * context agent, and subagents read on every dispatch. Each entry can be edited
 * or removed; an edit is attributed to the user and takes effect on the next
 * dispatch.
 *
 * It reloads when the thread's shared state changes and when a turn starts or
 * ends. Notes are appended without a shared-state event, so the Refresh button
 * covers the rest. The parent keys this panel by thread, so a switch starts clean.
 */
export const ThreadContextPanel = memo(function ThreadContextPanel({
  rpcRef,
  activeId,
  busy,
  shared,
  onError: setError,
}: {
  rpcRef: { readonly current: Rpc | null };
  activeId: string;
  busy: boolean;
  /** Identity changes whenever the thread's shared context changes. */
  shared: unknown;
  onError: (message: string) => void;
}) {
  const [keys, setKeys] = useState<ContextKey[] | null>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    let live = true;
    rpc
      .call<{ keys: ContextKey[] }>("thread.context.get", { threadId: activeId })
      .then((view) => {
        if (live) setKeys(view.keys ?? []);
      })
      .catch((e: unknown) => {
        if (live) setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      live = false;
    };
  }, [rpcRef, activeId, busy, shared, reload, setError]);

  const saveEntry = async (key: string, index: number, value: string) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      await rpc.call("thread.context.entry.save", { threadId: activeId, key, index, value });
      setReload((n) => n + 1);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const deleteEntry = async (key: string, index: number) => {
    const rpc = rpcRef.current;
    if (!rpc) return;
    try {
      await rpc.call("thread.context.entry.delete", { threadId: activeId, key, index });
      setReload((n) => n + 1);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <div className="section context-section">
      <div className="cm-row">
        <h3>Context</h3>
        <button className="cm-add" onClick={() => setReload((n) => n + 1)}>
          Refresh
        </button>
      </div>
      <p className="hint">
        What this thread&apos;s orchestrator, context agent, and subagents read on every dispatch.
        Edit or remove any entry.
      </p>
      {keys === null ? (
        <p className="hint">Loading…</p>
      ) : keys.length === 0 ? (
        <p className="hint">Nothing recorded yet. Agents write here as the thread runs.</p>
      ) : (
        keys.map((k) => (
          <div className="inh-block" key={k.key}>
            <span className="field-label">{k.key}</span>
            {k.entries.map((entry) => (
              <div className="inh-rule" key={`${entry.index}:${entry.at}`}>
                <div className="cm-row">
                  <span className="ctx-meta">
                    {entry.author} · {new Date(entry.at).toLocaleString()}
                  </span>
                  <button
                    className="cm-remove"
                    title="Remove this entry"
                    onClick={() => deleteEntry(k.key, entry.index)}
                  >
                    ×
                  </button>
                </div>
                <textarea
                  className="inh-edit"
                  rows={Math.min(8, Math.max(2, entry.value.split("\n").length))}
                  defaultValue={entry.value}
                  onBlur={(e) => {
                    const next = e.target.value;
                    if (next === entry.value) return;
                    if (!next.trim()) {
                      e.target.value = entry.value;
                      setError("An entry can't be empty. Use × to remove it.");
                      return;
                    }
                    saveEntry(k.key, entry.index, next);
                  }}
                />
              </div>
            ))}
          </div>
        ))
      )}
    </div>
  );
});
