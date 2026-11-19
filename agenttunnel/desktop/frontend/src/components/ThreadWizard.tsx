import { useEffect, useRef, useState } from "react";
import type { LeadRuntimeView, ThreadState, ThreadSummary } from "../App";
import { getPref, setPref } from "../lib/prefs";
import {
  ScheduleSection,
  SoloPickers,
  StatusReportsSection,
  TeamSettings,
  type ThreadSetupProps,
} from "./ThreadSetup";

export type ThreadWizardProps = ThreadSetupProps & {
  /** Pressed on any step: leaves the wizard and shows the chat. */
  onStart: () => void;
  /** Label for the start button. */
  startLabel: string;
};

type WizardStep = 1 | 2 | 3;

/** The mode the user last started a thread with: "team", or the solo runtime. */
const LAST_MODE_KEY = "at.lastThreadMode";

function loadLastSolo(): LeadRuntimeView | null {
  try {
    const v = JSON.parse(getPref(LAST_MODE_KEY) ?? "null") as LeadRuntimeView | "team" | null;
    return v && typeof v === "object" && typeof v.harness === "string" && v.harness ? v : null;
  } catch {
    return null;
  }
}

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

const STEPS: { step: WizardStep; label: string; sub: string }[] = [
  { step: 1, label: "Mode", sub: "Team or solo" },
  { step: 2, label: "Models", sub: "Who runs it" },
  { step: 3, label: "Schedule", sub: "Optional" },
];

/** The pre-run setup for a new thread: a centered Mode → Models → Schedule
 * flow whose defaults are ready, so Start works from any step. A fresh thread
 * starts in the mode the user last started with. Rendered with key={thread id}
 * so switching threads re-derives the step. */
export function ThreadWizard({ onStart, startLabel, ...setup }: ThreadWizardProps) {
  const { settings, thread, summary } = setup;
  const [step, setStep] = useState<WizardStep>(() => initialWizardStep(thread, summary));
  const soloOn = !!thread?.soloRuntime;
  // Mirrors ThreadSetup's solo toggle: pinning solo on needs a harness — the
  // thread's current one, else the Orchestrator role's, else the first found.
  const orchHarness = settings?.roles.find((r) => r.id === "orchestrator")?.harness ?? "";
  const firstSoloHarness =
    (thread?.soloRuntime?.harness ?? "") || orchHarness || setup.harnessOptionsFor("")[0]?.value || "";

  // Remember the last choice: a fresh thread opens in the mode the previous
  // one started with. Applied once, through the same RPC a click would use.
  const applied = useRef(false);
  useEffect(() => {
    if (applied.current || !thread || initialWizardStep(thread, summary) !== 1 || thread.soloRuntime) return;
    applied.current = true;
    const last = loadLastSolo();
    if (last && setup.harnessOptionsFor("").some((o) => o.value === last.harness)) {
      setup.setSolo(last).catch(() => undefined);
    }
    // Once per mounted thread; the setup handlers are re-created every render.
  }, [thread?.id]);

  const start = () => {
    setPref(LAST_MODE_KEY, thread?.soloRuntime ? JSON.stringify(thread.soloRuntime) : JSON.stringify("team"));
    onStart();
  };

  return (
    <div className="wizard">
      <header className="wizard-head">
        <h2>Set up this thread</h2>
        <p>Defaults are ready. Start now, or adjust the team first.</p>
      </header>

      <ol className="wizard-steps" aria-label="Setup steps">
        {STEPS.map(({ step: n, label, sub }) => (
          <li key={n}>
            <button
              type="button"
              className={`wizard-step${step === n ? " on" : ""}${step > n ? " done" : ""}`}
              aria-current={step === n ? "step" : undefined}
              onClick={() => setStep(n)}
            >
              <span className="wizard-step-n">{n}</span>
              <span className="wizard-step-text">
                <span className="wizard-step-label">{label}</span>
                <span className="wizard-step-sub">{sub}</span>
              </span>
            </button>
          </li>
        ))}
      </ol>

      <div className="wizard-body">
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
            <p className="hint">One agent runs the whole turn. Switch modes in step 1.</p>
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
      </div>

      <div className="setup-wizard-footer wizard-foot">
        {step > 1 && (
          <button type="button" className="setup-back" onClick={() => setStep((step - 1) as WizardStep)}>
            Back
          </button>
        )}
        {step < 3 && (
          <button type="button" className="wizard-next" onClick={() => setStep((step + 1) as WizardStep)}>
            Next: {STEPS[step].label}
          </button>
        )}
        <button type="button" className="primary wizard-start" onClick={start}>
          {startLabel}
        </button>
      </div>
    </div>
  );
}
