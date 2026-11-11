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
  updateRole: (id: string, patch: Partial<RoleConfigView>) => void;
  persistRole: (row: RoleConfigView) => Promise<void>;
  refreshModels: (harness: string) => Promise<void>;
  addModel: (harness: string, model: string) => Promise<void>;
  modelOptions: (harness: string, current: string) => DropdownOption[];
  harnessOptionsFor: (current: string) => DropdownOption[];
  /** Per-role enable switch. Omit to hide the switches. */
  toggleRoleEnabled?: (row: RoleConfigView, enabled: boolean) => Promise<void>;
};

/** Per-thread run settings plus the global role table. Shared by the pre-run
 * setup view and the aside models tab, so both edit the same handlers. */
export function ThreadSetup({
  settings,
  thread,
  toggle,
  addLead,
  addingLead,
  setLeadRuntime,
  updateRole,
  persistRole,
  refreshModels,
  addModel,
  modelOptions,
  harnessOptionsFor,
  toggleRoleEnabled,
}: ThreadSetupProps) {
  const count = thread?.leads && thread.leads > 0 ? thread.leads : 1;
  const multiLead = count > 1;
  const peerOn = !!thread?.peerMailbox || multiLead;

  return (
    <>
      {settings && (
        <div className="model-summary">
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
              thread && toggle("autoMode", !thread.autoMode),
            )}
            title="Auto mode: the orchestrator picks each task's model and harness"
          >
            <span className="sw" /> Auto mode
          </div>
          <p className="hint">
            {thread?.autoMode
              ? "Auto mode is on — the orchestrator picks each task's model and harness. Other roles are parked."
              : "Roles are global: their harness and model apply to every thread on the next turn. Leads below are per thread."}
          </p>
          {settings.roles.map((r) => {
            const parked = !!thread?.autoMode && r.id !== "orchestrator";
            return (
              <div
                className={`role-edit${toggleRoleEnabled ? " has-switch" : ""}${parked ? " parked" : ""}${r.enabled ? "" : " off"}`}
                key={r.id}
                title={
                  parked
                    ? "Parked while auto mode is on — the orchestrator decides this role's runtime"
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
                {toggleRoleEnabled && (
                  <div
                    className={`toggle role-switch${r.enabled ? " on" : ""}${r.required ? " disabled" : ""}`}
                    {...switchProps(r.enabled, () =>
                      toggleRoleEnabled(r, !r.enabled).catch(() => undefined),
                    )}
                    aria-disabled={r.required}
                    aria-label={`${r.enabled ? "Disable" : "Enable"} ${r.name}`}
                    title={r.required ? "Required" : r.enabled ? "Disable this role" : "Enable this role"}
                  >
                    <span className="sw" />
                  </div>
                )}
              </div>
            );
          })}
          <div className="lead-block">
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
                    disabled={!thread}
                    onChange={(v) => setLeadRuntime(i, { harness: v })}
                  />
                  <Dropdown
                    value={model}
                    options={modelOptions(effective, model)}
                    searchable
                    placeholder="model (default)"
                    disabled={!thread}
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
              disabled={!thread || addingLead}
              onClick={() => addLead(count + 1)}
            >
              +
            </button>
          </div>
          <div className="setup-peer">
            <div
              className={`toggle${peerOn ? " on" : ""}`}
              {...switchProps(peerOn, () => thread && !multiLead && toggle("peerMailbox", !thread.peerMailbox))}
              aria-disabled={multiLead || !thread}
              title="Let leads message each other"
            >
              <span className="sw" /> Peer messaging
            </div>
            <p className="hint">
              {multiLead
                ? "Required with more than one lead."
                : "Lets leads message each other. Needed when you run more than one lead."}
            </p>
          </div>
        </>
      )}
    </>
  );
}
