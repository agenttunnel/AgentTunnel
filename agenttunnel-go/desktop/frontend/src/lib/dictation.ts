import { useCallback, useEffect, useRef, useState } from "react";

/** Minimal typings for the Web Speech API (nonstandard, prefixed in WebKit). */
type SpeechRecognitionLike = {
  lang: string;
  continuous: boolean;
  interimResults: boolean;
  start(): void;
  stop(): void;
  abort(): void;
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

type Take = { rec: SpeechRecognitionLike; parts: string[]; cancelled: boolean; finished: boolean };

/**
 * useHoldToTalk records while Cmd (macOS) or Ctrl is held with T. Final phrases
 * gather during the take and land in `onText` once, when it ends: on key release
 * (`stop`), or on the recognizer's own end. `cancel` drops the take unseen. Key
 * repeat does not restart recording, and the combo never types a "t". Where the
 * webview has no Web Speech API, `onUnavailable` explains the OS fallback instead.
 */
export function useHoldToTalk({
  enabled,
  onText,
  onUnavailable,
}: {
  enabled: boolean;
  onText: (text: string) => void;
  onUnavailable?: (msg: string) => void;
}): { listening: boolean; stop: () => void; cancel: () => void } {
  const [listening, setListening] = useState(false);
  const takeRef = useRef<Take | null>(null);
  // Latest callbacks without rebinding the window listeners on every render.
  const cbRef = useRef({ onText, onUnavailable });
  useEffect(() => {
    cbRef.current = { onText, onUnavailable };
  });

  const start = useCallback(() => {
    if (takeRef.current) return;
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
    const take: Take = { rec, parts: [], cancelled: false, finished: false };
    rec.onresult = (e) => {
      if (take.cancelled) return;
      for (let i = e.resultIndex; i < e.results.length; i++) {
        const res = e.results[i];
        if (res.isFinal) take.parts.push(res[0].transcript);
      }
    };
    // Runs once per take, from stop, cancel, error, or the recognizer's own end.
    // A late end from a previous take must not clear a newer recording.
    const finish = () => {
      if (take.finished) return;
      take.finished = true;
      if (takeRef.current === take) {
        takeRef.current = null;
        setListening(false);
      }
      const text = take.parts.join(" ").trim();
      if (!take.cancelled && text) cbRef.current.onText(text);
    };
    rec.onerror = finish;
    rec.onend = finish;
    // Track the recorder only once it runs, so a throwing start leaves none behind.
    rec.start();
    takeRef.current = take;
    setListening(true);
  }, []);

  const stop = useCallback(() => {
    const take = takeRef.current;
    if (!take) return;
    takeRef.current = null;
    setListening(false);
    take.rec.stop();
  }, []);

  const cancel = useCallback(() => {
    const take = takeRef.current;
    if (!take) return;
    take.cancelled = true;
    takeRef.current = null;
    setListening(false);
    take.rec.abort();
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

  return { listening, stop, cancel };
}
