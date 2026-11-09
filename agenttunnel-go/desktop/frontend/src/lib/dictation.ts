import { useCallback, useEffect, useRef, useState } from "react";

/** Minimal typings for the Web Speech API (nonstandard, prefixed in WebKit). */
type SpeechRecognitionLike = {
  lang: string;
  continuous: boolean;
  interimResults: boolean;
  start(): void;
  stop(): void;
  onresult:
    | ((e: {
        resultIndex: number;
        results: ArrayLike<ArrayLike<{ transcript: string }> & { isFinal: boolean }>;
      }) => void)
    | null;
  onerror: ((e: { error?: string }) => void) | null;
  onend: (() => void) | null;
};

function getSpeechRecognition(): (new () => SpeechRecognitionLike) | null {
  const w = window as unknown as Record<string, unknown>;
  return (w.SpeechRecognition ?? w.webkitSpeechRecognition ?? null) as
    | (new () => SpeechRecognitionLike)
    | null;
}

const IS_MAC = /Mac|iPhone|iPad|iPod/.test(navigator.userAgent);
/** The hold modifier, as the composer hint names it. */
export const HOLD_MOD_LABEL = IS_MAC ? "Command" : "Ctrl";

/**
 * useHoldToTalk records while Cmd (macOS) or Ctrl is held with T, and inserts
 * the final transcript into `onText` when the key is released. Key repeat does
 * not restart recording, and the combo never types a "t". Where the webview has
 * no Web Speech API, `onUnavailable` explains the OS fallback instead.
 * Returns whether recording is active.
 */
export function useHoldToTalk({
  enabled,
  onText,
  onUnavailable,
}: {
  enabled: boolean;
  onText: (text: string) => void;
  onUnavailable?: (msg: string) => void;
}): boolean {
  const [listening, setListening] = useState(false);
  const recRef = useRef<SpeechRecognitionLike | null>(null);
  // Latest callbacks without rebinding the window listeners on every render.
  const cbRef = useRef({ onText, onUnavailable });
  useEffect(() => {
    cbRef.current = { onText, onUnavailable };
  });

  const start = useCallback(() => {
    if (recRef.current) return;
    const Ctor = getSpeechRecognition();
    if (!Ctor) {
      cbRef.current.onUnavailable?.(
        "Dictation is not available in this webview. Use your OS dictation instead (macOS: Edit ▸ Start Dictation, or press Fn Fn).",
      );
      return;
    }
    const rec = new Ctor();
    rec.lang = navigator.language || "en-US";
    rec.continuous = true;
    rec.interimResults = false;
    rec.onresult = (e) => {
      for (let i = e.resultIndex; i < e.results.length; i++) {
        const res = e.results[i];
        if (res.isFinal) cbRef.current.onText(res[0].transcript);
      }
    };
    // A late onend from a previous session must not clear a newer recording.
    const done = () => {
      if (recRef.current === rec) recRef.current = null;
      if (!recRef.current) setListening(false);
    };
    rec.onerror = done;
    rec.onend = done;
    // Track the recorder only once it runs, so a throwing start leaves none behind.
    rec.start();
    recRef.current = rec;
    setListening(true);
  }, []);

  const stop = useCallback(() => {
    const rec = recRef.current;
    if (!rec) return;
    recRef.current = null;
    rec.stop();
    setListening(false);
  }, []);

  useEffect(() => {
    if (!enabled) return;
    const held = (e: KeyboardEvent) => (IS_MAC ? e.metaKey : e.ctrlKey);
    const onDown = (e: KeyboardEvent) => {
      if (e.code !== "KeyT" || !held(e) || e.altKey || e.shiftKey) return;
      e.preventDefault();
      if (e.repeat) return;
      start();
    };
    // Releasing T or the modifier ends the take. On macOS, WebKit often drops
    // the T keyup while Cmd is held, so the Cmd release is the reliable stop.
    const onUp = (e: KeyboardEvent) => {
      if (e.code === "KeyT" || !held(e)) stop();
    };
    const onBlur = () => stop();
    window.addEventListener("keydown", onDown, true);
    window.addEventListener("keyup", onUp, true);
    window.addEventListener("blur", onBlur);
    return () => {
      window.removeEventListener("keydown", onDown, true);
      window.removeEventListener("keyup", onUp, true);
      window.removeEventListener("blur", onBlur);
      stop();
    };
  }, [enabled, start, stop]);

  return listening;
}
