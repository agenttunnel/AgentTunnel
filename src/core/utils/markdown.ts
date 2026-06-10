/**
 * Markdown renderer for AgentTunnel.
 *
 * Two layers:
 *   - `parseInline` (existing): tokenizes a string into a flat array of
 *     styled spans for inline emphasis (bold, italic, code, links, etc.).
 *     Output shape: `MdSegment[]`.
 *   - `parseBlock` (new): classifies a single line into a block-level
 *     `MdNode` — heading, list item, blockquote, horizontal rule, table
 *     row, or paragraph. The TUI renders one `MdNode` per output line.
 *
 * We deliberately keep the per-line model (rather than a full multi-line
 * AST). Output from the orchestrator / sub-agents is streamed one
 * terminal line at a time, so cross-line constructs (multi-line lists,
 * multi-line blockquotes) would already have been split. Each line is
 * rendered as the start of its own block. That's "good enough" for
 * terminal streaming and matches what users expect.
 */

export type MdColor =
  | 'red'
  | 'green'
  | 'yellow'
  | 'blue'
  | 'magenta'
  | 'cyan'
  | 'white'
  | 'gray';

export interface MdSegment {
  text: string;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
  strikethrough?: boolean;
  code?: boolean;
  color?: MdColor;
}

export type MdBlockKind =
  | 'heading'
  | 'list-item'
  | 'ordered-list-item'
  | 'quote'
  | 'rule'
  | 'table'
  | 'paragraph';

export interface MdBlock {
  kind: MdBlockKind;
  /** Heading level 1-6. Only present when kind === 'heading'. */
  level?: 1 | 2 | 3 | 4 | 5 | 6;
  /** List-item marker (e.g. '-', '*', '+', '1.', '2)'). */
  marker?: string;
  /** Indent depth, in "  " (two-space) units. 0 = no indent. */
  indent: number;
  /** Inline spans for the rendered text. */
  spans: MdSegment[];
  /** For tables: cells of the row. */
  cells?: string[];
  /** For tables: true if this is the alignment row (e.g. "| --- | --- |"). */
  isTableAlign?: boolean;
}

// ────────────────────────────────────────────────────────────────────────────
//  Inline parser
// ────────────────────────────────────────────────────────────────────────────

/**
 * Tokenize a string into styled spans. Supports:
 *   - inline code:    `code`
 *   - links:          [text](url)        → rendered as "text (url)"
 *   - bold:           **x** or __x__
 *   - italic:         *x* or _x_         (single, not adjacent to alnum on
 *                                              the other side, to avoid eating
 *                                              snake_case identifiers)
 *   - strikethrough:  ~~x~~
 *   - everything else: plain text
 */
