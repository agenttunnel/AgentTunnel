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
  color?: MdColor;
  code?: boolean;
  heading?: 1 | 2 | 3;
  rule?: boolean;
}

const TOKEN_PATTERN =
  /(\*\*[^*]+\*\*|`[^`]+`|^#{1,3}\s+.+$|^---+$)/gm;

export function parseMarkdownInline(input: string): MdSegment[] {
  const segments: MdSegment[] = [];
  let cursor = 0;
  const matches = input.matchAll(TOKEN_PATTERN);

  for (const match of matches) {
    const start = match.index ?? 0;
    if (start > cursor) {
      segments.push({ text: input.slice(cursor, start) });
    }

    const token = match[0];

    if (token.startsWith('**') && token.endsWith('**')) {
      segments.push({ text: token.slice(2, -2), bold: true });
    } else if (token.startsWith('`') && token.endsWith('`')) {
      segments.push({ text: token.slice(1, -1), code: true, color: 'magenta' });
    } else if (token.startsWith('###')) {
      segments.push({ text: token.replace(/^###\s+/, ''), heading: 3, bold: true, color: 'cyan' });
    } else if (token.startsWith('##')) {
      segments.push({ text: token.replace(/^##\s+/, ''), heading: 2, bold: true, color: 'cyan' });
    } else if (token.startsWith('#')) {
      segments.push({ text: token.replace(/^#\s+/, ''), heading: 1, bold: true, color: 'cyan' });
    } else if (token.startsWith('---')) {
      segments.push({ text: '─'.repeat(20), rule: true, dim: true });
    }

    cursor = start + token.length;
  }

  if (cursor < input.length) {
    segments.push({ text: input.slice(cursor) });
  }

  return segments.length > 0 ? segments : [{ text: input }];
}
