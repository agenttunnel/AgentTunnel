// The aside's Council tab: convene several perspectives on one question and
// let them debate to a synthesized outcome. The topic and seats live in
// thread-scoped stores, so typing here re-renders this panel only, and the
// values outlive the panel when its tab closes.

import { memo } from "react";
import { Dropdown, type DropdownOption } from "./Dropdown";
import { createThreadScoped, useThreadScopedValue } from "../lib/store";

/** One editable seat in the council sheet before it is convened. */
export type CouncilMemberRow = {
  name: string;
  role: string;
  harness: string;
  model: string;
  perspective: string;
};

export type CouncilSetup = { rounds: number; members: CouncilMemberRow[] };

const defaultCouncilSetup = (): CouncilSetup => ({
  rounds: 2,
  members: [
    {
      name: "planner",
      role: "planner",
      harness: "",
      model: "",
      perspective: "weigh architecture and long-term maintainability",
    },
    {
      name: "executor",
      role: "executor",
      harness: "",
      model: "",
      perspective: "prize the simplest thing that actually ships",
    },
    {
      name: "reviewer",
      role: "reviewer",
      harness: "",
      model: "",
      perspective: "hunt risk, edge cases, and hidden cost",
    },
  ],
});

/** The question per thread. */
export const councilTopic = createThreadScoped<string>(() => "");
/** Rounds and seats default globally and can be saved as the default for new threads. */
export const councilSetup = createThreadScoped(defaultCouncilSetup, "at.councilSetup");

export const CouncilPanel = memo(function CouncilPanel({
  activeId,
  roles,
  busy,
  harnessOptionsFor,
  modelOptions,
  refreshModels,
  onStart,
}: {
  activeId: string | null;
  roles: { id: string; name: string }[];
  busy: boolean;
  harnessOptionsFor: (current: string) => DropdownOption[];
  modelOptions: (harness: string, current: string) => DropdownOption[];
  refreshModels: (harness: string) => Promise<void>;
  onStart: () => void;
}) {
  const topic = useThreadScopedValue(councilTopic, activeId);
  const setup = useThreadScopedValue(councilSetup, activeId);
  const councilRounds = setup.rounds;
  const councilMembers = setup.members;
  const setCouncilTopic = (text: string) => councilTopic.set(activeId, text);
  const setCouncilRounds = (rounds: number) => councilSetup.set(activeId, (s) => ({ ...s, rounds }));
  const setCouncilMembers = (update: (prev: CouncilMemberRow[]) => CouncilMemberRow[]) =>
    councilSetup.set(activeId, (s) => ({ ...s, members: update(s.members) }));
  const updateCouncilMember = (idx: number, patch: Partial<CouncilMemberRow>) => {
    setCouncilMembers((prev) => prev.map((m, i) => (i === idx ? { ...m, ...patch } : m)));
  };

  return (
    <div className="section council-section">
      {!activeId && <p className="hint">Open a thread to convene a council.</p>}
      <div className="council-pane">
        <label className="field">
          <span>The question to debate</span>
          <textarea
            rows={2}
            value={topic}
            onChange={(e) => setCouncilTopic(e.target.value)}
            placeholder="e.g. Which approach should we take for the parser rewrite?"
          />
        </label>
        <label className="field">
          <span>Debate rounds — every member answers, then sees and challenges the table</span>
          <input
            type="number"
            min={1}
            max={4}
            value={councilRounds}
            onChange={(e) => setCouncilRounds(Math.max(1, Math.min(4, Number(e.target.value) || 2)))}
            style={{ maxWidth: 100 }}
          />
        </label>
        <div className="council-members">
          <span className="field-label">Members</span>
          {councilMembers.map((m, idx) => (
            <div className="council-member" key={idx}>
              <div className="cm-row">
                <input
                  className="cm-name"
                  value={m.name}
                  onChange={(e) => updateCouncilMember(idx, { name: e.target.value })}
                  placeholder="name"
                  title="Seat name — how this member shows up in the debate"
                />
                <Dropdown
                  value={m.role}
                  options={roles.map((r) => ({ value: r.id, label: r.name }))}
                  onChange={(v) => updateCouncilMember(idx, { role: v })}
                />
                <Dropdown
                  value={m.harness}
                  options={harnessOptionsFor(m.harness)}
                  onChange={(v) => {
                    updateCouncilMember(idx, { harness: v, model: "" });
                    if (v) refreshModels(v).catch(() => undefined);
                  }}
                />
                <Dropdown
                  value={m.model}
                  options={modelOptions(m.harness, m.model)}
                  searchable
                  placeholder="model (role default)"
                  onRefresh={() => refreshModels(m.harness).catch(() => undefined)}
                  onChange={(v) => updateCouncilMember(idx, { model: v })}
                />
                {councilMembers.length > 2 && (
                  <button
                    className="cm-remove"
                    title="Remove this seat"
                    onClick={() => setCouncilMembers((prev) => prev.filter((_, i) => i !== idx))}
                  >
                    ×
                  </button>
                )}
              </div>
              <input
                className="cm-perspective"
                value={m.perspective}
                onChange={(e) => updateCouncilMember(idx, { perspective: e.target.value })}
                placeholder="Perspective this member argues from (optional)"
              />
            </div>
          ))}
          {councilMembers.length < 6 && (
            <button
              className="cm-add"
              onClick={() =>
                setCouncilMembers((prev) => [
                  ...prev,
                  {
                    name: roles[prev.length % Math.max(1, roles.length)]?.id ?? "member",
                    role: roles[prev.length % Math.max(1, roles.length)]?.id ?? "executor",
                    harness: "",
                    model: "",
                    perspective: "",
                  },
                ])
              }
            >
              + Add seat
            </button>
          )}
        </div>
        <p className="hint">
          Members run in parallel each round. A facilitator — who was not part of the debate —
          writes the final consensus into this thread, with dissent recorded.
        </p>
        <div className="sheet-actions">
          <button
            className="primary"
            disabled={!activeId || !topic.trim() || councilMembers.length < 2 || busy}
            onClick={onStart}
          >
            Convene council
          </button>
        </div>
      </div>
    </div>
  );
});
