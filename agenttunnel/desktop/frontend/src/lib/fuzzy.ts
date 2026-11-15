// A small dependency-free fuzzy matcher for thread search. A query matches
// when its characters appear in order, case-insensitively. The score rewards
// contiguous runs and characters that start a word, so "thl" ranks
// "Thread list" above a scattered hit. Only a rank is needed, not positions.

const MATCH = 1;
const CONTIGUOUS = 6;
const RUN = 2;
const WORD_START = 8;
const LEADING_GAP = 0.05;
const WORD_BREAK = /[^a-z0-9]/i;

/**
 * Best fuzzy subsequence score of `query` in `text`, or null when the query
 * does not match. An empty query matches everything with score 0.
 */
export function fuzzyScore(query: string, text: string): number | null {
  const q = query.trim().toLowerCase();
  const t = text.toLowerCase();
  if (q === "") return 0;
  if (q.length > t.length) return null;

  const n = t.length;
  const starts = new Uint8Array(n);
  for (let i = 0; i < n; i++) starts[i] = i === 0 || WORD_BREAK.test(t[i - 1]) ? 1 : 0;

  // prevScore[j] is the best score matching the query prefix ending at text[j];
  // prevRun[j] is that alignment's contiguous run length, which earns a small
  // extra bonus as it grows.
  let prevScore = new Float64Array(n).fill(-Infinity);
  let prevRun = new Uint16Array(n);
  for (let j = 0; j < n; j++) {
    if (t[j] !== q[0]) continue;
    prevScore[j] = MATCH + (starts[j] ? WORD_START : 0) - j * LEADING_GAP;
    prevRun[j] = 1;
  }

  for (let i = 1; i < q.length; i++) {
    const curScore = new Float64Array(n).fill(-Infinity);
    const curRun = new Uint16Array(n);
    let bestAny = -Infinity;
    for (let j = 0; j < n; j++) {
      // A non-contiguous predecessor must end at or before j-2.
      if (j >= 2 && prevScore[j - 2] > bestAny) bestAny = prevScore[j - 2];
      if (t[j] !== q[i]) continue;

      let score = -Infinity;
      let run = 0;
      if (j >= 1 && prevScore[j - 1] > -Infinity) {
        run = prevRun[j - 1] + 1;
        score = prevScore[j - 1] + MATCH + CONTIGUOUS + (run > 2 ? RUN : 0);
      }
      if (bestAny > -Infinity) {
        const s = bestAny + MATCH + (starts[j] ? WORD_START : 0);
        if (s > score) {
          score = s;
          run = 1;
        }
      }
      if (score > -Infinity) {
        curScore[j] = score;
        curRun[j] = run;
      }
    }
    prevScore = curScore;
    prevRun = curRun;
  }

  let best = -Infinity;
  for (let j = 0; j < n; j++) if (prevScore[j] > best) best = prevScore[j];
  return best === -Infinity ? null : best;
}
