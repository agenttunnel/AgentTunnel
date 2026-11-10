import type { FileMatch } from "../lib/mentions";

/**
 * The @ menu above the composer: fuzzy file and folder matches. It holds no
 * state; the composer owns the query, the results and the highlighted row.
 */
export function MentionMenu({
  matches,
  active,
  onPick,
  onActive,
}: {
  matches: FileMatch[];
  active: number;
  onPick: (match: FileMatch) => void;
  onActive: (index: number) => void;
}) {
  return (
    <div className="mention-menu" role="listbox" aria-label="Attach a file or folder">
      {matches.length === 0 ? (
        <div className="mention-empty">No matching files or folders</div>
      ) : (
        matches.map((m, i) => {
          const slash = m.path.lastIndexOf("/");
          const name = m.path.slice(slash + 1);
          const parent = slash >= 0 ? m.path.slice(0, slash) : "";
          return (
            <div
              key={m.path}
              role="option"
              aria-selected={i === active}
              className={`mention-item${i === active ? " active" : ""}`}
              // mousedown keeps the textarea focused; click alone would blur it first.
              onMouseDown={(e) => {
                e.preventDefault();
                onPick(m);
              }}
              onMouseEnter={() => onActive(i)}
            >
              <span className="mention-kind">{m.dir ? "folder" : "file"}</span>
              <span className="mention-name">{name}{m.dir ? "/" : ""}</span>
              {parent && <span className="mention-parent">{parent}</span>}
            </div>
          );
        })
      )}
    </div>
  );
}
