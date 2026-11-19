// The app's navigation shortcuts, in one place so the tabs' tooltips, the
// palette's hints and the key handlers never disagree.

import type { CenterTab } from "./commands";

export const IS_MAC = /Mac|iPhone|iPad|iPod/.test(navigator.userAgent);
/** The command modifier as a key cap: ⌘ on macOS, Ctrl elsewhere. */
export const MOD = IS_MAC ? "⌘" : "Ctrl+";
/** The option modifier as a key cap. */
export const ALT = IS_MAC ? "⌥" : "Alt+";

/** Center tabs in strip order; Alt/Option + the digit selects one. */
export const CENTER_TAB_KEYS: { tab: CenterTab; label: string }[] = [
  { tab: "chat", label: "Chat" },
  { tab: "review", label: "Review" },
  { tab: "files", label: "Files" },
  { tab: "graph", label: "Graph" },
  { tab: "p2p", label: "P2P" },
  { tab: "board", label: "Board" },
];

/** The key cap of a center tab's shortcut, e.g. "⌥2". */
export function centerTabHint(tab: CenterTab): string {
  const i = CENTER_TAB_KEYS.findIndex((t) => t.tab === tab);
  return i < 0 ? "" : `${ALT}${i + 1}`;
}

/** The center tab an Alt/Option+digit keydown selects, or null. Reads
 * `code`, since Option+digit types a symbol on macOS. */
export function centerTabFromKey(e: KeyboardEvent): CenterTab | null {
  if (!e.altKey || e.metaKey || e.ctrlKey || e.shiftKey || e.isComposing) return null;
  const m = /^Digit([1-9])$/.exec(e.code);
  if (!m) return null;
  return CENTER_TAB_KEYS[Number(m[1]) - 1]?.tab ?? null;
}

/** Cmd/Ctrl+K and Cmd/Ctrl+P both open the palette. */
export function isPaletteKey(e: KeyboardEvent | { metaKey: boolean; ctrlKey: boolean; altKey: boolean; shiftKey: boolean; key: string }): boolean {
  if (!(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey) return false;
  const k = e.key.toLowerCase();
  return k === "k" || k === "p";
}

/** True while focus is in something the user types into. */
export function isTyping(t: EventTarget | null): boolean {
  if (t instanceof HTMLInputElement || t instanceof HTMLTextAreaElement || t instanceof HTMLSelectElement) {
    return true;
  }
  return t instanceof HTMLElement && t.isContentEditable;
}
