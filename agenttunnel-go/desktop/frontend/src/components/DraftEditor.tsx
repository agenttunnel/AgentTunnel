import { useRef, useState } from "react";

/**
 * DraftEditor edits a draft thread before its first turn: the title and the
 * description the first prompt is built from. Each field saves on blur. Clicking
 * Start blurs the focused field first, so its save is already in flight when
 * onStart runs; the parent waits for it before starting the turn. Mount it with
 * a key per draft: its local text is seeded once, from the props at mount.
 */
export function DraftEditor({
  title,
  description,
  onRename,
  onDescribe,
  onStart,
  onClose,
}: {
  title: string;
  description: string;
  onRename: (title: string) => void;
  onDescribe: (description: string) => void;
  onStart: () => void;
  onClose: () => void;
}) {
  const [titleText, setTitleText] = useState(title);
  const [text, setText] = useState(description);
  const savedDescription = useRef(description);

  const saveTitle = () => {
    const next = titleText.trim();
    if (next && next !== title) onRename(next);
  };
  const saveDescription = () => {
    if (text === savedDescription.current) return;
    savedDescription.current = text;
    onDescribe(text);
  };

  return (
    <div className="overlay" onClick={onClose}>
      <div className="sheet" onClick={(e) => e.stopPropagation()}>
        <div className="sheet-head">
          <strong>Draft</strong>
          <button onClick={onClose}>Close</button>
        </div>
        <div className="sheet-body">
          <input
            aria-label="Draft title"
            autoFocus
            value={titleText}
            placeholder="Draft title"
            onChange={(e) => setTitleText(e.target.value)}
            onBlur={saveTitle}
            onKeyDown={(e) => {
              if (e.key === "Enter") e.currentTarget.blur();
            }}
          />
          <textarea
            aria-label="Draft description"
            className="draft-description"
            rows={10}
            value={text}
            placeholder="What should the agent do first?"
            onChange={(e) => setText(e.target.value)}
            onBlur={saveDescription}
          />
          <div className="sheet-actions">
            <button className="primary" onClick={onStart}>
              Start
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
