import { paint, truncate, visibleWidth, type TuiColor } from "./theme.ts";

/**
 * Markdown as terminal lines.
 *
 * Assistant turns arrive as markdown and the transcript used to flatten them
 * into a single wrapped paragraph, which threw away exactly the structure a
 * reader needs: headings, list items, fenced code, and the `<details>` blocks
 * the workflow provider uses for its thinking. Line breaks are preserved
 * verbatim; everything else is decoration on top.
 *
 * Pure: text in, styled lines out. Widths are the caller's, and every returned
 * line is already within them.
 */

interface Inline {
  readonly text: string;
  readonly tone: TuiColor | null;
  readonly bold?: boolean;
  readonly italic?: boolean;
}

const INLINE_PATTERN = /(\*\*[^*]+\*\*|`[^`]+`|\*[^*]+\*|_[^_]+_)/gu;

/** Split a line into styled spans: bold, code, and emphasis. */
function inline(line: string): Inline[] {
  const spans: Inline[] = [];
  let index = 0;
  for (const match of line.matchAll(INLINE_PATTERN)) {
    const at = match.index ?? 0;
    if (at > index) spans.push({ text: line.slice(index, at), tone: null });
    const token = match[0];
    if (token.startsWith("**")) {
      spans.push({ text: token.slice(2, -2), tone: null, bold: true });
    } else if (token.startsWith("`")) {
      spans.push({ text: token.slice(1, -1), tone: "primary" });
    } else {
      spans.push({ text: token.slice(1, -1), tone: null, italic: true });
    }
    index = at + token.length;
  }
  if (index < line.length) spans.push({ text: line.slice(index), tone: null });
  return spans.length === 0 ? [{ text: "", tone: null }] : spans;
}

const styleSpan = (span: Inline): string => {
  if (span.text.length === 0) return "";
  if (span.tone === null && span.bold !== true && span.italic !== true) return span.text;
  const painted = span.tone === null ? span.text : paint(span.text, span.tone);
  // Emphasis is layered on top of the colour, which paint already reset.
  const open = `${span.bold === true ? "\u001b[1m" : ""}${span.italic === true ? "\u001b[3m" : ""}`;
  return open.length === 0 ? painted : `${open}${painted}\u001b[22m\u001b[23m`;
};

/** Wrap styled spans to `cols`, keeping the indent for continuation lines. */
function wrapSpans(spans: ReadonlyArray<Inline>, cols: number, indent: string): string[] {
  const out: string[] = [];
  let current = indent;
  let currentWidth = visibleWidth(indent);
  for (const span of spans) {
    const words = span.text.split(/(\s+)/u);
    for (const word of words) {
      if (word.length === 0) continue;
      const width = visibleWidth(word);
      const isSpace = /^\s+$/u.test(word);
      if (isSpace) {
        // A separator only joins words that fit; adding it blindly is how a
        // line ends up one column over the terminal.
        if (currentWidth > visibleWidth(indent) && currentWidth < cols) {
          current += " ";
          currentWidth += 1;
        }
        continue;
      }
      // A word longer than the line (a path, a URL, a hash) has to be cut:
      // wrapping alone cannot fit it.
      let rest = word;
      while (visibleWidth(rest) > cols - visibleWidth(indent)) {
        const room = Math.max(1, cols - currentWidth);
        const head = [...rest].slice(0, room).join("");
        if (currentWidth + visibleWidth(head) > cols) {
          out.push(current);
          current = indent;
          currentWidth = visibleWidth(indent);
        }
        current += styleSpan({ ...span, text: head });
        out.push(current);
        current = indent;
        currentWidth = visibleWidth(indent);
        rest = rest.slice(head.length);
      }
      if (rest.length === 0) continue;
      if (currentWidth + visibleWidth(rest) > cols && currentWidth > visibleWidth(indent)) {
        out.push(current);
        current = indent;
        currentWidth = visibleWidth(indent);
      }
      current += styleSpan({ ...span, text: rest });
      currentWidth += visibleWidth(rest);
    }
  }
  if (currentWidth > visibleWidth(indent) || out.length === 0) out.push(current);
  return out;
}

