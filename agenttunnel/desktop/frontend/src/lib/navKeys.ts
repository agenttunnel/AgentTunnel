// The app's shared shortcut key caps and key matchers.

export const IS_MAC = /Mac|iPhone|iPad|iPod/.test(navigator.userAgent);
/** The command modifier as a key cap: ⌘ on macOS, Ctrl elsewhere. */
export const MOD = IS_MAC ? "⌘" : "Ctrl+";
/** The option modifier as a key cap. */
export const ALT = IS_MAC ? "⌥" : "Alt+";

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
