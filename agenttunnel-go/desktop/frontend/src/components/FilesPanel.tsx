import { useEffect, useState } from "react";

export type FileEntryLike = {
  name: string;
  path: string;
  dir: boolean;
  size?: number;
  children?: FileEntryLike[] | null;
};

export type FileContentLike = {
  path: string;
  text: string;
  binary: boolean;
  truncated: boolean;
  size: number;
};

const statusLetter: Record<string, string> = {
  added: "A",
  modified: "M",
  deleted: "D",
  untracked: "?",
  renamed: "R",
  unmerged: "U",
};

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

function TreeNode({
  entry,
  depth,
  status,
  expanded,
  selectedPath,
  onToggle,
  onSelect,
}: {
  entry: FileEntryLike;
  depth: number;
  status: Record<string, string>;
  expanded: Set<string>;
  selectedPath: string | null;
  onToggle: (path: string) => void;
  onSelect: (path: string) => void;
}) {
  if (entry.dir) {
    const open = expanded.has(entry.path);
    return (
      <>
        <div
          className="tree-row"
          style={{ paddingLeft: 8 + depth * 16 }}
          onClick={() => onToggle(entry.path)}
        >
          <Chevron open={open} />
          <span className="tree-name dir">{entry.name}</span>
        </div>
        {open &&
          (entry.children ?? []).map((c) => (
            <TreeNode
              key={c.path}
              entry={c}
              depth={depth + 1}
              status={status}
              expanded={expanded}
              selectedPath={selectedPath}
              onToggle={onToggle}
              onSelect={onSelect}
            />
          ))}
      </>
    );
  }
  const st = status[entry.path];
  return (
    <div
      className={`tree-row${selectedPath === entry.path ? " sel" : ""}`}
      style={{ paddingLeft: 8 + depth * 16 }}
      onClick={() => onSelect(entry.path)}
    >
      <span className="tree-spacer" />
      {st && <span className={`badge sm ${st}`}>{statusLetter[st] ?? st[0]}</span>}
      <span className="tree-name">{entry.name}</span>
    </div>
  );
}

/**
 * FilesPanel is the file browser: a directory tree with git status badges
 * plus a preview of the selected file.
 */
export function FilesPanel({
  root,
  entries,
  status,
  loading,
  error,
  selectedPath,
  content,
  contentLoading,
  contentError,
  onSelectFile,
  onRefresh,
}: {
  root: string | null;
  entries: FileEntryLike[];
  status: Record<string, string>;
  loading: boolean;
  error: string | null;
  selectedPath: string | null;
  content: FileContentLike | null;
  contentLoading: boolean;
  contentError: string | null;
  onSelectFile: (path: string) => void;
  onRefresh: () => void;
}) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  // Start with top-level directories open so the tree reads at a glance.
  useEffect(() => {
    setExpanded(new Set(entries.filter((e) => e.dir).map((e) => e.path)));
  }, [root]);

  const toggle = (path: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });

  const previewLines = content && !content.binary ? content.text.split("\n").slice(0, 2000) : [];

  return (
    <div className="files-panel">
      <div className="panel-bar">
        <span className="chat-title" title={root ?? ""}>
          {root ? root.split("/").filter(Boolean).pop() : "Files"}
        </span>
        <button onClick={onRefresh}>Refresh</button>
      </div>
      {loading && <p className="hint">Loading files…</p>}
      {error && <p className="hint err-text">{error}</p>}
      {!loading && !error && entries.length === 0 && (
        <p className="hint">No files in this project.</p>
      )}
      <div className="tree">
        {entries.map((e) => (
          <TreeNode
            key={e.path}
            entry={e}
            depth={0}
            status={status}
            expanded={expanded}
            selectedPath={selectedPath}
            onToggle={toggle}
            onSelect={onSelectFile}
          />
        ))}
      </div>
      {(selectedPath || contentLoading || contentError) && (
        <div className="file-preview">
          <div className="preview-head">
            <span className="path">{selectedPath}</span>
            {content?.truncated && <span className="hint">truncated</span>}
          </div>
          {contentLoading && <p className="hint">Loading…</p>}
          {contentError && <p className="hint err-text">{contentError}</p>}
          {content && content.binary && <p className="hint">Binary file — no preview.</p>}
          {content && !content.binary && (
            <pre className="preview-body">
              {previewLines.map((ln, i) => (
                <div key={i} className="pv-line">
                  <span className="pv-ln">{i + 1}</span>
                  <span className="pv-code">{ln || " "}</span>
                </div>
              ))}
            </pre>
          )}
        </div>
      )}
    </div>
  );
}
