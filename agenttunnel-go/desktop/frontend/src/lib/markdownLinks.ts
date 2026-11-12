/**
 * Markdown file references in agent output. An agent writes `[plan](docs/plan.md)`
 * or a bare `docs/plan.md`; both should open in the viewer. Everything here is
 * pure so the chat renderer and the viewer share one definition of a file link.
 */

/** A markdown file path ending in .md or .markdown (query and fragment already stripped). */
const MD_PATH = /^[^?#]*\.(?:md|markdown)$/i;

/** Matches a project-relative path ending in .md/.markdown inside prose. The
 * lookarounds keep it out of URLs (`https://x/a.md`), identifiers (`foo.mdx`),
 * `~/` and `/absolute` paths, and mid-word or sentence-internal hits. A trailing
 * period ends a sentence and is allowed. */
const BARE_MD_PATH =
  /(?<![\w./:@~+-])((?:\.{1,2}\/)?(?:[\w@+-][\w@+.-]*\/)*[\w@+-][\w@+.-]*\.(?:md|markdown))(?![\w/-]|\.\w)/gi;

/** isMarkdownFileHref reports whether a link target names a local markdown file
 * (not a web, mail, or in-page link). */
export function isMarkdownFileHref(href: string): boolean {
  if (!href || href.startsWith("#") || href.startsWith("//") || hasScheme(href)) {
    return false;
  }
  return MD_PATH.test(href.replace(/[?#].*$/, ""));
}

/** isLinkableMarkdownPath gates inline code and bare prose. Only a single token
 * (`cat README.md` is not a file) that is project-relative (`docs/a.md`,
 * `./a.md`, `../a.md`). A first segment with a dot before any slash
 * (`github.com/a/b.md`) reads as a domain, not a folder. */
export function isLinkableMarkdownPath(text: string): boolean {
  if (/\s/.test(text) || text.startsWith("/") || text.startsWith("~") || hasScheme(text)) return false;
  if (!MD_PATH.test(text)) return false;
  const segments = text.replace(/^(?:\.{1,2}\/)+/, "").split("/");
  return segments.slice(0, -1).every((s) => s === ".." || !s.includes("."));
}

/** isExternalHref reports whether a link is a web or mail link that may leave the
 * app. Only these render as a plain anchor; the external-link handler opens http(s). */
export function isExternalHref(href: string): boolean {
  return /^(?:https?|mailto):/i.test(href);
}

/** hasScheme reports whether href starts with a URL scheme such as `https:`. */
function hasScheme(href: string): boolean {
  return /^[a-z][a-z0-9+.-]*:/i.test(href);
}

/** headingSlug turns heading text into the fragment a `#anchor` link targets:
 * lowercase, spaces to dashes, punctuation dropped (GitHub-style). */
export function headingSlug(text: string): string {
  return text
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{N}\s-]/gu, "")
    .replace(/\s+/g, "-");
}

/** docPathFromHref strips the fragment and query and decodes percent-escapes, so
 * `plan%20v2.md#steps` becomes `plan v2.md`. */
export function docPathFromHref(href: string): string {
  const bare = href.replace(/[?#].*$/, "");
  try {
    return decodeURI(bare);
  } catch {
    return bare;
  }
}

/** joinDocPath resolves a link from inside a document against that document's
 * folder. Absolute paths pass through; the daemon checks containment. */
export function joinDocPath(docPath: string, target: string): string {
  if (target.startsWith("/")) return target;
  const slash = docPath.lastIndexOf("/");
  return slash < 0 ? target : `${docPath.slice(0, slash)}/${target}`;
}

type MdNode = {
  type: string;
  value?: string;
  url?: string;
  title?: string | null;
  children?: MdNode[];
};

/**
 * remarkMarkdownPaths turns bare `.md` paths in prose and in inline code into
 * link nodes, so they render through the same link path as `[x](x.md)`. Links
 * and code blocks are left alone: a link already has a target, and fenced code
 * is literal text.
 */
export function remarkMarkdownPaths() {
  return (tree: MdNode) => {
    walk(tree);
  };
}

/** Nodes that already carry a target (or are a reference to one). Nothing inside
 * them may become a link: a button inside an anchor is invalid HTML. */
const LINK_TYPES = new Set(["link", "linkReference"]);

function walk(node: MdNode) {
  if (!node.children || LINK_TYPES.has(node.type)) return;
  node.children = node.children.flatMap((child) => {
    if (child.type === "text" && child.value) return splitText(child.value);
    if (child.type === "inlineCode" && child.value && isLinkableMarkdownPath(child.value)) {
      return [linkNode(child.value, [child])];
    }
    walk(child);
    return [child];
  });
}

function splitText(value: string): MdNode[] {
  const out: MdNode[] = [];
  let last = 0;
  for (const m of value.matchAll(BARE_MD_PATH)) {
    if (!isLinkableMarkdownPath(m[1])) continue;
    const at = m.index ?? 0;
    if (at > last) out.push({ type: "text", value: value.slice(last, at) });
    out.push(linkNode(m[1], [{ type: "text", value: m[1] }]));
    last = at + m[0].length;
  }
  if (last === 0) return [{ type: "text", value }];
  if (last < value.length) out.push({ type: "text", value: value.slice(last) });
  return out;
}

function linkNode(url: string, children: MdNode[]): MdNode {
  return { type: "link", url, title: null, children };
}