const headingTone = (level: number): TuiColor => (level <= 2 ? "primary" : "muted");

/**
 * Render one message: markdown in, lines out. `<details>` blocks become
 * thinking sections — labelled, indented, and dimmed — because the workflow
 * provider puts its reasoning there and it should not read as the answer.
 */
export function renderMarkdown(
  text: string,
  cols: number,
  options?: { readonly thinkingTone?: TuiColor; readonly collapseThinking?: number },
): string[] {
  const out: string[] = [];
  const lines = text.replace(/\r\n?/gu, "\n").split("\n");
  let fence = false;
  let thinking = false;
  let thinkingLines = 0;

  for (const raw of lines) {
    const line = raw.replace(/\s+$/u, "");
    const fenceMatch = /^\s*```/u.test(line);
    if (fenceMatch) {
      fence = !fence;
      out.push(paint(truncate(`  ${"─".repeat(Math.max(0, cols - 4))}`, cols), "faint"));
      continue;
    }
    if (fence) {
      out.push(paint(truncate(`  ${line}`, cols), "muted"));
      continue;
    }

    // `<details><summary>…</summary>` opens a thinking block; `</details>` closes it.
    const open = /^\s*<details>\s*<summary>([\s\S]*?)<\/summary>\s*$/u.exec(line);
    if (open !== null) {
      thinking = true;
      thinkingLines = 0;
      const label = (open[1] ?? "").replace(/<[^>]*>/gu, "").trim();
      out.push(paint(truncate(`  ${label.length === 0 ? "thinking" : label}`, cols), "faint"));
      continue;
    }
    if (/^\s*<\/details>\s*$/u.test(line)) {
      thinking = false;
      out.push("");
      continue;
    }
    if (thinking) {
      const limit = options?.collapseThinking ?? 0;
      if (limit > 0 && thinkingLines >= limit) {
        if (thinkingLines === limit) {
          out.push(paint("  … (open the log for the rest)", "faint"));
        }
        thinkingLines += 1;
        continue;
      }
      thinkingLines += 1;
      const spans = inline(line.trim());
      for (const wrapped of wrapSpans(spans, cols, "    ")) {
        out.push(paint(wrapped, options?.thinkingTone ?? "faint"));
      }
      continue;
    }

    if (line.trim().length === 0) {
      if (out.length > 0 && out.at(-1) !== "") out.push("");
      continue;
    }

    const heading = /^(#{1,6})\s+(.*)$/u.exec(line);
    if (heading !== null) {
      const level = (heading[1] ?? "#").length;
      out.push("");
      for (const wrapped of wrapSpans(inline(heading[2] ?? ""), cols, "")) {
        out.push(paint(wrapped, headingTone(level), { bold: true }));
      }
      continue;
    }

    if (/^\s*(?:---+|\*\*\*+|___+)\s*$/u.test(line)) {
      out.push(paint("─".repeat(Math.max(0, cols)), "faint"));
      continue;
    }

    const bullet = /^\s*[-*+]\s+(.*)$/u.exec(line);
    if (bullet !== null) {
      const wrapped = wrapSpans(inline(bullet[1] ?? ""), cols, "  • ");
      out.push(...wrapped);
      continue;
    }

    const numbered = /^\s*(\d+)[.)]\s+(.*)$/u.exec(line);
    if (numbered !== null) {
      const wrapped = wrapSpans(inline(numbered[2] ?? ""), cols, `  ${numbered[1]}. `);
      out.push(...wrapped);
      continue;
    }

    const quote = /^\s*>\s?(.*)$/u.exec(line);
    if (quote !== null) {
      for (const wrapped of wrapSpans(inline(quote[1] ?? ""), cols, "  │ ")) {
        out.push(paint(wrapped, "muted"));
      }
      continue;
    }

    out.push(...wrapSpans(inline(line), cols, ""));
  }

  while (out.length > 0 && out.at(-1) === "") out.pop();
  // Unpadded: the frame pads every line to the terminal itself, and doing it
  // twice made the renderer's own output hard to read in tests and logs.
  return out;
}
