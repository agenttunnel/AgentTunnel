// A solo thread's identity: one of the six harness hues, picked by its id.
// The hash is stable across restarts and across every client, so nothing is
// stored. The hue colors the thread's card, sidebar row, and chat-head tabs
// and, while the thread is open, the whole shell (see .solo-accent in
// style.css). The CSS hue classes are the six harness tints, in this order.

const HUE_COUNT = 6;

/** The `solo-hue-N` class for a solo thread id. Pair it with `solo-identity`
 * on cards, rows, and tabs, or `solo-accent` on the shell. */
export function soloHue(threadId: string): string {
  // FNV-1a: small, fast, and well spread over a few small buckets.
  let h = 0x811c9dc5;
  for (let i = 0; i < threadId.length; i++) {
    h = Math.imul(h ^ threadId.charCodeAt(i), 0x01000193);
  }
  return `solo-hue-${(h >>> 0) % HUE_COUNT}`;
}
