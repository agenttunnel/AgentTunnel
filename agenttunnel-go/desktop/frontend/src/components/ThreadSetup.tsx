import { useState } from "react";
import type { LeadRuntimeView, RoleConfigView, SettingsView, ThreadState, ThreadSummary } from "../App";
import { switchProps } from "../lib/switchProps";
import { Dropdown, type DropdownOption } from "./Dropdown";

type ThreadSetupProps = {
  settings: SettingsView | null;
  thread: ThreadState | null;
  toggle: (field: "autoMode" | "peerMailbox", value: boolean) => Promise<void>;
  addLead: (count: number) => Promise<void>;
  addingLead: boolean;
  setLeadRuntime: (index: number, rt: LeadRuntimeView) => Promise<void>;
  setSolo: (rt: LeadRuntimeView | null) => Promise<void>;
  updateRole: (id: string, patch: Partial<RoleConfigView>) => void;
  persistRole: (row: RoleConfigView) => Promise<void>;
  refreshModels: (harness: string) => Promise<void>;
  addModel: (harness: string, model: string) => Promise<void>;
  modelOptions: (harness: string, current: string) => DropdownOption[];
  harnessOptionsFor: (current: string) => DropdownOption[];
  toggleRoleEnabled: (row: RoleConfigView, enabled: boolean) => Promise<void>;
  /** Seconds between workflow status reports while a turn runs; 0 is off. */
  setStatusPeriod: (seconds: number) => Promise<void>;
  /** The active thread's list row: carries its schedule, if one is set. */
  summary: ThreadSummary | null;
  /** The environment's other top-level threads this thread may wait on. */
  scheduleCandidates: ThreadSummary[];
  /** Resolves a thread id to its title, falling back to the raw id. */
  threadTitle: (id: string) => string;
  /** Schedules the current thread to start once every listed thread finishes. */
  schedule: (dependsOn: string[], prompt: string) => Promise<void>;
  /** Cancels the current thread's schedule. */
  unschedule: () => Promise<void>;
};

const STATUS_PERIOD_OPTIONS: DropdownOption[] = [
  { value: "0", label: "Off" },
  { value: "15", label: "Every 15 seconds" },
  { value: "30", label: "Every 30 seconds" },
  { value: "60", label: "Every minute" },
  { value: "120", label: "Every 2 minutes" },
  { value: "300", label: "Every 5 minutes" },
  { value: "600", label: "Every 10 minutes" },
];

/** Per-thread run settings plus the global role table. Shared by the pre-run
 * setup view and the aside models tab, so both edit the same handlers. */
