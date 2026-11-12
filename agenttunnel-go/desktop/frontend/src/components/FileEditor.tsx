import { memo, useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import type { Rpc } from "../lib/rpc";

// Mirrors internal/fileedit: one directory level per fs.list call, and text
// documents only. Listings are lazy, so a tree only ever holds what is expanded.
export type FsEntry = { name: string; path: string; dir: boolean; size?: number };
type FsListing = { entries: FsEntry[]; truncated?: boolean };
type FsDocument = { path: string; text: string; size: number; hash: string };

type DirState = { loading?: boolean; entries?: FsEntry[]; truncated?: boolean; error?: string };
// text is the editor buffer; doc.text is what is on disk as far as we know.
// Dirty means the two differ. rev bumps when the buffer is reset from disk so
// the CodeMirror instance remounts with the new content.
type OpenFile = { doc: FsDocument; text: string; rev: number; saving?: boolean; error?: string };

// more is set on the "show more" row that follows a folder's first PAGE entries.
type Row = {
  key: string;
  depth: number;
  entry?: FsEntry;
  note?: string;
  err?: boolean;
  more?: { dir: string; hidden: number };
};

const ROOT = "";
// PAGE bounds the rows one folder renders. A folder can list up to the server's
// 2000-entry cap; rendering all of them is what makes the tree slow, so the
// rest sit behind a "show more" row.
const PAGE = 200;

function errorText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

function Chevron({ open }: { open: boolean }) {
  return (
    <svg
      className={`tree-chev${open ? " open" : ""}`}
      viewBox="0 0 24 24"
      width="12"
      height="12"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <polyline points="9 6 15 12 9 18" />
    </svg>
  );
}

const FileTree = memo(function FileTree({
  rows,
  expanded,
  selected,
  onToggle,
  onOpen,
  onMore,
}: {
  rows: Row[];
  expanded: Set<string>;
  selected: string | null;
  onToggle: (path: string) => void;
  onOpen: (path: string) => void;
  onMore: (dir: string) => void;
}) {
  return (
    <>
      {rows.map((row) => {
        const pad = { paddingLeft: 8 + row.depth * 14 };
        const e = row.entry;
        if (row.more) {
          const { dir, hidden } = row.more;
          return (
            <div key={row.key} className="tree-row note more" style={pad} role="button" onClick={() => onMore(dir)}>
              <span className="tree-spacer" />
              <span className="tree-name">Show {Math.min(PAGE, hidden)} more ({hidden} hidden)</span>
            </div>
          );
        }
        if (!e) {
          return (
            <div key={row.key} className={`tree-row note${row.err ? " err-text" : ""}`} style={pad}>
              <span className="tree-spacer" />
              <span className="tree-name">{row.note}</span>
            </div>
          );
        }
        if (e.dir) {
          return (
            <div
              key={row.key}
              className="tree-row"
              style={pad}
              role="treeitem"
              aria-expanded={expanded.has(e.path)}
              onClick={() => onToggle(e.path)}
            >
              <Chevron open={expanded.has(e.path)} />
              <span className="tree-name dir">{e.name}</span>
            </div>
          );
        }
        return (
          <div
            key={row.key}
            className={`tree-row${selected === e.path ? " sel" : ""}`}
            style={pad}
            role="treeitem"
            onClick={() => onOpen(e.path)}
          >
            <span className="tree-spacer" />
            <span className="tree-name">{e.name}</span>
          </div>
        );
      })}
    </>
  );
});

/**
 * CodeEditor is a lazily loaded CodeMirror 6 instance. The packages come in
 * through dynamic import so they stay out of the initial bundle. Mounted per
 * file (the parent keys it), so `initial` is read once.
 */
function CodeEditor({
  initial,
  onChange,
  onSave,
}: {
  initial: string;
  onChange: (text: string) => void;
  onSave: () => void;
}) {
  const host = useRef<HTMLDivElement>(null);
  const changeRef = useRef(onChange);
  const saveRef = useRef(onSave);
  changeRef.current = onChange;
  saveRef.current = onSave;
  // The buffer is owned by CodeMirror after mount; `initial` changes on every
  // keystroke, so it must be captured once rather than used as an effect dep.
  const [start] = useState(initial);
  const [fallback, setFallback] = useState(false);

  useEffect(() => {
    let view: { destroy: () => void } | undefined;
    let cancelled = false;
    Promise.all([import("codemirror"), import("@codemirror/view")])
      .then(([cm, cmView]) => {
        if (cancelled || !host.current) return;
        const { EditorView, keymap } = cmView;
        view = new EditorView({
          doc: start,
          parent: host.current,
          extensions: [
            cm.basicSetup,
            EditorView.theme(
              {
                "&": { height: "100%", fontSize: "12px", backgroundColor: "var(--surface-1)", color: "var(--text)" },
                ".cm-scroller": { fontFamily: "var(--mono)", lineHeight: "1.55" },
                ".cm-gutters": { backgroundColor: "var(--surface-1)", color: "var(--faint)", border: "none" },
                ".cm-activeLine": { backgroundColor: "var(--panel)" },
                ".cm-activeLineGutter": { backgroundColor: "transparent" },
                ".cm-cursor": { borderLeftColor: "var(--accent)" },
                "&.cm-focused": { outline: "none" },
                "&.cm-focused .cm-selectionBackground, .cm-selectionBackground": {
                  backgroundColor: "var(--accent-soft)",
                },
              },
              { dark: true },
            ),
            keymap.of([
              {
                key: "Mod-s",
                preventDefault: true,
                run: () => {
                  saveRef.current();
                  return true;
                },
              },
            ]),
            EditorView.updateListener.of((u) => {
              if (u.docChanged) changeRef.current(u.state.doc.toString());
            }),
          ],
        });
      })
      .catch(() => {
        if (!cancelled) setFallback(true);
      });
    return () => {
      cancelled = true;
      view?.destroy();
    };
  }, [start]);

  if (fallback) {
    return (
      <textarea
        className="files-fallback"
        defaultValue={start}
        spellCheck={false}
        onChange={(e) => changeRef.current(e.target.value)}
      />
    );
  }
  return <div className="files-editor-host" ref={host} />;
}

/**
 * FileEditor is the Files view: a lazily expanded tree on the left and one
 * text editor on the right. It stays mounted while another aside tab is up
 * so unsaved edits survive a tab switch; `active` gates the first load.
 */
export function FileEditor({ rpc, threadId, active }: { rpc: Rpc | null; threadId: string; active: boolean }) {
  const [dirs, setDirs] = useState<Record<string, DirState>>({});
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const [selected, setSelected] = useState<string | null>(null);
  const [open, setOpen] = useState<Record<string, OpenFile>>({});
  const [opening, setOpening] = useState<string | null>(null);
  const [openError, setOpenError] = useState<string | null>(null);
  // shown is how many entries each folder renders; absent means PAGE.
  const [shown, setShown] = useState<Record<string, number>>({});
  // Ref mirrors let async callbacks and the editor's key handler read the
  // latest files without re-creating callbacks on every keystroke. inFlight
  // stops a second click from starting a second read of the same file.
  const openRef = useRef(open);
  openRef.current = open;
  const inFlight = useRef(new Set<string>());

  const loadDir = useCallback(
    async (path: string) => {
      if (!rpc) return;
      setDirs((prev) => ({ ...prev, [path]: { loading: true } }));
      try {
        const res = await rpc.call<FsListing>("fs.list", { threadId, path });
        setDirs((prev) => ({ ...prev, [path]: { entries: res.entries, truncated: res.truncated } }));
      } catch (e) {
        setDirs((prev) => ({ ...prev, [path]: { error: errorText(e) } }));
      }
    },
    [rpc, threadId],
  );

  // Load the root once the view is shown, and each directory the first time it
  // is expanded. Refresh clears dirs, which re-runs this for everything open.
  useEffect(() => {
    if (!active || !rpc) return;
    for (const path of [ROOT, ...expanded]) {
      if (!dirs[path]) loadDir(path);
    }
  }, [active, rpc, expanded, dirs, loadDir]);

  const toggleDir = useCallback((path: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  }, []);

  const rows = useMemo(() => {
    const out: Row[] = [];
    const walk = (dir: string, depth: number) => {
      const st = dirs[dir];
      if (!st || st.loading) {
        out.push({ key: `load:${dir}`, depth, note: "Loading…" });
        return;
      }
      if (st.error) {
        out.push({ key: `err:${dir}`, depth, note: st.error, err: true });
        return;
      }
      const list = st.entries ?? [];
      const limit = shown[dir] ?? PAGE;
      for (const entry of list.slice(0, limit)) {
        out.push({ key: entry.path, depth, entry });
        if (entry.dir && expanded.has(entry.path)) walk(entry.path, depth + 1);
      }
      if (list.length > limit) {
        out.push({ key: `more:${dir}`, depth, more: { dir, hidden: list.length - limit } });
      }
      if (st.truncated) out.push({ key: `trunc:${dir}`, depth, note: "Listing truncated" });
    };
    walk(ROOT, 0);
    return out;
  }, [dirs, expanded, shown]);

  const showMore = useCallback((dir: string) => {
    setShown((prev) => ({ ...prev, [dir]: (prev[dir] ?? PAGE) + PAGE }));
  }, []);

  const openFile = useCallback(
    async (path: string) => {
      setSelected(path);
      setOpenError(null);
      if (openRef.current[path] || !rpc || inFlight.current.has(path)) return;
      inFlight.current.add(path);
      setOpening(path);
      try {
        const doc = await rpc.call<FsDocument>("fs.read", { threadId, path });
        setOpen((prev) => (prev[path] ? prev : { ...prev, [path]: { doc, text: doc.text, rev: 0 } }));
      } catch (e) {
        setOpenError(errorText(e));
      } finally {
        inFlight.current.delete(path);
        setOpening((cur) => (cur === path ? null : cur));
      }
    },
    [rpc, threadId],
  );

  const setText = useCallback((path: string, text: string) => {
    setOpen((prev) => ({ ...prev, [path]: { ...prev[path], text } }));
  }, []);

  const saveFile = useCallback(
    async (path: string) => {
      const f = openRef.current[path];
      if (!rpc || !f || f.saving || f.text === f.doc.text) return;
      const sent = f.text;
      setOpen((prev) => ({ ...prev, [path]: { ...prev[path], saving: true, error: undefined } }));
      try {
        const saved = await rpc.call<FsDocument>("fs.write", { threadId, path, text: sent, hash: f.doc.hash });
        setOpen((prev) => ({
          ...prev,
          [path]: { ...prev[path], saving: false, doc: { path, text: sent, size: saved.size, hash: saved.hash } },
        }));
      } catch (e) {
        setOpen((prev) => ({ ...prev, [path]: { ...prev[path], saving: false, error: errorText(e) } }));
      }
    },
    [rpc, threadId],
  );

  // Revert re-reads from disk and drops the buffer. It is the only way out of
  // a conflict (the file changed under an edit), so it is always offered.
  const revertFile = useCallback(
    async (path: string) => {
      if (!rpc) return;
      try {
        const doc = await rpc.call<FsDocument>("fs.read", { threadId, path });
        setOpen((prev) => ({
          ...prev,
          [path]: { doc, text: doc.text, rev: (prev[path]?.rev ?? 0) + 1 },
        }));
        setOpenError(null);
      } catch (e) {
        setOpenError(errorText(e));
      }
    },
    [rpc, threadId],
  );

  const refresh = useCallback(() => setDirs({}), []);

  const saveSelected = useCallback(() => {
    if (selected) saveFile(selected);
  }, [selected, saveFile]);

  // Cmd/Ctrl+S from the tree or toolbar. The editor's own Mod-s binding runs
  // first and calls preventDefault, so this skips that keystroke.
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.defaultPrevented) return;
    if ((e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "s") {
      e.preventDefault();
      saveSelected();
    }
  };

  const cur = selected ? open[selected] : undefined;
  const dirty = !!cur && cur.text !== cur.doc.text;

  return (
    <div className="files-view" onKeyDown={onKeyDown}>
      <div className="files-tree-pane">
        <div className="panel-bar">
          <span className="chat-title">Files</span>
          <button onClick={refresh} disabled={!rpc}>
            Refresh
          </button>
        </div>
        <div className="files-tree" role="tree">
          <FileTree
            rows={rows}
            expanded={expanded}
            selected={selected}
            onToggle={toggleDir}
            onOpen={openFile}
            onMore={showMore}
          />
        </div>
      </div>
      <div className="files-editor-pane">
        {!cur && (
          <p className={`hint${openError ? " err-text" : ""}`}>
            {opening ? `Opening ${opening}…` : openError ?? "Select a file to edit. Cmd/Ctrl+S saves."}
          </p>
        )}
        {cur && selected && (
          <>
            <div className="files-editor-head">
              <span className="path" title={selected}>
                {selected}
              </span>
              {dirty && <span className="files-dirty" title="Unsaved changes">● unsaved</span>}
              {cur.saving && <span className="hint">Saving…</span>}
              <span className="files-editor-actions">
                <button onClick={() => revertFile(selected)} disabled={cur.saving}>
                  Revert
                </button>
                <button onClick={() => saveFile(selected)} disabled={!dirty || cur.saving}>
                  Save
                </button>
              </span>
            </div>
            {cur.error && <p className="hint err-text">Save failed: {cur.error}</p>}
            {openError && <p className="hint err-text">{openError}</p>}
            <CodeEditor
              key={`${selected}:${cur.rev}`}
              initial={cur.text}
              onChange={(text) => setText(selected, text)}
              onSave={saveSelected}
            />
          </>
        )}
      </div>
    </div>
  );
}
