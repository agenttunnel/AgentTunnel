import { useCallback } from "react";
import { DocLinkContext, Markdown } from "./Markdown";
import { docPathFromHref, joinDocPath } from "../lib/markdownLinks";

/** One markdown document as the daemon's markdown.read returns it. */
export type MarkdownDocLike = { path: string; size: number; text: string };

/**
 * MarkdownViewer shows one markdown file read-only. It is dumb: App fetches the
 * document and owns the open state. It is loaded lazily from App, so the viewer
 * code stays out of the first paint. Links inside the document resolve against
 * the document's own folder, as they would on GitHub.
 */
export default function MarkdownViewer({
  path,
  doc,
  loading,
  error,
  onOpenPath,
  onClose,
}: {
  path: string;
  doc: MarkdownDocLike | null;
  loading: boolean;
  error: string | null;
  /** Receives a root-relative path, already decoded. */
  onOpenPath: (path: string) => void;
  onClose: () => void;
}) {
  const base = doc?.path ?? path;
  const openRelative = useCallback(
    (href: string) => onOpenPath(joinDocPath(base, docPathFromHref(href))),
    [base, onOpenPath],
  );

  let body;
  if (error) body = <div className="empty err-text">{error}</div>;
  else if (loading || !doc) body = <div className="empty">Loading…</div>;
  else if (doc.text.trim() === "") body = <div className="empty">This file is empty.</div>;
  else
    body = (
      <DocLinkContext.Provider value={openRelative}>
        <Markdown text={doc.text} />
      </DocLinkContext.Provider>
    );

  return (
    <div className="doc-view">
      <div className="doc-view-head">
        <span className="doc-view-path" title={base}>
          {base}
        </span>
        <button type="button" className="doc-view-close" onClick={onClose}>
          Close
        </button>
      </div>
      <div className="stream doc-view-body">{body}</div>
    </div>
  );
}
