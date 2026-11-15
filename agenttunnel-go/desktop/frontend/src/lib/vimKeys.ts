import { useEffect } from "react";

/**
 * Vimium-style keys, opt-in with /vim. They work in the desktop app and the
 * web build alike, since both run this frontend. While on: f shows letter hints
 * over clickable elements, j/k scroll a line, Ctrl+D/Ctrl+U scroll half a page,
 * g/G jump to the top/bottom, and Escape leaves a text field. Keys are ignored
 * while typing and while a dialog is open.
 */

const STORAGE_KEY = "at.vimKeys";
const LINE_PX = 60;
const HINT_KEYS = "asdfghjkl";
const HINT_SELECTOR = [
  "a[href]",
  "button",
  "input:not([type=hidden])",
  "select",
  "textarea",
  "summary",
  "[contenteditable=true]",
  "[role=button]",
  "[role=link]",
  "[role=tab]",
  "[role=menuitem]",
  "[role=checkbox]",
  "[role=switch]",
  "[role=option]",
].join(",");

export function loadVimKeys(): boolean {
  try {
    return window.localStorage.getItem(STORAGE_KEY) === "1";
  } catch {
    return false;
  }
}

export function saveVimKeys(on: boolean) {
  try {
    if (on) window.localStorage.setItem(STORAGE_KEY, "1");
    else window.localStorage.removeItem(STORAGE_KEY);
  } catch {
    /* private mode: the setting lasts only for this launch */
  }
}

/**
 * Labels for `count` hints, all the same length so no label is a prefix of
 * another. Home-row letters keep them quick to type.
 */
export function hintLabels(count: number): string[] {
  let len = 1;
  while (HINT_KEYS.length ** len < count) len++;
  const labels: string[] = [];
  for (let i = 0; i < count; i++) {
    let n = i;
    let label = "";
    for (let j = 0; j < len; j++) {
      label = HINT_KEYS[n % HINT_KEYS.length] + label;
      n = Math.floor(n / HINT_KEYS.length);
    }
    labels.push(label);
  }
  return labels;
}

function isTyping(t: EventTarget | null): boolean {
  return (
    t instanceof HTMLInputElement ||
    t instanceof HTMLTextAreaElement ||
    t instanceof HTMLSelectElement ||
    (t instanceof HTMLElement && t.isContentEditable)
  );
}

/** The element j/k should scroll: the nearest scrolling ancestor of focus or of the viewport centre. */
function scrollTarget(): HTMLElement | null {
  const active = document.activeElement;
  const start =
    active instanceof HTMLElement && active !== document.body
      ? active
      : document.elementFromPoint(window.innerWidth / 2, window.innerHeight / 2);
  for (let n: Element | null = start; n && n !== document.body; n = n.parentElement) {
    if (!(n instanceof HTMLElement)) continue;
    const oy = getComputedStyle(n).overflowY;
    if ((oy === "auto" || oy === "scroll") && n.scrollHeight > n.clientHeight) return n;
  }
  return document.scrollingElement instanceof HTMLElement ? document.scrollingElement : null;
}

/** Clickable elements whose box is on screen and not hidden. */
function visibleHintTargets(): HTMLElement[] {
  const vw = window.innerWidth;
  const vh = window.innerHeight;
  const out: HTMLElement[] = [];
  for (const el of document.querySelectorAll<HTMLElement>(HINT_SELECTOR)) {
    if (el.matches(":disabled") || el.closest("[inert],[aria-hidden=true]")) continue;
    const r = el.getBoundingClientRect();
    if (r.width === 0 || r.height === 0 || r.right < 0 || r.bottom < 0 || r.left > vw || r.top > vh) continue;
    const s = getComputedStyle(el);
    if (s.visibility === "hidden" || s.display === "none") continue;
    out.push(el);
  }
  return out;
}

type HintMode = {
  root: HTMLElement;
  targets: HTMLElement[];
  labels: string[];
  spans: HTMLElement[];
  typed: string;
};

