import { createContext, memo, useContext, type ComponentProps, type ReactNode } from "react";
import ReactMarkdown, { type Components, type ExtraProps } from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeHighlight from "rehype-highlight";
import {
  docPathFromHref,
  headingSlug,
  isExternalHref,
  isMarkdownFileHref,
  remarkMarkdownPaths,
} from "../lib/markdownLinks";

/**
 * DocLinkContext receives a clicked markdown file reference (the raw href, which
 * may be relative). App supplies it so every chat message can open the viewer
 * without each message being handed a callback; the viewer supplies its own to
 * resolve links against the open document's folder. Null means no viewer is
 * mounted and file links render as inert text.
 */
export const DocLinkContext = createContext<((path: string) => void) | null>(null);

const components: Components = {
  table({ children }: ComponentProps<"table"> & ExtraProps) {
    return (
      <div className="md-table-wrap">
        <table>{children}</table>
      </div>
    );
  },
  a({ href, children, title }: ComponentProps<"a"> & ExtraProps) {
    if (!href) return <span className="md-link-inert">{children}</span>;
    if (isMarkdownFileHref(href)) {
      return <FileLink href={href} title={title}>{children}</FileLink>;
    }
    if (isExternalHref(href)) return <a href={href} title={title}>{children}</a>;
    // In-document fragments and every other relative target stay inside the
    // webview. An href here would navigate the single-page app away (or, for
    // http on the dev origin, be sent out as an external link), so none is rendered.
    if (href.startsWith("#")) {
      return (
        <button
          type="button"
          className="md-file-link"
          title={title}
          onClick={(e) => scrollToHeading(e.currentTarget, href.slice(1))}
        >
          {children}
        </button>
      );
    }
    return <span className="md-link-inert" title={title ?? href}>{children}</span>;
  },
};

/** scrollToHeading moves the nearest rendered document to the heading a `#fragment`
 * names. Headings match by GitHub-style slug; an unknown fragment does nothing. */
function scrollToHeading(from: Element, fragment: string) {
  let want = fragment;
  try {
    want = decodeURIComponent(fragment);
  } catch {
    // Keep the raw fragment: it will simply match no heading.
  }
  const slug = headingSlug(want);
  const root = from.closest(".md");
  if (!root) return;
  for (const h of root.querySelectorAll("h1, h2, h3, h4, h5, h6")) {
    if (headingSlug(h.textContent ?? "") === slug) {
      h.scrollIntoView({ block: "start" });
      return;
    }
  }
}

/** FileLink is a button, not an anchor: an href would be caught by the external-link
 * handler or navigate the window. */
function FileLink({ href, title, children }: { href: string; title?: string; children: ReactNode }) {
  const open = useContext(DocLinkContext);
  return (
    <button
      type="button"
      className="md-file-link"
      title={title ?? docPathFromHref(href)}
      disabled={!open}
      onClick={() => open?.(href)}
    >
      {children}
    </button>
  );
}

const remarkPlugins = [remarkGfm, remarkMarkdownPaths];
const rehypePlugins: ComponentProps<typeof ReactMarkdown>["rehypePlugins"] = [
  // Only fenced language hints highlight. Auto-detection runs a language guesser on
  // every unlabeled block, which costs too much on long documents.
  [rehypeHighlight, { detect: false, ignoreMissing: true }],
];

/**
 * Markdown renders agent output the way the agents write it — paragraphs, lists,
 * tables, inline code — with fenced code blocks syntax-highlighted. Raw HTML is
 * not enabled, so agent output cannot inject markup.
 *
 * Local .md/.markdown references (links and bare paths) render as buttons that
 * open the file in the viewer; see DocLinkContext.
 *
 * Tables are wrapped in a scroll container: a wide table scrolls instead of
 * crushing its columns. Memoized so a re-render of a message list only touches
 * messages whose text changed.
 */
export const Markdown = memo(function Markdown({ text }: { text: string }) {
  return (
    <div className="md">
      <ReactMarkdown
        remarkPlugins={remarkPlugins}
        rehypePlugins={rehypePlugins}
        components={components}
      >
        {text}
      </ReactMarkdown>
    </div>
  );
});