export function ThreadSetup({
  settings,
  thread,
  toggle,
  addLead,
  addingLead,
  setLeadRuntime,
  setSolo,
  updateRole,
  persistRole,
  refreshModels,
  addModel,
  modelOptions,
  harnessOptionsFor,
  toggleRoleEnabled,
  setStatusPeriod,
  summary,
  scheduleCandidates,
  threadTitle,
  schedule,
  unschedule,
}: ThreadSetupProps) {
  const count = thread?.leads && thread.leads > 0 ? thread.leads : 1;
  const multiLead = count > 1;
  const peerOn = !!thread?.peerMailbox || multiLead;
  const solo = thread?.soloRuntime;
  const soloOn = !!solo;
  const soloHarn = solo?.harness ?? "";
  // Turning solo on pins a harness right away: the thread's current solo
  // harness (re-enabling), else the Orchestrator role's, else the first
  // enabled one. With no harness available the toggle stays a no-op instead
  // of sending a would-clear-it-empty string.
  const orchHarness = settings?.roles.find((r) => r.id === "orchestrator")?.harness ?? "";
  const firstSoloHarness = soloHarn || orchHarness || harnessOptionsFor("")[0]?.value || "";
  const statusPeriod = thread?.statusPeriodSeconds ?? 0;
  const statusPeriodValue = String(statusPeriod);
  const statusPeriodOptions = STATUS_PERIOD_OPTIONS.some((o) => o.value === statusPeriodValue)
    ? STATUS_PERIOD_OPTIONS
    : [
        ...STATUS_PERIOD_OPTIONS,
        { value: statusPeriodValue, label: `Every ${statusPeriod} seconds` },
      ];

  return (
    <>
      {settings && (
        <div className="model-summary" style={soloOn ? { opacity: 0.45 } : undefined}>
          {thread?.autoMode && <span className="ms-auto">auto</span>}
          {settings.roles
            .filter((r) => r.enabled)
            .map((r) => (
            <span key={r.id}>
              {r.name} <b>{r.model || "default"}</b> {r.harness}
            </span>
          ))}
        </div>
      )}
      {!settings && <p className="hint">Loading roles…</p>}
      {settings && (
        <>
          <div
            className={`toggle${thread?.autoMode ? " on" : ""}`}
            {...switchProps(!!thread?.autoMode, () =>
              thread && !soloOn && toggle("autoMode", !thread.autoMode),
            )}
            aria-disabled={soloOn || undefined}
            title={
              soloOn
                ? "Parked while solo mode is on — the solo agent runs the whole turn itself"
                : "Auto mode: the orchestrator picks each task's model and harness"
            }
          >
            <span className="sw" /> Auto mode
          </div>
          <div
            className={`toggle${soloOn ? " on" : ""}`}
            {...switchProps(soloOn, () =>
              thread &&
              (soloOn
                ? setSolo(null)
                : firstSoloHarness && setSolo({ harness: firstSoloHarness })),
            )}
            title="Solo mode: one agent on the chosen harness, no orchestrator team"
          >
            <span className="sw" /> Solo mode
          </div>
          {soloOn && (
            <div className="role-edit">
              <span className="rn">Solo</span>
              <Dropdown
                value={soloHarn}
                options={harnessOptionsFor(soloHarn)}
                disabled={!thread}
                onChange={(v) => setSolo({ harness: v })}
              />
              <Dropdown
                value={solo?.model ?? ""}
                options={modelOptions(soloHarn, solo?.model ?? "")}
                searchable
                placeholder="model (default)"
                disabled={!thread}
                onRefresh={() => refreshModels(soloHarn).catch(() => undefined)}
                addLabel="Add model"
                onAdd={(m) => {
                  addModel(soloHarn, m)
                    .then(() =>
                      setSolo({ harness: soloHarn, model: m, variant: solo?.variant ?? "" }),
                    )
                    .catch(() => undefined);
                }}
                onChange={(v) =>
                  setSolo({ harness: soloHarn, model: v, variant: solo?.variant ?? "" })
                }
              />
            </div>
          )}
          <p className="hint">
            {soloOn
              ? "Solo mode is on — this thread runs one agent and no orchestrator team. Other settings are parked."
              : thread?.autoMode
                ? "Auto mode is on — the orchestrator picks each task's model and harness. Other roles are parked."
                : "Roles are global: their harness and model apply to every thread on the next turn. Leads below are per thread."}
          </p>
          {settings.roles.map((r) => {
            const parked = soloOn || (!!thread?.autoMode && r.id !== "orchestrator");
            return (
              <div
                className={`role-edit has-switch${parked ? " parked" : ""}${r.enabled ? "" : " off"}`}
                key={r.id}
                title={
                  parked
                    ? soloOn
                      ? "Parked while solo mode is on — the solo agent runs this thread alone"
                      : "Parked while auto mode is on — the orchestrator decides this role's runtime"
                    : undefined
                }
              >
                <span className="rn">
                  {r.name}
                  {r.experimental && <span className="badge experimental">Experimental</span>}
                </span>
                <Dropdown
                  value={r.harness}
                  options={harnessOptionsFor(r.harness)}
                  disabled={parked}
                  onChange={(v) => {
                    const row = { ...r, harness: v };
                    updateRole(r.id, { harness: v });
                    persistRole(row).catch(() => undefined);
                    refreshModels(v).catch(() => undefined);
                  }}
                />
                <Dropdown
                  value={r.model}
                  options={modelOptions(r.harness, r.model)}
                  searchable
                  align="end"
                  placeholder="model (default)"
                  disabled={parked}
                  onRefresh={() => refreshModels(r.harness).catch(() => undefined)}
                  addLabel="Add model"
                  onAdd={(m) => {
                    addModel(r.harness, m)
                      .then(() => {
                        const row = { ...r, model: m };
                        updateRole(r.id, { model: m });
                        persistRole(row).catch(() => undefined);
                      })
                      .catch(() => undefined);
                  }}
                  onChange={(v) => {
                    const row = { ...r, model: v };
                    updateRole(r.id, { model: v });
                    persistRole(row).catch(() => undefined);
                  }}
                />
                <div
                  className={`toggle role-switch${r.enabled ? " on" : ""}${r.required ? " disabled" : ""}`}
                  {...switchProps(r.enabled, () => toggleRoleEnabled(r, !r.enabled).catch(() => undefined))}
                  aria-disabled={r.required}
                  aria-label={`${r.enabled ? "Disable" : "Enable"} ${r.name}`}
                  title={r.required ? "Required" : r.enabled ? "Disable this role" : "Enable this role"}
                >
                  <span className="sw" />
                </div>
              </div>
            );
          })}
          <div
            className="lead-block"
            style={soloOn ? { opacity: 0.45 } : undefined}
            title={soloOn ? "Parked while solo mode is on — one agent, no team" : undefined}
          >
            <div className="lead-head">
              <span className="rn">Leads</span>
            </div>
            <p className="hint">
              Each lead runs the Orchestrator's runtime unless you override it here. Changes apply to the next turn.
            </p>
            {Array.from({ length: count }, (_, i) => {
              const ov: LeadRuntimeView = thread?.leadRuntimes?.[i] ?? {};
              const harness = ov.harness ?? "";
              const model = ov.model ?? "";
              const variant = ov.variant ?? "";
              const orch = settings.roles.find((r) => r.id === "orchestrator");
              const effective = harness || orch?.harness || "";
              return (
                <div className="role-edit" key={i}>
                  <span className="rn">Lead {i + 1}</span>
                  <Dropdown
                    value={harness}
                    options={[
                      { value: "", label: `Orchestrator (${orch?.harness ?? "default"})` },
                      ...harnessOptionsFor(harness),
                    ]}
                    disabled={!thread || soloOn}
                    onChange={(v) => setLeadRuntime(i, { harness: v })}
                  />
                  <Dropdown
                    value={model}
                    options={modelOptions(effective, model)}
                    searchable
                    placeholder="model (default)"
                    disabled={!thread || soloOn}
                    onRefresh={() => refreshModels(effective).catch(() => undefined)}
                    addLabel="Add model"
                    onAdd={(m) => {
                      addModel(effective, m)
                        .then(() => setLeadRuntime(i, { harness, model: m, variant }))
                        .catch(() => undefined);
                    }}
                    onChange={(v) => setLeadRuntime(i, { harness, model: v, variant })}
                  />
                </div>
              );
            })}
            <button
              type="button"
              className="lead-add"
              aria-label="Add a lead"
              title="Add a lead"
              disabled={!thread || addingLead || soloOn}
              onClick={() => addLead(count + 1)}
            >
              +
            </button>
          </div>
          <div className="setup-peer">
            <div
              className={`toggle${peerOn ? " on" : ""}`}
              {...switchProps(peerOn, () =>
                thread && !multiLead && !soloOn && toggle("peerMailbox", !thread.peerMailbox),
              )}
              aria-disabled={soloOn || multiLead || !thread}
              title="Let leads message each other"
            >
              <span className="sw" /> Peer messaging
            </div>
            <p className="hint">
              {soloOn
                ? "Parked while solo mode is on — a solo thread has no team to message."
                : multiLead
                  ? "Required with more than one lead."
                  : "Lets leads message each other. Needed when you run more than one lead."}
            </p>
          </div>
          <div className="lead-block">
            <div className="lead-head">
              <span className="rn">Status reports</span>
            </div>
            <div className="role-edit">
              <span className="rn">Period</span>
              <Dropdown
                value={statusPeriodValue}
                options={statusPeriodOptions}
                disabled={!thread}
                onChange={(v) => setStatusPeriod(Number(v)).catch(() => undefined)}
              />
            </div>
            <p className="hint">
              Posts a short workflow status (agents, tasks, elapsed) into the chat while a turn runs, even
              when the orchestrator is busy. Takes effect on the next turn; zero turns it off.
            </p>
          </div>
          {/* Subthreads cannot be scheduled: their parent owns when they run. */}
          {summary && !summary.parentThreadId && (
            <ScheduleSection
              summary={summary}
              candidates={scheduleCandidates}
              threadTitle={threadTitle}
              schedule={schedule}
              unschedule={unschedule}
            />
          )}
        </>
      )}
    </>
  );
}

