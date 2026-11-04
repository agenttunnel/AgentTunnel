// Word-level diff for intra-line change highlighting in the diff viewer,
// in the spirit of opencode's word-line diffs. Pairs one deleted and one added
// line and marks the changed token runs on each side.

export type TokSpan = { text: string; changed: boolean };

// Splits into word / whitespace / punctuation runs so "foo(bar)" diffs as
// ["foo", "(", "bar", ")"] rather than one changed blob.
function tokenize(s: string): string[] {
  const out = s.match(/[\p{L}\p{N}_]+|\s+|[^\p{L}\p{N}_\s]+/gu);
  return out ?? [];
}

const MAX_TOKENS = 200;
const MAX_CHARS = 1000;

function toSpans(tokens: string[], changed: boolean[]): TokSpan[] {
  const spans: TokSpan[] = [];
  for (let k = 0; k < tokens.length; k++) {
    const last = spans[spans.length - 1];
    if (last && last.changed === changed[k]) last.text += tokens[k];
    else spans.push({ text: tokens[k], changed: changed[k] });
  }
  return spans;
}

/** Marks changed token runs between one deleted and one added line. Falls
 * back to fully-marked spans for long inputs. */
export function wordDiff(
  oldText: string,
  newText: string,
): { oldSpans: TokSpan[]; newSpans: TokSpan[] } {
  const plain = (s: string): TokSpan[] => [{ text: s, changed: true }];
  if (oldText === newText || oldText.length + newText.length > MAX_CHARS) {
    return { oldSpans: plain(oldText), newSpans: plain(newText) };
  }
  const a = tokenize(oldText);
  const b = tokenize(newText);
  if (a.length > MAX_TOKENS || b.length > MAX_TOKENS) {
    return { oldSpans: plain(oldText), newSpans: plain(newText) };
  }
  // Suffix LCS table: t[i][j] = LCS length of a[i..] and b[j..].
  const w = b.length + 1;
  const t = new Uint16Array((a.length + 1) * w);
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      t[i * w + j] =
        a[i] === b[j]
          ? t[(i + 1) * w + (j + 1)] + 1
          : Math.max(t[(i + 1) * w + j], t[i * w + (j + 1)]);
    }
  }
  // Forward walk: matches are shared, otherwise the longer-LCS direction wins.
  const aChanged = new Array<boolean>(a.length).fill(true);
  const bChanged = new Array<boolean>(b.length).fill(true);
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      aChanged[i] = false;
      bChanged[j] = false;
      i++;
      j++;
    } else if (t[(i + 1) * w + j] >= t[i * w + (j + 1)]) {
      i++;
    } else {
      j++;
    }
  }
  return { oldSpans: toSpans(a, aChanged), newSpans: toSpans(b, bChanged) };
}
