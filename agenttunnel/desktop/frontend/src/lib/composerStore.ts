import type { ImageRef } from "./rpc";
import type { Attachment, FileMatch } from "./mentions";
import { getPref, setLocalPref } from "./prefs";
import { createStore } from "./store";

/** Image caps mirror the daemon's /attachments endpoint so a bad paste fails
 * in the composer, before any turn or tokens are spent. */
export const MAX_IMAGE_BYTES = 8 * 1024 * 1024;
export const MAX_TURN_IMAGES = 8;
export const MAX_TURN_IMAGE_BYTES = 32 * 1024 * 1024;
export const IMAGE_MIMES = new Set(["image/png", "image/jpeg", "image/webp", "image/gif"]);
/** One image in a composer draft: the original bytes (kept so a failed send can
 * restore the preview), a local objectURL, and the stored ImageRef once the
 * daemon has accepted the upload. */
export type DraftImage = {
  localId: number;
  name: string;
  file: Blob;
  previewUrl: string;
  uploading: boolean;
  ref?: ImageRef;
  error?: string;
};

/** Long pastes collapse to a `[Pasted text #id +N lines]` token; the full text is
 *  kept per draft and swapped back in on send, persistence, or click. */
const PASTE_TOKEN = /\[Pasted text #(\d+) \+\d+ lines\]/g;
export function pasteTokens(text: string): { start: number; end: number; id: number }[] {
  return [...text.matchAll(PASTE_TOKEN)].map((m) => {
    const start = m.index ?? 0;
    return { start, end: start + m[0].length, id: Number(m[1]) };
  });
}
export function expandPastes(text: string, store?: Record<number, string>): string {
  if (!store) return text;
  return text.replace(PASTE_TOKEN, (tok, id: string) => store[Number(id)] ?? tok);
}

/** Composer drafts keyed by project and thread, remembered across restarts. */
export function draftKeyOf(projectId: string | null, threadId: string | null): string {
  return `${projectId ?? ""}\u001f${threadId ?? ""}`;
}

function loadDrafts(): Record<string, string> {
  try {
    const raw = getPref("at.drafts");
    if (raw) {
      const parsed = JSON.parse(raw) as unknown;
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        const out: Record<string, string> = {};
        for (const [k, v] of Object.entries(parsed)) if (typeof v === "string") out[k] = v;
        return out;
      }
    }
  } catch {
    /* corrupt: start with no drafts */
  }
  return {};
}

/**
 * Composer state lives outside React's App tree so a keystroke re-renders the
 * composer only. App reads it imperatively (send, palette, council topic).
 * Draft content is keyed by draftKeyOf, so each (project, thread) keeps its own
 * text, pastes, attachments and images.
 */
export const composerStore = createStore({
  // Each (project, thread) keeps its own composer text, so switching threads
  // swaps the draft instead of carrying it over.
  drafts: loadDrafts(),
  // Pasted text per draft: draftKey -> token id -> full text.
  pastes: {} as Record<string, Record<number, string>>,
  // Files and folders attached per draft through @; sent with the turn as context.
  attachments: {} as Record<string, Attachment[]>,
  // Image drafts per paste or drop: previewed locally, uploaded over HTTP on
  // attach, then sent by attachment id. Keyed like attachments.
  draftImages: {} as Record<string, DraftImage[]>,
  // Caret in the composer: an @-token is the word ending at the caret.
  caret: 0,
  mentionActive: 0,
  // Start of an @-token the user dismissed with Escape.
  mentionDismissed: null as number | null,
  // Matches tagged with the query they answer, so an old list never shows for a new query.
  mentionResults: null as { query: string; matches: FileMatch[] } | null,
  // Slash popup: highlighted row, and the input it was dismissed on with Esc.
  slashIdx: 0,
  slashClosed: null as string | null,
  // Shell-style prompt history: the index of the sent prompt the composer shows
  // (null = the live draft), with the draft to restore on ArrowDown past the newest.
  recall: null as { key: string; idx: number; draft: string } | null,
});

export const setDrafts = composerStore.setter("drafts");
export const setPastes = composerStore.setter("pastes");
export const setAttachments = composerStore.setter("attachments");
export const setDraftImages = composerStore.setter("draftImages");
export const setCaret = composerStore.setter("caret");
export const setMentionActive = composerStore.setter("mentionActive");
export const setMentionDismissed = composerStore.setter("mentionDismissed");
export const setMentionResults = composerStore.setter("mentionResults");
export const setSlashIdx = composerStore.setter("slashIdx");
export const setSlashClosed = composerStore.setter("slashClosed");
export const setRecall = composerStore.setter("recall");

/** The draft text for a key, with pasted blocks expanded and trimmed. */
export function draftText(key: string): string {
  const { drafts, pastes } = composerStore.get();
  return expandPastes(drafts[key] ?? "", pastes[key]).trim();
}

// Remember composer drafts across restarts; empty drafts are dropped. Writes
// from one event (text and pastes together) coalesce into one save.
let persisted = composerStore.get();
let saveQueued = false;
composerStore.subscribe(() => {
  if (saveQueued) return;
  saveQueued = true;
  queueMicrotask(saveDrafts);
});
function saveDrafts() {
  saveQueued = false;
  const next = composerStore.get();
  if (next.drafts === persisted.drafts && next.pastes === persisted.pastes) return;
  persisted = next;
  const { drafts, pastes } = next;
  try {
    const kept = Object.fromEntries(
      Object.entries(drafts)
        .map(([k, v]) => [k, expandPastes(v, pastes[k])] as const)
        .filter(([, v]) => v !== ""),
    );
    setLocalPref("at.drafts", JSON.stringify(kept));
  } catch {
    /* stay in memory */
  }
}
