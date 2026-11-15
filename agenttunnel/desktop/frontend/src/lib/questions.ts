/** An agent question as the thread read model ships it (thread.get `questions`). */
export type QuestionKind = "choice" | "multi" | "text";

export type AgentQuestion = {
  questionId: string;
  agentId: string;
  kind: QuestionKind;
  question: string;
  options?: string[] | null;
  status: "pending" | "answered" | "closed";
  selected?: string[] | null;
  text?: string;
  reason?: string;
  at: string;
};

/** The reason the daemon records when the human dismisses a question. */
export const QUESTION_DECLINED = "declined by user";

/**
 * canSubmit mirrors the daemon's answer rules so the Send button is only enabled
 * for answers the daemon will accept: a choice takes one option or other text
 * (never both), a multi takes any options and/or text, a text takes text.
 */
export function canSubmit(kind: QuestionKind, selected: string[], text: string): boolean {
  const hasText = text.trim() !== "";
  if (kind === "text") return hasText;
  if (kind === "choice") return (selected.length === 1) !== hasText;
  return selected.length > 0 || hasText;
}
