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
 * BtwPanel shows a /btw side conversation above the composer: one exchange per
 * ask, follow-ups keep appending. It is ephemeral: it is not a thread message
 * and does not touch the running turn. Dismiss the whole panel with × or Esc.
 */
export const BtwPanel = memo(function BtwPanel({
  entries,
  onClose,
}: {
  entries: BtwEntry[];
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

  const last = entries.length - 1;
  const latest = entries[last];

  return (
    <section className="btw" aria-label="Side question">
      <header className="btw-head">
        <span className="btw-tag">/btw</span>
        <span className="btw-question">{latest.question}</span>
        <button type="button" className="btw-close" onClick={onClose} title="Dismiss (Esc)" aria-label="Dismiss side answer">
          ×
        </button>
      </header>
      <div className="btw-body">
        {entries.map((e, i) => (
          <div className="btw-msg" key={e.id}>
            <p className="btw-msg-q">{e.question}</p>
            {e.error !== null ? (
              <p className="btw-error">{e.error}</p>
            ) : e.answer === null ? (
              i === last ? <p className="btw-wait">Thinking. The running turn keeps going.</p> : null
            ) : (
              <Markdown text={e.answer} />
            )}
          </div>
        ))}
      </div>
      <p className="btw-hint">Follow up with /btw &lt;question&gt;</p>
    </section>
  );
});
