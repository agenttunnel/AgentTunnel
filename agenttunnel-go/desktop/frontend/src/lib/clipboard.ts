import { Clipboard } from "@wailsio/runtime";

/**
 * Writes text to the system clipboard and reports whether it landed.
 *
 * Three environments, in order of who can answer:
 * - a secure context has the async Clipboard API (a browser tab, and Windows,
 *   where the shell serves from http://wails.localhost);
 * - the desktop shell on macOS and Linux serves the frontend from the `wails://`
 *   scheme, which is not a secure context, so `navigator.clipboard` is absent
 *   and the native runtime has to do it;
 * - anything else (permission denied, an unfocused document) gets the legacy
 *   textarea copy so a click still produces a result.
 */
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      /* fall through to the shell, then the legacy path */
    }
  }
  try {
    await Clipboard.SetText(text);
    return true;
  } catch {
    /* not running inside the desktop shell */
  }
  return copyWithTextarea(text);
}

/** The pre-Clipboard-API path: select a throwaway textarea and copy it. */
function copyWithTextarea(text: string): boolean {
  const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  // Off-screen but selectable; fixed keeps the page from scrolling to it.
  textarea.style.position = "fixed";
  textarea.style.top = "0";
  textarea.style.left = "0";
  textarea.style.width = "1px";
  textarea.style.height = "1px";
  textarea.style.opacity = "0";
  textarea.style.pointerEvents = "none";
  document.body.appendChild(textarea);
  let copied = false;
  try {
    textarea.focus({ preventScroll: true });
    textarea.select();
    textarea.setSelectionRange(0, text.length);
    copied = document.execCommand("copy");
  } catch {
    copied = false;
  }
  textarea.remove();
  // The copy steals focus; hand it back so the transcript keeps its place.
  if (active && document.contains(active)) active.focus({ preventScroll: true });
  return copied;
}