function openHints(): HintMode | null {
  const targets = visibleHintTargets();
  if (targets.length === 0) return null;
  const labels = hintLabels(targets.length);
  const root = document.createElement("div");
  root.className = "vim-hints";
  const spans = targets.map((el, i) => {
    const r = el.getBoundingClientRect();
    const span = document.createElement("span");
    span.className = "vim-hint";
    span.textContent = labels[i];
    span.style.left = `${Math.max(0, r.left)}px`;
    span.style.top = `${Math.max(0, r.top)}px`;
    root.appendChild(span);
    return span;
  });
  document.body.appendChild(root);
  return { root, targets, labels, spans, typed: "" };
}

function activate(el: HTMLElement) {
  el.focus();
  if (!isTyping(el)) el.click();
}

/**
 * The keyboard hook. Returns nothing; mounted once in App and enabled by the
 * /vim setting. Capture phase so handled keys never reach other listeners.
 */
export function useVimKeys(enabled: boolean) {
  useEffect(() => {
    if (!enabled) return;
    let hints: HintMode | null = null;

    const closeHints = () => {
      hints?.root.remove();
      hints = null;
    };

    const onHintKey = (e: KeyboardEvent, h: HintMode) => {
      // Shortcuts like Cmd+Q pass through and dismiss the hints.
      if (e.metaKey || e.ctrlKey || e.altKey) return closeHints();
      e.preventDefault();
      e.stopPropagation();
      if (e.key === "Escape") return closeHints();
      if (e.key === "Backspace") {
        h.typed = h.typed.slice(0, -1);
      } else if (e.key.length === 1 && HINT_KEYS.includes(e.key.toLowerCase())) {
        h.typed += e.key.toLowerCase();
      } else {
        return;
      }
      const matched = h.labels.findIndex((label) => label === h.typed);
      if (matched !== -1) {
        const target = h.targets[matched];
        closeHints();
        activate(target);
        return;
      }
      let visible = 0;
      h.spans.forEach((span, i) => {
        const ok = h.labels[i].startsWith(h.typed);
        span.hidden = !ok;
        if (ok) visible++;
      });
      if (visible === 0) closeHints();
    };

    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing) return;
      if (hints) return onHintKey(e, hints);
      if (isTyping(e.target) || e.altKey) return;
      // Other dialogs (cheat sheet, settings, palette) own their keys.
      if (document.querySelector('[aria-modal="true"]')) return;

      if (e.ctrlKey && !e.metaKey && !e.shiftKey && (e.key === "d" || e.key === "u")) {
        const el = scrollTarget();
        if (!el) return;
        e.preventDefault();
        el.scrollBy({ top: (e.key === "d" ? 1 : -1) * el.clientHeight * 0.5 });
        return;
      }
      if (e.metaKey || e.ctrlKey) return;

      if (e.key === "f" && !e.shiftKey) {
        e.preventDefault();
        hints = openHints();
        return;
      }
      if (e.key === "j" || e.key === "k") {
        const el = scrollTarget();
        if (!el) return;
        e.preventDefault();
        el.scrollBy({ top: e.key === "j" ? LINE_PX : -LINE_PX });
        return;
      }
      if (e.key === "g" || e.key === "G") {
        const el = scrollTarget();
        if (!el) return;
        e.preventDefault();
        el.scrollTo({ top: e.key === "g" ? 0 : el.scrollHeight });
      }
    };

    // A hint's box goes stale when the page moves, so drop hints on scroll or resize.
    const onMove = () => closeHints();
    // Escape leaves a text field after the app had its chance to use it.
    const onEscape = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented || !isTyping(e.target)) return;
      (e.target as HTMLElement).blur();
    };

    window.addEventListener("keydown", onKey, true);
    window.addEventListener("keydown", onEscape);
    window.addEventListener("resize", onMove);
    window.addEventListener("scroll", onMove, true);
    return () => {
      window.removeEventListener("keydown", onKey, true);
      window.removeEventListener("keydown", onEscape);
      window.removeEventListener("resize", onMove);
      window.removeEventListener("scroll", onMove, true);
      closeHints();
    };
  }, [enabled]);
}