/** The thread's schedule: pick the other threads it waits on and the prompt to
 * run when they finish — or cancel the schedule while it waits. */
function ScheduleSection({
  summary,
  candidates,
  threadTitle,
  schedule,
  unschedule,
}: {
  summary: ThreadSummary;
  candidates: ThreadSummary[];
  threadTitle: (id: string) => string;
  schedule: (dependsOn: string[], prompt: string) => Promise<void>;
  unschedule: () => Promise<void>;
}) {
  const [selected, setSelected] = useState<ReadonlySet<string>>(() => new Set());
  const [prompt, setPrompt] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const cancel = () => {
    setBusy(true);
    setError(null);
    unschedule()
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };

  if (summary.waiting) {
    const waiting = summary.waiting;
    return (
      <div className="lead-block">
        <div className="lead-head">
          <span className="rn">Schedule</span>
        </div>
        <div className="setup-waiting">
          <span className="dot pending" />
          <span className="setup-waiting-deps">
            Waiting on {waiting.dependsOn.map(threadTitle).join(", ")}
          </span>
        </div>
        <div className="setup-waiting-prompt" title={waiting.prompt}>
          {waiting.prompt}
        </div>
        <button type="button" disabled={busy} onClick={cancel}>
          {busy ? "Cancelling…" : "Cancel schedule"}
        </button>
        {error && <p className="hint err-text">{error}</p>}
      </div>
    );
  }

  const toggle = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const submit = () => {
    setBusy(true);
    setError(null);
    schedule([...selected], prompt)
      .then(() => {
        setSelected(new Set());
        setPrompt("");
      })
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };

  return (
    <div className="lead-block">
      <div className="lead-head">
        <span className="rn">Schedule</span>
      </div>
      <p className="hint">
        Start this thread automatically once other threads finish. Pick the threads it waits on
        and the prompt to run.
      </p>
      {candidates.length === 0 && (
        <p className="hint">No other threads to wait on yet.</p>
      )}
      {candidates.length > 0 && (
        <div className="schedule-pick">
          {candidates.map((c) => (
            <label key={c.id} className="schedule-pick-row" title={c.title}>
              <input
                type="checkbox"
                checked={selected.has(c.id)}
                onChange={() => toggle(c.id)}
              />
              <span className="schedule-pick-title">{c.title}</span>
              <span className="schedule-pick-state">
                {(c.runningAgents ?? 0) > 0 ? "running" : "idle"}
              </span>
            </label>
          ))}
        </div>
      )}
      <textarea
        className="schedule-prompt-input"
        rows={3}
        placeholder="Prompt to run when the threads finish…"
        value={prompt}
        onChange={(e) => setPrompt(e.target.value)}
      />
      <div className="schedule-actions">
        <button
          type="button"
          className="primary"
          disabled={busy || selected.size === 0 || prompt.trim() === ""}
          onClick={submit}
        >
          {busy ? "Scheduling…" : "Schedule"}
        </button>
        {error && <span className="err-text">{error}</span>}
      </div>
    </div>
  );
}
