import { memo, useEffect } from "react";
import { Markdown } from "./Markdown";

/** One side question on one thread. `answer` null while the daemon is still thinking. */
export type BtwEntry = {
  id: number;
  question: string;
  answer: string | null;
  error: string | null;
};

/**
 * BtwPanel shows a /btw side answer above the composer. It is ephemeral: it is
 * not a thread message and does not touch the running turn. Dismiss with × or Esc.
 */
export const BtwPanel = memo(function BtwPanel({
  entry,
  onClose,
}: {
  entry: BtwEntry;
  onClose: () => void;
}) {
  useEffect(() => {
    // The composer handles Esc first (closing its slash menu) and marks it handled.
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !e.defaultPrevented) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <section className="btw" aria-label="Side question">
      <header className="btw-head">
        <span className="btw-tag">/btw</span>
        <span className="btw-question">{entry.question}</span>
        <button type="button" className="btw-close" onClick={onClose} title="Dismiss (Esc)" aria-label="Dismiss side answer">
          ×
        </button>
      </header>
      <div className="btw-body">
        {entry.error !== null ? (
          <p className="btw-error">{entry.error}</p>
        ) : entry.answer === null ? (
          <p className="btw-wait">Thinking. The running turn keeps going.</p>
        ) : (
          <Markdown text={entry.answer} />
        )}
      </div>
    </section>
  );
});
