import { useState, type KeyboardEvent } from "react";
import { QUESTION_DECLINED, canSubmit, type AgentQuestion, type QuestionKind } from "../lib/questions";

const KIND_LABEL: Record<QuestionKind, string> = {
  choice: "Pick one",
  multi: "Pick any",
  text: "Answer",
};

// Shown as a keyboard hint; Mac users see the Cmd glyph.
const SEND_KEY = typeof navigator !== "undefined" && /Mac/i.test(navigator.userAgent) ? "⌘" : "Ctrl";

type Props = {
  question: AgentQuestion;
  /** The asking agent's display name. */
  askedBy: string;
  onAnswer: (selected: string[], text: string) => Promise<void>;
  onDismiss: () => Promise<void>;
};

/**
 * QuestionCard is an agent's question in the chat. Pending questions take an
 * answer or a skip; closed ones keep the answer (or why it closed) in the log.
 */
export function QuestionCard({ question: q, askedBy, onAnswer, onDismiss }: Props) {
  const [selected, setSelected] = useState<string[]>([]);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (q.status !== "pending") {
    const answer = [(q.selected ?? []).join(", "), q.text ?? ""].filter(Boolean).join(" · ");
    const answered = q.status === "answered";
    return (
      <section className="question-card closed" aria-label={`Question from ${askedBy}`}>
        <div className="q-head">
          <span className="q-asker">{askedBy}</span>
          <span className={answered ? "q-kind" : "q-kind closed-kind"}>{answered ? "Answered" : "Closed"}</span>
        </div>
        <div className="q-text">{q.question}</div>
        {answered ? (
          <div className="q-answer">{answer}</div>
        ) : (
          <div className="q-answer muted">
            {q.reason === QUESTION_DECLINED ? "Skipped" : q.reason || "No answer"}
          </div>
        )}
      </section>
    );
  }

  const ready = !busy && canSubmit(q.kind, selected, text);

  // A choice takes one option, so picking one clears other text and typing
  // other text clears the pick. A multi keeps both.
  const pick = (opt: string) => {
    if (q.kind === "choice") {
      setSelected([opt]);
      setText("");
      return;
    }
    setSelected((s) => (s.includes(opt) ? s.filter((x) => x !== opt) : [...s, opt]));
  };
  const edit = (value: string) => {
    setText(value);
    if (q.kind === "choice" && value !== "") setSelected([]);
  };

  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await action();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const submit = () => {
    if (!ready) return;
    void run(() => onAnswer(selected, text.trim()));
  };

  // Cmd/Ctrl+Enter sends from a text box; plain Enter keeps its newline. IME
  // confirmation must not send.
  const onTextKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing || e.keyCode === 229) return;
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      submit();
    }
  };

  return (
    <section className="question-card" aria-label={`Question from ${askedBy}`}>
      <div className="q-head">
        <span className="q-asker">{askedBy}</span>
        <span className="q-kind">{KIND_LABEL[q.kind]}</span>
      </div>
      <div className="q-text">{q.question}</div>

      {q.kind !== "text" && (
        <div className="q-options" role={q.kind === "choice" ? "radiogroup" : "group"}>
          {(q.options ?? []).map((opt) => (
            <label key={opt} className={`q-option${selected.includes(opt) ? " on" : ""}`}>
              <input
                type={q.kind === "choice" ? "radio" : "checkbox"}
                name={`q-${q.questionId}`}
                checked={selected.includes(opt)}
                disabled={busy}
                onChange={() => pick(opt)}
              />
              <span className="q-option-label">{opt}</span>
            </label>
          ))}
        </div>
      )}

      <textarea
        className="q-input"
        rows={q.kind === "text" ? 3 : 1}
        value={text}
        disabled={busy}
        aria-label={q.kind === "text" ? "Your answer" : "Other answer (optional)"}
        placeholder={q.kind === "text" ? "Your answer" : "Other (optional)"}
        onChange={(e) => edit(e.target.value)}
        onKeyDown={onTextKey}
      />

      {error && <div className="err-text">{error}</div>}

      <div className="q-actions">
        <span className="hint">
          <kbd>{SEND_KEY}</kbd>
          <kbd>Enter</kbd> to send
        </span>
        <button className="ghost" onClick={() => void run(onDismiss)} disabled={busy}>
          Skip
        </button>
        <button className="primary" onClick={submit} disabled={!ready}>
          Send answer
        </button>
      </div>
    </section>
  );
}
