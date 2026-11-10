/** A file or folder the composer attached to a turn. Path is workdir-relative. */
export type Attachment = { path: string; dir: boolean };

/** One hit from the daemon's files.search. */
export type FileMatch = Attachment;

/** Most attachments one turn carries; mirrors files.MaxAttachments on the daemon. */
export const MAX_ATTACHMENTS = 20;

/** An @ being typed: the @ sits at `start` and the query runs up to the caret. */
export type MentionToken = { start: number; query: string };

/**
 * The @-token ending at the caret, or null. An @ opens a mention only at the
 * start of a word, and only while no whitespace separates it from the caret.
 */
export function mentionAt(text: string, caret: number): MentionToken | null {
  const before = text.slice(0, caret);
  const at = before.lastIndexOf("@");
  if (at < 0) return null;
  if (at > 0 && !/\s/.test(before[at - 1])) return null;
  const query = before.slice(at + 1);
  if (/\s/.test(query)) return null;
  return { start: at, query };
}

/** The text with the @-token removed; the attachment replaces it as a chip. */
export function dropMention(text: string, m: MentionToken, caret: number): string {
  return text.slice(0, m.start) + text.slice(caret);
}
