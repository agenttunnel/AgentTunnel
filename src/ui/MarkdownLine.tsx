import React from 'react';
import { Text } from 'ink';
import {
  colors,
  colorForOutputType,
  glyphs,
  prefixForOutputType,
} from './theme.js';
import { parseBlock, type MdBlock, type MdSegment } from '../core/utils/markdown.js';
import type { OutputLine, OutputLineType } from '../types/index.js';

interface MarkdownLineProps {
  line: OutputLine;
  /** When true, render in dim mode (e.g. for sub-agent log detail). */
  dim?: boolean;
}

/**
 * Render one `OutputLine` as a markdown-aware row.
 *
 * Used by both the main log (`OutputLineView` consumers) and the
 * sub-agent log detail view. The `dim` flag dims the text without
 * changing semantics.
 */
export function MarkdownLine({ line, dim = false }: MarkdownLineProps): React.ReactElement {
  if (line.codeBlock) {
    return (
      <Text backgroundColor={colors.surface} color={colors.primary}>
        {'  '}
        {glyphs.blockHalf}
        {line.text}
      </Text>
    );
  }

  const typeColor = dim ? colors.grid : colorForOutputType(line.type);
  const prefix = dim ? '' : prefixForOutputType(line.type);
  const block = parseBlock(line.text);

  return renderBlock(block, line.type, typeColor, prefix, dim);
}

function renderBlock(
  block: MdBlock,
  lineType: OutputLineType,
  typeColor: string,
  prefix: string,
  dim: boolean,
): React.ReactElement {
  const baseProps = { color: typeColor };

  // For error lines, even markdown emphasis keeps the danger color
  // (we still let inline spans override their own color when present).
  const errorGuard = lineType === 'error';

  switch (block.kind) {
    case 'heading': {
      const level = block.level ?? 1;
      const headingColor = errorGuard
        ? colors.danger
        : level <= 2
          ? colors.primary
          : colors.accent;
      const indent = '  '.repeat(block.indent);
      const bar = level === 1 ? `${glyphs.blockFull}` : level === 2 ? '▰' : '▱';
      return (
        <Text {...baseProps}>
          {prefix}
          <Text color={headingColor} bold>
            {indent}
            {`${bar} `}
          </Text>
          {block.spans.map((s, i) => (
            <SpanText key={i} span={s} fallback={headingColor} dim={dim} />
          ))}
        </Text>
      );
    }

    case 'rule': {
      return (
        <Text {...baseProps}>
          {prefix}
          <Text color={colors.dim}>
            {`${glyphs.hLine.repeat(60)}`}
          </Text>
        </Text>
      );
    }

    case 'list-item': {
      const indent = '  '.repeat(block.indent);
      const bullet = block.indent === 0 ? `${glyphs.chevron} ` : '· ';
      return (
        <Text {...baseProps}>
          {prefix}
          <Text color={colors.accent}>
            {indent}
            {bullet}
          </Text>
          {block.spans.map((s, i) => (
            <SpanText key={i} span={s} fallback={typeColor} dim={dim} />
          ))}
        </Text>
      );
    }

    case 'ordered-list-item': {
      const indent = '  '.repeat(block.indent);
      return (
        <Text {...baseProps}>
          {prefix}
          <Text color={colors.accent} bold>
            {indent}
            {`${block.marker} `}
          </Text>
          {block.spans.map((s, i) => (
            <SpanText key={i} span={s} fallback={typeColor} dim={dim} />
          ))}
        </Text>
      );
    }

    case 'quote': {
      const indent = '  '.repeat(block.indent);
      return (
        <Text {...baseProps}>
          {prefix}
          <Text color={colors.accent}>
            {indent}
            {`│ `}
          </Text>
          {block.spans.map((s, i) => (
            <SpanText
              key={i}
              span={s}
              fallback={colors.grid}
              italicDefault
              dim={dim}
            />
          ))}
        </Text>
      );
    }

    case 'table': {
      if (block.isTableAlign) {
        return (
          <Text {...baseProps}>
            {prefix}
            <Text color={colors.dim}>
              {`─ ${blockText(block)} ─`}
            </Text>
          </Text>
        );
      }
      const cells = block.cells ?? [];
      return (
        <Text {...baseProps}>
          {prefix}
          <Text color={colors.dim}>{`│ `}</Text>
          {cells.map((c, i) => (
            <React.Fragment key={i}>
              <Text color={colors.white}>{c}</Text>
              {i < cells.length - 1 ? <Text color={colors.dim}>{` │ `}</Text> : null}
            </React.Fragment>
          ))}
          <Text color={colors.dim}>{` │`}</Text>
        </Text>
      );
    }

    case 'paragraph':
    default: {
      if (block.spans.length === 0 || (block.spans.length === 1 && block.spans[0]?.text === '')) {
        return <Text {...baseProps}>{prefix}</Text>;
      }
      return (
        <Text {...baseProps}>
          {prefix}
          {block.spans.map((s, i) => (
            <SpanText key={i} span={s} fallback={typeColor} dim={dim} />
          ))}
        </Text>
      );
    }
  }
}

interface SpanTextProps {
  span: MdSegment;
  fallback: string;
  italicDefault?: boolean;
  dim?: boolean;
}

function SpanText({ span, fallback, italicDefault, dim }: SpanTextProps): React.ReactElement {
  return (
    <Text
      bold={span.bold}
      italic={span.italic ?? italicDefault}
      underline={span.underline}
      strikethrough={span.strikethrough}
      dimColor={span.dim ?? dim}
      color={span.color ?? fallback}
    >
      {span.text}
    </Text>
  );
}

/** Plain text fallback used to render a block when we want to dump the raw line. */
function blockText(block: MdBlock): string {
  if (block.cells) return block.cells.join(' │ ');
  return block.spans.map((s) => s.text).join('');
}
