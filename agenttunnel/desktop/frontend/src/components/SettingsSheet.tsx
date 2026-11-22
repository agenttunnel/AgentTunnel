// The settings sheet. It owns its form: the environment fields live in
// settingsForm, so typing re-renders the sheet alone, and App's loadSettings
// refills them without holding them as state. Daemon settings and the
// harness list stay with App, which other panels read too.

import { memo, useEffect, useState } from "react";
import type {
  DependencyView,
  HarnessInfo,
  LeadRuntimeView,
  RoleConfigView,
  SettingsView,
} from "../App";
import type { Rpc } from "../lib/rpc";
import { createStore, useStoreField, type SetState } from "../lib/store";
import { switchProps } from "../lib/switchProps";
import { THEMES, applyTheme, loadTheme, type ThemeId } from "../lib/themes";
import { ApprovedModelsPicker } from "./ApprovedModelsPicker";
import { BrowserNotifyToggle } from "./BrowserNotifyToggle";
import { Dropdown, type DropdownOption } from "./Dropdown";
import { ThinkingPicker } from "./ThinkingPicker";
import { useEscapeClose } from "../lib/escape";

/** The sheet's form fields and notices. App writes them (loadSettings,
 * refreshModels, any saved setting); only the sheet reads them. */
export const settingsForm = createStore({
  workdir: "",
  approvedModels: [] as string[],
  staleAfter: "30",
  saved: false,
  modelsNotice: null as string | null,
  // Read again on every open; the last answer shows meanwhile.
  appVersion: null as string | null,
  deps: null as DependencyView[] | null,
  depsError: null as string | null,
});

/** Refills the form from the daemon's settings. */
export function fillSettingsForm(s: SettingsView) {
  settingsForm.setter("workdir")(s.workdir ?? "");
  settingsForm.setter("approvedModels")(s.approvedModels ?? []);
  settingsForm.setter("staleAfter")(String(s.staleAfterMinutes ?? 30));
}

export const setModelsNotice = settingsForm.setter("modelsNotice");

/** Shows "Saved" in the sheet's head for a moment. */
export function flashSaved() {
  settingsForm.setter("saved")(true);
  window.setTimeout(() => settingsForm.setter("saved")(false), 1500);
}

const setWorkdirInput = settingsForm.setter("workdir");
const setApprovedModels = settingsForm.setter("approvedModels");
const setStaleAfterInput = settingsForm.setter("staleAfter");
const setAppVersion = settingsForm.setter("appVersion");
const setDeps = settingsForm.setter("deps");
const setDepsError = settingsForm.setter("depsError");

