/**
 * Sticky recap above the composer: one line saying where this thread stands —
 * its goal, what the fleet is doing right now, and what the last report said to
 * do next. The text is derived from the activity stream the Agents panel
 * already reads (see `deriveThreadRecap`), so it stays honest while sub-agents
 * work, updates as they report, and renders nothing when there is nothing to
 * say. Static text by design: no animation, no ticking clock, no repaint.
 */
export function ThreadRecapBar({ text }: { text: string | null }) {
  if (text === null || text.length === 0) {
    return null;
  }
  return (
    <div className="mx-auto w-full max-w-3xl px-1 pb-1.5">
      <p
        className="line-clamp-2 font-mono text-[.7rem] leading-relaxed text-muted-foreground/80"
        aria-live="polite"
      >
        <span aria-hidden className="text-muted-foreground/50">
          ※ recap:{" "}
        </span>
        {text}
      </p>
    </div>
  );
}
