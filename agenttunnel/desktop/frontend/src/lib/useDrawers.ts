import { useCallback, useEffect, useRef, useState, type RefObject } from "react";

// Below these widths a side panel stops docking and slides over the thread
// instead. Keep in sync with the media queries in styles/layout.css.
const SIDEBAR_DRAWER_QUERY = "(max-width: 767px)";
const ASIDE_DRAWER_QUERY = "(max-width: 1099px)";

export type DrawerSide = "sidebar" | "aside";

function useMedia(query: string): boolean {
  const [match, setMatch] = useState(() => window.matchMedia(query).matches);
  useEffect(() => {
    const mq = window.matchMedia(query);
    const sync = () => setMatch(mq.matches);
    sync();
    mq.addEventListener("change", sync);
    return () => mq.removeEventListener("change", sync);
  }, [query]);
  return match;
}

/**
 * useDrawers turns the side panels into slide-over drawers on narrow windows.
 * The docked layout (widths, collapsed rails) is untouched: a drawer's open
 * state lives only here, so opening one on a phone never changes the saved
 * desktop layout. One drawer is open at a time; Esc, the scrim, or a change of
 * `closeKey` (the thread or view the user navigated to) closes it. Focus moves
 * into the drawer when it opens and back to the opener when it closes.
 */
export function useDrawers(shellRef: RefObject<HTMLElement | null>, closeKey: unknown) {
  const sidebarDrawer = useMedia(SIDEBAR_DRAWER_QUERY);
  const asideDrawer = useMedia(ASIDE_DRAWER_QUERY);
  const [want, setWant] = useState<DrawerSide | null>(null);
  const opener = useRef<HTMLElement | null>(null);

  // A side that docks again (the window widened) is no longer a drawer.
  const open =
    want === "sidebar" && sidebarDrawer ? "sidebar" : want === "aside" && asideDrawer ? "aside" : null;

  const close = useCallback(() => setWant(null), []);
  // Opens a drawer from code (a panel opened by key or palette). A side that
  // docks is not a drawer, so this is a no-op there.
  const reveal = useCallback((side: DrawerSide) => setWant(side), []);
  const toggle = useCallback((side: DrawerSide) => {
    if (document.activeElement instanceof HTMLElement) opener.current = document.activeElement;
    setWant((cur) => (cur === side ? null : side));
  }, []);

  useEffect(() => setWant(null), [closeKey]);

  useEffect(() => {
    if (!open) return;
    const panel = shellRef.current?.querySelector<HTMLElement>(
      open === "sidebar" ? ".col.sidebar" : ".col.aside",
    );
    // Focus the panel itself, not its search box: on a phone that would pop
    // the keyboard over the list the user opened the drawer to see.
    if (panel) {
      panel.tabIndex = -1;
      panel.focus({ preventScroll: true });
    }
    const onKey = (e: KeyboardEvent) => {
      // A sheet or menu above the drawer takes Esc first.
      if (e.key !== "Escape" || e.defaultPrevented || e.isComposing) return;
      if (document.querySelector(".overlay, .ctx-menu")) return;
      e.preventDefault();
      setWant(null);
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      const back = opener.current;
      opener.current = null;
      if (back?.isConnected) back.focus({ preventScroll: true });
    };
  }, [open, shellRef]);

  const className =
    (sidebarDrawer ? " sb-drawer" : "") +
    (asideDrawer ? " as-drawer" : "") +
    (open ? ` drawer-open drawer-open-${open}` : "");

  return { sidebarDrawer, asideDrawer, open, toggle, close, reveal, className };
}
