import type { LeadRuntimeView, RoleConfigView, SettingsView, ThreadState } from "../App";
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
        </>
      )}
    </>
  );
}