const CODE_PATTERN = /`[^`\n]+`/;
const LINK_PATTERN = /\[([^\]\n]+)\]\(([^)\n]+)\)/;
const BOLD_STAR_PATTERN = /\*\*([^*\n]+)\*\*/;
const BOLD_UNDER_PATTERN = /__([^_\n]+)__/;
const STRIKE_PATTERN = /~~([^~\n]+)~~/;
const ITALIC_STAR_PATTERN = /(^|[^\w*])\*([^*\n]+)\*(?=$|[^\w*])/;
const ITALIC_UNDER_PATTERN = /(^|[^\w_])_([^_\n]+)_(?=$|[^\w_])/;

export function parseInline(input: string): MdSegment[] {
  const out: MdSegment[] = [];
  let cursor = 0;

  while (cursor < input.length) {
    const rest = input.slice(cursor);
    const codeMatch = CODE_PATTERN.exec(rest);
    const linkMatch = LINK_PATTERN.exec(rest);
    const boldStar = BOLD_STAR_PATTERN.exec(rest);
    const boldUnder = BOLD_UNDER_PATTERN.exec(rest);
    const strike = STRIKE_PATTERN.exec(rest);
    const italicStar = ITALIC_STAR_PATTERN.exec(rest);
    const italicUnder = ITALIC_UNDER_PATTERN.exec(rest);

    // Pick the earliest match.
    type Hit = { idx: number; len: number; build: () => MdSegment[] };
    const hits: Hit[] = [];
    if (codeMatch) {
      const m = codeMatch;
      hits.push({
        idx: cursor + m.index,
        len: m[0].length,
        build: () => [{ text: m[0].slice(1, -1), code: true, color: 'magenta' }],
      });
    }
    if (linkMatch) {
      const m = linkMatch;
      hits.push({
        idx: cursor + m.index,
        len: m[0].length,
        build: () => [
          { text: m[1] ?? '', color: 'cyan', underline: true },
          { text: ` (${m[2] ?? ''})`, dim: true },
        ],
      });
    }
    if (boldStar) {
      const m = boldStar;
      hits.push({
        idx: cursor + m.index,
        len: m[0].length,
        build: () => [{ text: m[1] ?? '', bold: true }],
      });
    }
    if (boldUnder) {
      const m = boldUnder;
      hits.push({
        idx: cursor + m.index,
        len: m[0].length,
        build: () => [{ text: m[1] ?? '', bold: true }],
      });
    }
    if (strike) {
      const m = strike;
      hits.push({
        idx: cursor + m.index,
        len: m[0].length,
        build: () => [{ text: m[1] ?? '', strikethrough: true }],
      });
    }
    if (italicStar) {
      const m = italicStar;
      // Italic regex has a leading non-word guard at group 1 — adjust.
      const guard = m[1] ?? '';
      const inner = m[2] ?? '';
      const start = cursor + m.index + guard.length;
      const len = m[0].length - guard.length;
      hits.push({
        idx: start,
        len,
        build: () => [{ text: inner, italic: true }],
      });
    }
    if (italicUnder) {
      const m = italicUnder;
      const guard = m[1] ?? '';
      const inner = m[2] ?? '';
      const start = cursor + m.index + guard.length;
      const len = m[0].length - guard.length;
      hits.push({
        idx: start,
        len,
        build: () => [{ text: inner, italic: true }],
      });
    }

    if (hits.length === 0) {
      out.push({ text: rest });
      break;
    }

    hits.sort((a, b) => a.idx - b.idx);
    const first = hits[0]!;
    if (first.idx > cursor) {
      out.push({ text: input.slice(cursor, first.idx) });
    }
    out.push(...first.build());
    cursor = first.idx + first.len;
  }

  return mergeAdjacent(out);
}

function mergeAdjacent(spans: MdSegment[]): MdSegment[] {
  const merged: MdSegment[] = [];
  for (const s of spans) {
    if (s.text.length === 0) continue;
    const last = merged[merged.length - 1];
    if (
      last &&
      last.bold === s.bold &&
      last.italic === s.italic &&
      last.dim === s.dim &&
      last.underline === s.underline &&
      last.strikethrough === s.strikethrough &&
      last.code === s.code &&
      last.color === s.color
    ) {
      last.text += s.text;
    } else {
      merged.push({ ...s });
    }
  }
  return merged;
}

// ────────────────────────────────────────────────────────────────────────────
//  Block-level classifier (per-line)
// ────────────────────────────────────────────────────────────────────────────

const HEADING_PATTERN = /^(#{1,6})\s+(.+?)\s*#*\s*$/;
const UL_PATTERN = /^(\s*)([-*+])\s+(.+)$/;
const OL_PATTERN = /^(\s*)(\d+[.)])\s+(.+)$/;
const QUOTE_PATTERN = /^(\s*)>\s?(.*)$/;
// A horizontal rule: three or more of - * _ (optionally with spaces) on a line by themselves.
// We match the three characters case (`- - -`) as well as the contiguous case (`---`).
const RULE_PATTERN = /^\s*(?:[-*_]\s*){3,}$|^\s*-{3,}\s*$|^\s*\*{3,}\s*$|^\s*_{3,}\s*$/;
const TABLE_ROW_PATTERN = /^\s*\|([^|]+)\s*(\|\s*([^|]+)\s*)+\|?\s*$/;
const TABLE_ALIGN_PATTERN = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)+\|?\s*$/;

/**
 * Classify a single line of markdown into a block node.
 * Empty lines are rendered as a thin separator (rule).
 */
export function parseBlock(line: string): MdBlock {
  if (line.trim().length === 0) {
    return { kind: 'paragraph', indent: 0, spans: [{ text: '' }] };
  }

  const heading = HEADING_PATTERN.exec(line);
  if (heading && heading[1] && heading[2] !== undefined) {
    const levelRaw = heading[1].length;
    const level = (Math.min(6, Math.max(1, levelRaw)) as 1 | 2 | 3 | 4 | 5 | 6);
    return {
      kind: 'heading',
      level,
      indent: 0,
      spans: parseInline(heading[2]),
    };
  }

  const rule = RULE_PATTERN.exec(line);
  if (rule) {
    return { kind: 'rule', indent: 0, spans: [] };
  }

  const quote = QUOTE_PATTERN.exec(line);
  if (quote) {
    const indent = (quote[1]?.length ?? 0) / 2;
    const inner = quote[2] ?? '';
    return {
      kind: 'quote',
      indent,
      spans: parseInline(inner),
    };
  }

  const ul = UL_PATTERN.exec(line);
  if (ul) {
    const indent = (ul[1]?.length ?? 0) / 2;
    const marker = ul[2] ?? '-';
    const text = ul[3] ?? '';
    return {
      kind: 'list-item',
      marker,
      indent,
      spans: parseInline(text),
    };
  }

  const ol = OL_PATTERN.exec(line);
  if (ol) {
    const indent = (ol[1]?.length ?? 0) / 2;
    const marker = ol[2] ?? '1.';
    const text = ol[3] ?? '';
    return {
      kind: 'ordered-list-item',
      marker,
      indent,
      spans: parseInline(text),
    };
  }

  const align = TABLE_ALIGN_PATTERN.exec(line);
  if (align) {
    return { kind: 'table', indent: 0, spans: [], isTableAlign: true };
  }
  const table = TABLE_ROW_PATTERN.exec(line);
  if (table) {
    const cells = line
      .trim()
      .replace(/^\|/, '')
      .replace(/\|$/, '')
      .split('|')
      .map((c) => c.trim());
    return { kind: 'table', indent: 0, spans: [], cells };
  }

  return { kind: 'paragraph', indent: 0, spans: parseInline(line) };
}

// Backwards-compatible alias used elsewhere in the codebase.
export function parseMarkdownInline(input: string): MdSegment[] {
  return parseInline(input);
}
