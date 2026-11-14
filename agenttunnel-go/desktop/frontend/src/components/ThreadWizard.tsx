import { useState } from "react";
import type { ThreadState, ThreadSummary } from "../App";
import {
  ScheduleSection,
  SoloPickers,
  StatusReportsSection,
  TeamSettings,
  type ThreadSetupProps,
} from "./ThreadSetup";

export type ThreadWizardProps = ThreadSetupProps & {
  /** Pressed on the last step: leaves the wizard and shows the chat. */
  onStart: () => void;
  /** Label for the last step's start button. */
  startLabel: string;
};

type WizardStep = 1 | 2 | 3;

/** The opening step comes from what the thread already has persisted: a
 * scheduled thread resumes at Schedule, one with a picked runtime at Models,
 * a fresh one at Mode. The wizard owns no choice state — every control
 * persists immediately through its usual RPC, so a remount loses nothing. */
function initialWizardStep(thread: ThreadState | null, summary: ThreadSummary | null): WizardStep {
  if (summary?.waiting) return 3;
  const soloChosen = !!thread?.soloRuntime;
  const leadTouched = (thread?.leadRuntimes ?? []).some((rt) => rt.harness || rt.model);
  return soloChosen || leadTouched ? 2 : 1;
}

const STEP_LABELS = ["Mode", "Models", "Schedule"];

/** The pre-run setup for a new thread, split into Mode → Models → Schedule.
 * Rendered with key={thread id} so switching threads re-derives the step. */
export function ThreadWizard({ onStart, startLabel, ...setup }: ThreadWizardProps) {
  const { settings, thread, summary } = setup;
  const [step, setStep] = useState<WizardStep>(() => initialWizardStep(thread, summary));
  const soloOn = !!thread?.soloRuntime;
  // Mirrors ThreadSetup's solo toggle: pinning solo on needs a harness — the
  // thread's current one, else the Orchestrator role's, else the first found.
  const orchHarness = settings?.roles.find((r) => r.id === "orchestrator")?.harness ?? "";
  const firstSoloHarness =
    (thread?.soloRuntime?.harness ?? "") || orchHarness || setup.harnessOptionsFor("")[0]?.value || "";

  return (
    <>
      <div className="setup-steps">
        {STEP_LABELS.map((label, i) => (
          <span key={label} className={step === i + 1 ? "on" : ""}>
            {i + 1} {label}
          </span>
        ))}
      </div>

      {step === 1 && (
        <div className="setup-mode-cards">
          <button
            type="button"
            className={`setup-mode-card${!soloOn ? " on" : ""}`}
            aria-pressed={!soloOn}
            onClick={() => soloOn && setup.setSolo(null)}
          >
            <span className="setup-mode-title">Multi-agent</span>
            <span className="setup-mode-sub">
              An orchestrator plans the work and dispatches a team.
            </span>
          </button>
          <button
            type="button"
            className={`setup-mode-card${soloOn ? " on" : ""}`}
            aria-pressed={soloOn}
            disabled={!firstSoloHarness}
            title={firstSoloHarness ? undefined : "No harness available yet"}
            onClick={() => !soloOn && firstSoloHarness && setup.setSolo({ harness: firstSoloHarness })}
          >
            <span className="setup-mode-title">Solo</span>
            <span className="setup-mode-sub">
              {firstSoloHarness
                ? `One agent runs the whole turn on ${firstSoloHarness}.`
                : "Needs a provider CLI — none detected yet."}
            </span>
          </button>
        </div>
      )}

      {step === 2 && !settings && <p className="hint">Loading roles…</p>}
      {step === 2 && settings && soloOn && (
        <>
          <SoloPickers
            thread={thread}
            setSolo={setup.setSolo}
            refreshModels={setup.refreshModels}
            addModel={setup.addModel}
            modelOptions={setup.modelOptions}
            harnessOptionsFor={setup.harnessOptionsFor}
          />
          <p className="hint">One agent runs the whole turn. Use Back to switch modes.</p>
        </>
      )}
      {step === 2 && settings && !soloOn && (
        <TeamSettings
          settings={settings}
          thread={thread}
          toggle={setup.toggle}
          addLead={setup.addLead}
          addingLead={setup.addingLead}
          setLeadRuntime={setup.setLeadRuntime}
          updateRole={setup.updateRole}
          persistRole={setup.persistRole}
          refreshModels={setup.refreshModels}
          addModel={setup.addModel}
          modelOptions={setup.modelOptions}
          harnessOptionsFor={setup.harnessOptionsFor}
          toggleRoleEnabled={setup.toggleRoleEnabled}
        />
      )}

      {step === 3 && !settings && <p className="hint">Loading roles…</p>}
      {step === 3 && settings && (
        <>
          <StatusReportsSection thread={thread} setStatusPeriod={setup.setStatusPeriod} />
          {summary && !summary.parentThreadId ? (
            <ScheduleSection
              summary={summary}
              candidates={setup.scheduleCandidates}
              threadTitle={setup.threadTitle}
              schedule={setup.schedule}
              unschedule={setup.unschedule}
            />
          ) : (
            <p className="hint">
              Subthreads run when their parent dispatches them — they can't be scheduled.
            </p>
          )}
        </>
      )}

      <div className="setup-wizard-footer">
        {step > 1 && (
          <button type="button" className="setup-back" onClick={() => setStep((step - 1) as WizardStep)}>
            Back
          </button>
        )}
        {step < 3 && (
          <button type="button" className="primary" onClick={() => setStep((step + 1) as WizardStep)}>
            Next
          </button>
        )}
        {step === 3 && (
          <button type="button" className="setup-continue" onClick={onStart}>
            {startLabel}
          </button>
        )}
      </div>
    </>
  );
}
