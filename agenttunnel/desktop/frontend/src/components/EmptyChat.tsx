import { memo } from "react";
import { ALT, MOD } from "../lib/navKeys";

const STARTERS = [
  "Explain how this codebase is organized and where to start reading.",
  "Find a bug in this project, explain it, and fix it with a test.",
  "Add tests for the least-covered module.",
  "Review the uncommitted changes and suggest improvements.",
];

/**
 * The empty chat: what to do first. A starter fills the composer (it never
 * sends); the keys are the few worth knowing on day one.
 */
export const EmptyChat = memo(function EmptyChat({
  solo,
  onStarter,
}: {
  solo: boolean;
  onStarter: (text: string) => void;
}) {
  return (
    <div className="stream-empty empty-chat">
      <img src="/brand-mark.svg" alt="" />
      <div className="stream-empty-title">What should we work on?</div>
      <div className="stream-empty-sub">
        {solo
          ? "Describe the task. One agent runs the whole turn."
          : "Describe the task. The orchestrator plans it and dispatches your team."}
      </div>
      <div className="starters" role="list" aria-label="Starter prompts">
        {STARTERS.map((s) => (
          <button key={s} type="button" role="listitem" className="starter" onClick={() => onStarter(s)}>
            {s}
          </button>
        ))}
      </div>
      <dl className="empty-keys">
        <div>
          <dt>
            <kbd>@</kbd>
          </dt>
          <dd>attach a file</dd>
        </div>
        <div>
          <dt>
            <kbd>/</kbd>
          </dt>
          <dd>commands</dd>
        </div>
        <div>
          <dt>
            <kbd>{MOD}K</kbd>
          </dt>
          <dd>jump anywhere</dd>
        </div>
        <div>
          <dt>
            <kbd>{ALT}2</kbd>
          </dt>
          <dd>review changes</dd>
        </div>
        <div>
          <dt>
            <kbd>{MOD}T</kbd>
          </dt>
          <dd>hold to dictate</dd>
        </div>
      </dl>
    </div>
  );
});
