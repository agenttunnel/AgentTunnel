import { useRef, useState, type KeyboardEvent } from "react";
import { QUESTION_DECLINED, canSubmit, type AgentQuestion, type QuestionKind } from "../lib/questions";

const KIND_LABEL: Record<QuestionKind, string> = {
  choice: "Pick one",
  multi: "Pick any",
  text: "Answer",
};

type Props = {
  question: AgentQuestion;
  /** The asking agent's display name. */
  askedBy: string;
  onAnswer: (selected: string[], text: string) => Promise<void>;
  onDismiss: () => Promise<void>;
  /** Runs after an answer or a skip lands, so focus can move on. */
  onSettled?: () => void;
};

/**
 * QuestionCard is an agent's question in the chat. Pending questions take an
 * answer or a skip; closed ones keep the answer (or why it closed) in the log.
 *
 * Keyboard: number keys pick options, arrows move between them, Enter sends
 * (Shift+Enter is a newline in the text box), the same as the composer.
 */
export function QuestionCard({ question: q, askedBy, onAnswer, onDismiss, onSettled }: Props) {
  const [selected, setSelected] = useState<string[]>([]);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const optionsRef = useRef<HTMLDivElement>(null);

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

  const options = q.kind === "text" ? [] : (q.options ?? []);
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
      onSettled?.();
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

  const focusOption = (i: number) =>
    optionsRef.current?.querySelectorAll<HTMLInputElement>("input")[i]?.focus();

  const onKey = (e: KeyboardEvent<HTMLElement>) => {
    // Buttons keep their own Enter; IME confirmation must not send.
    if (e.nativeEvent.isComposing || e.keyCode === 229 || busy) return;
    if (e.target instanceof HTMLButtonElement) return;
    const inText = e.target instanceof HTMLTextAreaElement;
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      submit();
      return;
    }
    if (inText) return;
    // 1-9 picks the matching option.
    if (/^[1-9]$/.test(e.key) && !e.metaKey && !e.ctrlKey && !e.altKey) {
      const i = Number(e.key) - 1;
      if (i < options.length) {
        e.preventDefault();
        pick(options[i]);
        focusOption(i);
      }
      return;
    }
    // Radios move with arrows natively; checkboxes get the same.
    if (q.kind === "multi" && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
      const inputs = [...(optionsRef.current?.querySelectorAll<HTMLInputElement>("input") ?? [])];
      const at = inputs.indexOf(e.target as HTMLInputElement);
      if (at < 0) return;
      e.preventDefault();
      focusOption((at + (e.key === "ArrowDown" ? 1 : -1) + inputs.length) % inputs.length);
    }
  };

  return (
    <section
      className="question-card pending"
      aria-label={`Question from ${askedBy}`}
      data-qid={q.questionId}
      onKeyDown={onKey}
    >
      <div className="q-head">
        <span className="q-wait">Waiting on you</span>
        <span className="q-asker">{askedBy}</span>
        <span className="q-kind">{KIND_LABEL[q.kind]}</span>
      </div>
      <div className="q-text">{q.question}</div>

      {options.length > 0 && (
        <div
          ref={optionsRef}
          className="q-options"
          role={q.kind === "choice" ? "radiogroup" : "group"}
          aria-label={q.question}
        >
          {options.map((opt, i) => (
            <label key={opt} className={`q-option${selected.includes(opt) ? " on" : ""}`}>
              <input
                type={q.kind === "choice" ? "radio" : "checkbox"}
                name={`q-${q.questionId}`}
                checked={selected.includes(opt)}
                disabled={busy}
                onChange={() => pick(opt)}
              />
              {i < 9 && (
                <kbd className="q-num" aria-hidden="true">
                  {i + 1}
                </kbd>
              )}
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
      />

      {error && <div className="err-text">{error}</div>}

      <div className="q-actions">
        <span className="hint">
          {options.length > 0 && (
            <>
              <kbd>1</kbd>–<kbd>{Math.min(options.length, 9)}</kbd> pick ·{" "}
            </>
          )}
          <kbd>Enter</kbd> send · <kbd>Shift</kbd>
          <kbd>Enter</kbd> newline
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
