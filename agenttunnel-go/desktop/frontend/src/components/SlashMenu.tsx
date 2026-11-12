import { memo, useEffect, useRef } from "react";
import type { SlashCommand } from "../lib/commands";

/**
 * SlashMenu lists the commands matching what is typed after "/". The composer
 * owns the keyboard (arrows, Tab, Enter, Esc); this only renders and picks.
 * Picks fire on mousedown so the textarea keeps focus.
 */
export const SlashMenu = memo(function SlashMenu({
  items,
  index,
  onPick,
}: {
  items: SlashCommand[];
  index: number;
  onPick: (cmd: SlashCommand) => void;
}) {
  const listRef = useRef<HTMLDivElement>(null);

  // Arrow keys move the highlight; keep the highlighted row inside the scroll box.
  useEffect(() => {
    listRef.current?.querySelector<HTMLElement>(".slash-item.on")?.scrollIntoView({ block: "nearest" });
  }, [index]);

  return (
    <div ref={listRef} className="slash-menu" role="listbox" aria-label="Slash commands">
      {items.map((cmd, i) => (
        <button
          key={cmd.name}
          type="button"
          role="option"
          aria-selected={i === index}
          className={`slash-item${i === index ? " on" : ""}`}
          onMouseDown={(e) => {
            e.preventDefault();
            onPick(cmd);
          }}
        >
          <span className="slash-name">
            /{cmd.name}
            {cmd.args && <span className="slash-args"> {cmd.args}</span>}
          </span>
          <span className="slash-desc">{cmd.description}</span>
          {cmd.hint && <span className="ctx-hint">{cmd.hint}</span>}
        </button>
      ))}
    </div>
  );
});
