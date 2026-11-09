import { useEffect, type RefObject } from "react";
import { HOLD_MOD_LABEL } from "./dictation";

/** Hints the empty composer cycles through. */
export const COMPOSER_HINTS = [
  "Write your task and press enter to submit it",
  `Hold ${HOLD_MOD_LABEL} + t for voice-to-text`,
] as const;

/**
 * Cycles an empty composer's placeholder through COMPOSER_HINTS while `rotate`
 * holds, and shows `idle` otherwise. It writes the DOM attribute directly, so a
 * tick costs no React render. One interval runs, paused while the document is
 * hidden, and it is cleared on unmount or when `rotate` turns off.
 */
export function useRotatingPlaceholder(
  ref: RefObject<HTMLTextAreaElement>,
  { rotate, idle, ms = 4000 }: { rotate: boolean; idle: string; ms?: number },
) {
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (!rotate) {
      el.placeholder = idle;
      return;
    }
    let i = 0;
    let timer: number | undefined;
    const tick = () => {
      i = (i + 1) % COMPOSER_HINTS.length;
      el.placeholder = COMPOSER_HINTS[i];
    };
    const run = () => {
      if (timer === undefined && !document.hidden) timer = window.setInterval(tick, ms);
    };
    const halt = () => {
      if (timer !== undefined) window.clearInterval(timer);
      timer = undefined;
    };
    const onVisibility = () => (document.hidden ? halt() : run());
    el.placeholder = COMPOSER_HINTS[0];
    run();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      halt();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [ref, rotate, idle, ms]);
}
