/**
 * Subsequence fuzzy matching for small, in-memory lists (model names).
 *
 * Every non-space character of the query must appear in the target in order.
 * The score rewards contiguous runs and word starts so the closest matches
 * rank first; null means the target does not match at all.
 */
export function fuzzyScore(query: string, target: string): number | null {
  const q = query.trim().toLowerCase();
  if (!q) return 0;
  const t = target.toLowerCase();

  let score = 0;
  let from = 0;
  let prev = -2;
  for (const ch of q) {
    if (ch === " ") continue;
    const at = t.indexOf(ch, from);
    if (at < 0) return null;
    score += 1;
    if (at === prev + 1) score += 8;
    if (at === 0 || !/[a-z0-9]/.test(t[at - 1])) score += 6;
    if (at === from) score += 2;
    score -= Math.min(at - from, 4);
    prev = at;
    from = at + 1;
  }
  return score;
}
