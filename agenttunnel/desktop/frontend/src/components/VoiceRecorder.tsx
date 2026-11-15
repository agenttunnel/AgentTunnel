import { useEffect, useRef, useState, type CSSProperties } from "react";

/** Bars across the waveform; the strip scrolls one bar per level sample. */
const BARS = 32;
/** Level samples arrive at ~12/s: enough to read as motion, few style writes. */
const SAMPLE_MS = 80;
/** Resting bar heights, so reduced motion still shows a waveform shape. */
const BAR_HEIGHTS = Array.from({ length: BARS }, (_, i) => 0.2 + 0.6 * Math.abs(Math.sin(i * 1.7)));

function formatElapsed(total: number): string {
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, "0")}`;
}

/** Its own component so the once-a-second re-render never touches the waveform. */
function Elapsed() {
  const [seconds, setSeconds] = useState(0);
  useEffect(() => {
    const started = performance.now();
    const timer = window.setInterval(
      () => setSeconds(Math.floor((performance.now() - started) / 1000)),
      200,
    );
    return () => window.clearInterval(timer);
  }, []);
  return <time className="rec-time">{formatElapsed(seconds)}</time>;
}

/**
 * The recording UI that replaces the composer's text field while dictation runs.
 * Mounting opens the mic for a level meter; unmounting closes it. Without a mic
 * stream (or under reduced motion) the bars stay on their CSS fallback.
 */
export function VoiceRecorder({
  hint,
  onStop,
  onCancel,
}: {
  /** The hold combo that ends the take, e.g. "Command + T". */
  hint: string;
  onStop: () => void;
  onCancel: () => void;
}) {
  const waveRef = useRef<HTMLDivElement>(null);
  const [live, setLive] = useState(false);

  useEffect(() => {
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    if (!navigator.mediaDevices?.getUserMedia) return;
    let disposed = false;
    let raf = 0;
    let stream: MediaStream | null = null;
    let ctx: AudioContext | null = null;

    navigator.mediaDevices
      .getUserMedia({ audio: true })
      .then((s) => {
        if (disposed) {
          s.getTracks().forEach((t) => t.stop());
          return;
        }
        stream = s;
        ctx = new AudioContext();
        void ctx.resume();
        const analyser = ctx.createAnalyser();
        analyser.fftSize = 256;
        ctx.createMediaStreamSource(s).connect(analyser);
        const samples = new Uint8Array(analyser.fftSize);
        const levels = new Float32Array(BARS);
        let head = 0;
        let last = -Infinity;
        const frame = (now: number) => {
          raf = requestAnimationFrame(frame);
          if (now - last < SAMPLE_MS) return;
          last = now;
          analyser.getByteTimeDomainData(samples);
          let sum = 0;
          for (let i = 0; i < samples.length; i++) {
            const v = (samples[i] - 128) / 128;
            sum += v * v;
          }
          levels[head] = Math.min(1, Math.sqrt(sum / samples.length) * 3.5);
          head = (head + 1) % BARS;
          const bars = waveRef.current?.children;
          if (!bars) return;
          // Transform only: compositor work, no layout.
          for (let i = 0; i < BARS; i++) {
            (bars[i] as HTMLElement).style.transform = `scaleY(${
              0.12 + 0.88 * levels[(head + i) % BARS]
            })`;
          }
        };
        raf = requestAnimationFrame(frame);
        setLive(true);
      })
      .catch(() => {
        // No mic permission or no device: the CSS fallback keeps the strip moving.
      });

    return () => {
      disposed = true;
      cancelAnimationFrame(raf);
      stream?.getTracks().forEach((t) => t.stop());
      void ctx?.close();
    };
  }, []);

  return (
    <div className="rec-ui" role="status" aria-label="Recording">
      <span className="rec-pulse" aria-hidden="true" />
      <Elapsed />
      <div className={`rec-wave${live ? "" : " css"}`} ref={waveRef} aria-hidden="true">
        {BAR_HEIGHTS.map((h, i) => (
          <span
            key={i}
            className="rec-bar"
            style={{ "--h": h, animationDelay: `${(i * 37) % 600}ms` } as CSSProperties}
          />
        ))}
      </div>
      <span className="rec-hint">Release {hint} to insert</span>
      <button
        type="button"
        className="rec-cancel"
        onClick={onCancel}
        title="Discard this recording"
        aria-label="Cancel recording"
      >
        ×
      </button>
      <button
        type="button"
        className="rec-stop"
        onClick={onStop}
        title="Stop and insert the transcript"
        aria-label="Stop and insert"
      >
        <span className="rec-stop-glyph" aria-hidden="true" />
      </button>
    </div>
  );
}