export const SettingsSheet = memo(function SettingsSheet({
  rpc,
  settings,
  setSettings,
  harnesses,
  loadHarnesses,
  attached,
  keepRunning,
  onToggleKeepRunning,
  onSaveThreadDefaults,
  onClose,
  harnessOptionsFor,
  modelOptions,
  refreshModels,
  addModel,
  updateRole,
  persistRole,
}: {
  rpc: Rpc | null;
  settings: SettingsView;
  setSettings: SetState<SettingsView | null>;
  harnesses: HarnessInfo;
  loadHarnesses: () => Promise<void>;
  /** The window attached to a daemon it did not start: no keep-running switch. */
  attached: boolean;
  keepRunning: boolean;
  onToggleKeepRunning: (enabled: boolean) => void;
  onSaveThreadDefaults: () => void;
  onClose: () => void;
  harnessOptionsFor: (current: string) => DropdownOption[];
  modelOptions: (harness: string, current: string) => DropdownOption[];
  refreshModels: (harness: string) => Promise<void>;
  addModel: (harness: string, model: string) => Promise<void>;
  updateRole: (id: string, patch: Partial<RoleConfigView>) => void;
  persistRole: (row: RoleConfigView) => Promise<void>;
}) {
  const workdirInput = useStoreField(settingsForm, "workdir");
  const approvedModels = useStoreField(settingsForm, "approvedModels");
  const staleAfterInput = useStoreField(settingsForm, "staleAfter");
  const saved = useStoreField(settingsForm, "saved");
  const modelsNotice = useStoreField(settingsForm, "modelsNotice");
  // Appearance is shared by every client of this daemon through the prefs store.
  const [theme, setTheme] = useState<ThemeId>(loadTheme);
  const appVersion = useStoreField(settingsForm, "appVersion");
  const deps = useStoreField(settingsForm, "deps");
  const depsError = useStoreField(settingsForm, "depsError");
  const [depsBusy, setDepsBusy] = useState(false);

  const pickTheme = (id: ThemeId) => {
    setTheme(id);
    applyTheme(id);
  };

  const loadDeps = async () => {
    if (!rpc) return;
    setDepsBusy(true);
    try {
      setDeps(await rpc.call<DependencyView[]>("deps.check"));
      setDepsError(null);
    } catch (e) {
      setDepsError(e instanceof Error ? e.message : String(e));
    } finally {
      setDepsBusy(false);
    }
  };

  // The version and the forge CLIs are read each time the sheet opens.
  useEffect(() => {
    if (!rpc) return;
    rpc
      .call<{ version?: string }>("app.info")
      .then((info) => setAppVersion(info?.version || "dev"))
      .catch(() => setAppVersion("dev"));
    loadDeps().catch(() => undefined);
  }, [rpc]);

  const enabledHarnesses = harnesses.enabled ?? harnesses.all;
  const isHarnessEnabled = (h: string) => enabledHarnesses.includes(h);
  const leadSettings = settings.lead ?? {};
  const orchestratorRole = settings.roles.find((r) => r.id === "orchestrator");
  const leadHarness = leadSettings.harness || orchestratorRole?.harness || "";

  const saveEnv = async () => {
    if (!rpc) return;
    const s = await rpc.call<SettingsView>("settings.set", {
      workdir: workdirInput,
      autoMode: settings?.autoMode ?? false,
      approvedModels,
      staleAfterMinutes: staleAfterInput,
    });
    setSettings(s);
    flashSaved();
  };

  const toggleHarness = async (id: string, enabled: boolean) => {
    if (!rpc) return;
    await rpc.call("settings.set", { harness: { id, enabled } });
    await loadHarnesses();
    flashSaved();
  };

  /** Pins the master orchestrator's runtime; empty fields inherit the role. */
  const persistLeadRuntime = async (runtime: LeadRuntimeView) => {
    if (!rpc) return;
    const s = await rpc.call<SettingsView>("settings.set", {
      lead: {
        harness: runtime.harness ?? "",
        model: runtime.model ?? "",
        variant: runtime.variant ?? "",
      },
    });
    setSettings(s);
    flashSaved();
  };

  const toggleExperimentalRoles = async (enabled: boolean) => {
    if (!rpc) return;
    const s = await rpc.call<SettingsView>("settings.set", { experimentalRoles: enabled });
    setSettings(s);
    flashSaved();
  };

  useEscapeClose(onClose);
  return (
    <div className="overlay" onClick={onClose}>
      <div className="sheet" onClick={(e) => e.stopPropagation()}>
        <div className="sheet-head">
          <strong>Settings</strong>
          <div className="sheet-head-right">
            {saved && <span className="saved">Saved</span>}
            <button onClick={onClose}>Close</button>
          </div>
        </div>
        <div className="sheet-body">
          <section className="set-section">
            <h4>Appearance</h4>
            <p className="hint">
              Color theme for this device. It applies everywhere and is saved locally, not in
              the daemon config.
            </p>
            <div className="theme-grid">
              {THEMES.map((t) => (
                <button
                  key={t.id}
                  type="button"
                  className={`theme-card ${theme === t.id ? "on" : ""}`}
                  aria-pressed={theme === t.id}
                  onClick={() => pickTheme(t.id)}
                >
                  <span className="theme-swatch" aria-hidden="true">
                    {t.swatches.map((c, i) => (
                      <span key={`${t.id}-${i}`} style={{ background: c }} />
                    ))}
                  </span>
                  <span className="theme-name">{t.name}</span>
                  <span className="theme-note">{t.note}</span>
                </button>
              ))}
            </div>
          </section>
          <section className="set-section">
            <h4>New thread defaults</h4>
            <p className="hint">
              New threads open with the tab, panels, layout, and council setup saved here. A
              thread you change keeps its own view.
            </p>
            <div className="sheet-actions">
              <button onClick={onSaveThreadDefaults}>Save this view as default</button>
            </div>
          </section>
          {!attached && (
            <section className="set-section">
              <h4>Desktop</h4>
              <p className="hint">
                When this is on, closing the window leaves the daemon (and any running
                turns) alive in the background so <code>at</code> can still reach it.
                Turn it off to stop the daemon with the window.
              </p>
              <div
                className={`toggle ${keepRunning ? "on" : ""}`}
                {...switchProps(keepRunning, () => onToggleKeepRunning(!keepRunning))}
              >
                <span className="sw" /> Keep daemon running after the window closes
              </div>
            </section>
          )}
          <BrowserNotifyToggle />
          <section className="set-section">
            <h4>Environment</h4>
            <label className="field">
              <span>Fallback working directory (used when a thread has no project)</span>
              <input
                value={workdirInput}
                onChange={(e) => setWorkdirInput(e.target.value)}
                placeholder="/path/to/your/repo"
              />
            </label>
            <div
              className={`toggle ${settings.autoMode ? "on" : ""}`}
              {...switchProps(settings.autoMode, () =>
                setSettings({ ...settings, autoMode: !settings.autoMode }),
              )}
              style={{ marginTop: 10 }}
            >
              <span className="sw" /> Auto mode — orchestrator picks each task's model
            </div>
            <div className="field" style={{ marginTop: 12 }}>
              <span>Approved models for auto mode</span>
              <ApprovedModelsPicker
                value={approvedModels}
                onChange={setApprovedModels}
                models={harnesses.models}
                harnessOrder={harnesses.all}
              />
            </div>
            <label className="field" style={{ marginTop: 12 }}>
              <span>Mark threads stale after this many minutes of inactivity</span>
              <input
                value={staleAfterInput}
                onChange={(e) => setStaleAfterInput(e.target.value)}
                inputMode="numeric"
                placeholder="30"
                style={{ maxWidth: 120 }}
              />
            </label>
            {settings.configPath && (
              <p className="hint">
                Config file: <code>{settings.configPath}</code>
              </p>
            )}
            <div className="sheet-actions">
              <button className="primary" onClick={saveEnv}>
                Save environment
              </button>
            </div>
          </section>

          <section className="set-section">
            <h4>Harnesses</h4>
            <p className="hint">Disable a harness to remove it from every role.</p>
            {harnesses.all.map((h) => (
              <div className="harness-row" key={h}>
                <span className="hn">{h}</span>
                <span className={`avail ${harnesses.available.includes(h) ? "detected" : ""}`}>
                  {harnesses.available.includes(h) ? "detected" : "not on PATH"}
                </span>
                <div
                  className={`toggle ${isHarnessEnabled(h) ? "on" : ""}`}
                  {...switchProps(isHarnessEnabled(h), () =>
                    toggleHarness(h, !isHarnessEnabled(h)),
                  )}
                  aria-label={`${isHarnessEnabled(h) ? "Disable" : "Enable"} ${h}`}
                >
                  <span className="sw" />
                </div>
              </div>
            ))}
          </section>

          <section className="set-section">
            <h4>Master orchestrator</h4>
            <p className="hint">
              The team lead runs on this harness and model. Leave it on the Orchestrator role
              to inherit that role's default; each thread can still override its own leads.
            </p>
            <div className="role-edit">
              <span className="rn">Lead agent</span>
              <Dropdown
                value={leadSettings.harness ?? ""}
                options={[
                  {
                    value: "",
                    label: `Orchestrator role (${orchestratorRole?.harness ?? "default"})`,
                  },
                  ...harnessOptionsFor(leadSettings.harness ?? ""),
                ]}
                onChange={(v) => {
                  persistLeadRuntime({ harness: v, model: "", variant: "" }).catch(
                    () => undefined,
                  );
                  if (v) refreshModels(v).catch(() => undefined);
                }}
              />
              <Dropdown
                value={leadSettings.model ?? ""}
                options={modelOptions(leadHarness, leadSettings.model ?? "")}
                searchable
                align="end"
                placeholder="model (default)"
                footNote="Discovered from the harness CLI"
                onRefresh={() => leadHarness && refreshModels(leadHarness).catch(() => undefined)}
                addLabel="Add model"
                onAdd={(m) => {
                  addModel(leadHarness, m)
                    .then(() =>
                      persistLeadRuntime({
                        harness: leadSettings.harness ?? "",
                        model: m,
                        variant: leadSettings.variant ?? "",
                      }),
                    )
                    .catch(() => undefined);
                }}
                onChange={(v) =>
                  persistLeadRuntime({
                    harness: leadSettings.harness ?? "",
                    model: v,
                    variant: leadSettings.variant ?? "",
                  }).catch(() => undefined)
                }
              />
              <ThinkingPicker
                harness={leadHarness}
                value={leadSettings.variant ?? ""}
                onChange={(v) =>
                  persistLeadRuntime({
                    harness: leadSettings.harness ?? "",
                    model: leadSettings.model ?? "",
                    variant: v,
                  }).catch(() => undefined)
                }
              />
            </div>
          </section>

          <section className="set-section">
            <h4>Dependencies</h4>
            <p className="hint">
              The forge CLIs the Pull request tab shells out to. Versions are read from the
              tools on PATH.
            </p>
            {depsError && <p className="hint warn">{depsError}</p>}
            {deps === null && !depsError && <p className="hint">Checking…</p>}
            {(deps ?? []).map((d) => (
              <div className="dep-row" key={d.name}>
                <span className="dn">{d.name}</span>
                <span className={`dv ${d.status}`}>
                  {d.status === "missing"
                    ? "not installed"
                    : `${d.version || "unknown version"}${
                        d.status === "ok" ? "" : ` — ${d.minVersion} or newer required`
                      }`}
                </span>
                <span
                  className={`dot ${
                    d.status === "ok" ? "clean" : d.status === "outdated" ? "dirty" : "missing"
                  }`}
                  title={d.status}
                />
              </div>
            ))}
            <div className="sheet-actions">
              <button onClick={() => loadDeps().catch(() => undefined)} disabled={depsBusy}>
                {depsBusy ? "Checking…" : "Recheck"}
              </button>
            </div>
          </section>

          <section className="set-section">
            <h4>Roles</h4>
            <p className="hint">
              Each subagent role runs on its own harness and model; the Orchestrator role is the
              master orchestrator above. Changes apply to the next turn.
            </p>
            <div
              className={`toggle ${settings.experimentalRoles ? "on" : ""}`}
              {...switchProps(settings.experimentalRoles, () =>
                toggleExperimentalRoles(!settings.experimentalRoles).catch(() => undefined),
              )}
              style={{ marginBottom: 10 }}
            >
              <span className="sw" /> Experimental roles
            </div>
            <p className="hint">
              Experimental roles are off by default. Turn this on to dispatch them.
            </p>
            {modelsNotice && <p className="hint warn">{modelsNotice}</p>}
            {settings.roles
              .filter((r) => r.id !== "orchestrator")
              .map((r) => (
                <div className="role-edit" key={r.id}>
                  <span className="rn">
                    {r.name}
                    {r.experimental && <span className="badge experimental">Experimental</span>}
                  </span>
                  <Dropdown
                    value={r.harness}
                    options={harnessOptionsFor(r.harness)}
                    onChange={(v) => {
                      // A thinking level belongs to one harness; a new harness starts at its default.
                      const row = { ...r, harness: v, variant: "" };
                      updateRole(r.id, { harness: v, variant: "" });
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
                    footNote="Discovered from the harness CLI"
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
                  <ThinkingPicker
                    harness={r.harness}
                    value={r.variant}
                    onChange={(v) => {
                      const row = { ...r, variant: v };
                      updateRole(r.id, { variant: v });
                      persistRole(row).catch(() => undefined);
                    }}
                  />
                </div>
              ))}
          </section>

          <section className="set-section about-section">
            <h4>About</h4>
            <p className="hint">
              AgentTunnel {appVersion ?? "dev"} · Built by Wael Rabah
            </p>
            <p className="hint">
              <a href="https://github.com/WaelRabah">github.com/WaelRabah</a>
            </p>
          </section>
        </div>
      </div>
    </div>
  );
});
