import { describe, it, expect } from 'vitest';
import { parseInline, parseBlock, parseMarkdownInline } from './markdown.js';

describe('parseInline', () => {
  it('returns the text as a single plain span when no markdown is present', () => {
    expect(parseInline('hello world')).toEqual([{ text: 'hello world' }]);
  });

  it('parses inline code', () => {
    expect(parseInline('use `npm test` to run tests')).toEqual([
      { text: 'use ' },
      { text: 'npm test', code: true, color: 'magenta' },
      { text: ' to run tests' },
    ]);
  });

  it('parses bold with **', () => {
    expect(parseInline('this is **bold** text')).toEqual([
      { text: 'this is ' },
      { text: 'bold', bold: true },
      { text: ' text' },
    ]);
  });

  it('parses bold with __', () => {
    expect(parseInline('this is __bold__ text')).toEqual([
      { text: 'this is ' },
      { text: 'bold', bold: true },
      { text: ' text' },
    ]);
  });

  it('parses italic with single *', () => {
    expect(parseInline('this is *italic* text')).toEqual([
      { text: 'this is ' },
      { text: 'italic', italic: true },
      { text: ' text' },
    ]);
  });

  it('parses italic with single _', () => {
    expect(parseInline('this is _italic_ text')).toEqual([
      { text: 'this is ' },
      { text: 'italic', italic: true },
      { text: ' text' },
    ]);
  });

  it('does not eat underscores inside snake_case identifiers', () => {
    const spans = parseInline('see user_id column');
    // No italic span should be emitted; the whole string is one plain span.
    expect(spans).toEqual([{ text: 'see user_id column' }]);
  });

  it('parses strikethrough', () => {
    expect(parseInline('this is ~~old~~ text')).toEqual([
      { text: 'this is ' },
      { text: 'old', strikethrough: true },
      { text: ' text' },
    ]);
  });

  it('parses links as text + dim url', () => {
    const spans = parseInline('see [docs](https://example.com)');
    expect(spans).toEqual([
      { text: 'see ' },
      { text: 'docs', color: 'cyan', underline: true },
      { text: ' (https://example.com)', dim: true },
    ]);
  });

  it('handles multiple inline elements on one line', () => {
    const spans = parseInline('use **bold** and *italic* and `code` here');
    // Should emit at least one span for each of the four pieces.
    const bold = spans.find((s) => s.bold);
    const italic = spans.find((s) => s.italic);
    const code = spans.find((s) => s.code);
    expect(bold?.text).toBe('bold');
    expect(italic?.text).toBe('italic');
    expect(code?.text).toBe('code');
  });

  it('merges adjacent same-style spans', () => {
    const spans = parseInline('**a****b**');
    // The two bolds are adjacent; after parse, the inner content should be one span.
    expect(spans.length).toBe(1);
    expect(spans[0]?.text).toBe('ab');
    expect(spans[0]?.bold).toBe(true);
  });

  it('handles empty input', () => {
    expect(parseInline('')).toEqual([]);
  });
});

describe('parseBlock', () => {
  it('classifies headings 1-6', () => {
    expect(parseBlock('# Title').kind).toBe('heading');
    expect(parseBlock('# Title').level).toBe(1);
    expect(parseBlock('### Sub').level).toBe(3);
    expect(parseBlock('###### Deep').level).toBe(6);
  });

  it('classifies a horizontal rule', () => {
    expect(parseBlock('---').kind).toBe('rule');
    expect(parseBlock('***').kind).toBe('rule');
    expect(parseBlock('___').kind).toBe('rule');
    expect(parseBlock('- - -').kind).toBe('rule');
  });

  it('classifies unordered list items', () => {
    const b = parseBlock('- first item');
    expect(b.kind).toBe('list-item');
    expect(b.marker).toBe('-');
    expect(b.indent).toBe(0);
  });

  it('classifies indented list items with indent depth', () => {
    const b = parseBlock('  - nested item');
    expect(b.kind).toBe('list-item');
    expect(b.indent).toBe(1);
  });

  it('classifies ordered list items with numeric marker', () => {
    const b = parseBlock('1. first');
    expect(b.kind).toBe('ordered-list-item');
    expect(b.marker).toBe('1.');
  });

  it('classifies blockquotes', () => {
    const b = parseBlock('> a wise saying');
    expect(b.kind).toBe('quote');
    expect(b.indent).toBe(0);
  });

  it('classifies table rows', () => {
    const b = parseBlock('| name | age |');
    expect(b.kind).toBe('table');
    expect(b.cells).toEqual(['name', 'age']);
  });

  it('classifies table align rows', () => {
    const b = parseBlock('| --- | :---: |');
    expect(b.kind).toBe('table');
    expect(b.isTableAlign).toBe(true);
  });

  it('falls back to paragraph for plain text', () => {
    const b = parseBlock('just some text');
    expect(b.kind).toBe('paragraph');
    expect(b.spans.length).toBeGreaterThan(0);
  });

  it('classifies empty lines as paragraphs (with empty span)', () => {
    const b = parseBlock('');
    expect(b.kind).toBe('paragraph');
  });
});

describe('parseMarkdownInline (backwards compat)', () => {
  it('parses inline elements the same as parseInline', () => {
    expect(parseMarkdownInline('hello **world**')).toEqual(parseInline('hello **world**'));
  });
});
