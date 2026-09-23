import type {
  ModelSelection,
  OrchestrationShellSnapshot,
  OrchestrationThreadDetailSnapshot,
  ServerConfig,
  ServerSettings,
} from "@t3tools/contracts";
import {
  deriveAgentPanelModel,
  foldSubagentActivities,
} from "@t3tools/client-runtime/state/subagentRuntime";

/** A project the palette can start a thread in. */
export interface TuiProjectRow {
  readonly id: string;
  readonly title: string;
  readonly workspaceRoot: string;
  readonly defaultModelSelection: ModelSelection | null;
}

/** A configured provider instance and the models it offers. */
export interface TuiProviderRow {
  readonly instanceId: string;
  readonly label: string;
  readonly driver: string;
  readonly models: ReadonlyArray<{
    readonly slug: string;
    readonly name: string;
    /** The provider's own default, which a new thread should start from. */
    readonly isDefault?: boolean;
  }>;
}

/** A server setting the settings screen can flip, with its current value. */
export interface TuiServerSettingRow {
  readonly key: string;
  readonly label: string;
  readonly value: boolean | string;
}

/** The settings a terminal client can act on, in the order the screen lists them. */
const SERVER_SETTING_KEYS = [
  "enableLegacyTokenStreaming",
  "enableProviderUpdateChecks",
  "enableAgentBrowserAccess",
  "newWorktreesStartFromOrigin",
] as const satisfies ReadonlyArray<keyof ServerSettings>;

/**
 * Turning the server's snapshots into the lines the TUI shows. Pure on purpose:
 * the renderer, the key handling, and this formatting can all be exercised
 * without a terminal or a server, which is what makes the TUI testable at all.
 */

export interface TuiThreadRow {
  readonly id: string;
  readonly title: string;
  readonly projectTitle: string;
  readonly branch: string | null;
  /** `7/7 settled`-style fleet summary, or null when the thread has no agents. */
  readonly fleet: string | null;
}

export interface TuiThreadView {
  readonly title: string;
  readonly recap: string | null;
  readonly lines: ReadonlyArray<TuiLine>;
}

/**
 * One glyph per line kind and per agent status, the way both reference TUIs
 * mark rows: a filled dot for settled work, a ring for live work, a cross for
 * what failed. Text keeps the detail; the glyph carries the state.
 */
export function lineGlyph(kind: TuiLine["kind"]): string {
  if (kind === "user") return "›";
  if (kind === "agent") return "·";
  if (kind === "system") return "!";
  return "⏺";
}

export function statusGlyph(status: string): string {
  if (status === "running" || status === "pending") return "◍";
  if (status === "waiting") return "◌";
  if (status === "completed") return "●";
  if (status === "failed") return "✗";
  if (status === "cancelled" || status === "interrupted") return "⊘";
  return "○";
}

export interface TuiLine {
  readonly kind: "user" | "assistant" | "agent" | "system";
  readonly text: string;
}

const TITLE_MAX_CHARS = 80;
const LINE_MAX_CHARS = 400;
/** Transcript entries keep their line breaks; only runaway text is bounded. */
const MESSAGE_MAX_CHARS = 8_000;

function oneLine(text: string, max: number): string {
  const normalized = text.replace(/\s+/g, " ").trim();
  return normalized.length <= max ? normalized : `${normalized.slice(0, max - 1).trimEnd()}…`;
}

/** Threads newest first, with the fleet summary the Agents panel would show. */
export function threadRows(snapshot: OrchestrationShellSnapshot): ReadonlyArray<TuiThreadRow> {
  const projectTitles = new Map(snapshot.projects.map((project) => [project.id, project.title]));
  return snapshot.threads
    .slice()
    .sort((left, right) => right.updatedAt.localeCompare(left.updatedAt))
    .map((thread) => ({
      id: thread.id,
      title: oneLine(thread.title, TITLE_MAX_CHARS),
      projectTitle: projectTitles.get(thread.projectId) ?? "unknown project",
      branch: thread.branch,
      fleet: null,
    }));
}

/**
 * The transcript of one thread: the user's messages, the assistant's answers,
 * what each sub-agent did, and the system rows that explain the run. Agents and
 * their recap come from the same fold and derivation the web clients use, so a
 * TUI session and a browser session describe the same work the same way.
 */
export function threadView(
  snapshot: OrchestrationThreadDetailSnapshot,
  options?: { readonly includeActivities?: boolean },
): TuiThreadView {
  const includeActivities = options?.includeActivities ?? true;
  // The detail snapshot wraps the thread: messages, activities, and the session
  // live under `thread`, not at the top level.
  const thread = snapshot.thread;
  const agents = foldSubagentActivities(thread.activities, {
    sessionLive: thread.session !== null && thread.session.status === "running",
  });
  const model = deriveAgentPanelModel({ agents });
  const newestUser = [...thread.messages].reverse().find((message) => message.role === "user");
  // The web client's richer recap (`deriveThreadRecap`, PR #40) is not on this
  // branch: it lands with the sticky composer row. Until then the TUI says the
  // same thing with the data this branch has — goal, live fleet, settled work.
  const recapParts: string[] = [];
  const goal = newestUser === undefined ? "" : oneLine(newestUser.text, 140);
  if (goal.length > 0) recapParts.push(`Goal: ${goal}`);
  if (model.liveCount > 0)
    recapParts.push(`${model.liveCount} agent${model.liveCount === 1 ? "" : "s"} working`);
  else if (model.settledCount > 0) recapParts.push(`${model.settledCount} settled`);
  const recap = recapParts.length === 0 ? null : recapParts.join("  ·  ");

  const lines: TuiLine[] = [];
  for (const message of thread.messages) {
    const trimmed = message.text.replace(/\s+$/u, "");
    if (trimmed.length === 0) continue;
    // Markdown is rendered line by line downstream, so the breaks have to
    // survive this step — collapsing them here is what flattened the answers.
    const text =
      trimmed.length <= MESSAGE_MAX_CHARS
        ? trimmed
        : `${trimmed.slice(0, MESSAGE_MAX_CHARS - 1).trimEnd()}…`;
    lines.push({ kind: message.role === "user" ? "user" : "assistant", text });
  }
  if (includeActivities) {
    for (const agent of agents) {
      const detail = agent.error ?? agent.result ?? agent.progress ?? null;
      if (detail === null) continue;
      lines.push({
        kind: "agent",
        text: oneLine(`${agent.role ?? agent.title} · ${agent.status} — ${detail}`, LINE_MAX_CHARS),
      });
    }
  }

  return {
    title: oneLine(thread.title, TITLE_MAX_CHARS),
    recap,
    lines,
  };
}

/** The projects a new thread can be started in, oldest first as the server sends them. */
export function projectRows(snapshot: OrchestrationShellSnapshot): ReadonlyArray<TuiProjectRow> {
  return snapshot.projects.map((project) => ({
    id: project.id,
    title: project.title,
    workspaceRoot: project.workspaceRoot,
    defaultModelSelection: project.defaultModelSelection,
  }));
}

/** Installed, enabled provider instances and the models each one offers. */
export function providerRows(config: ServerConfig): ReadonlyArray<TuiProviderRow> {
  return config.providers
    .filter(
      (provider) =>
        provider.installed && provider.enabled && provider.availability !== "unavailable",
    )
    .map((provider) => ({
      instanceId: provider.instanceId,
      label: provider.displayName ?? provider.instanceId,
      driver: provider.driver,
      models: provider.models.map((model) => ({
        slug: model.slug,
        name: model.name,
        isDefault: model.isDefault === true,
      })),
    }));
}

const SERVER_SETTING_LABELS: Readonly<Record<string, string>> = {
  enableLegacyTokenStreaming: "stream tokens as they arrive",
  enableProviderUpdateChecks: "check for provider updates",
  enableAgentBrowserAccess: "let agents drive the preview browser",
  newWorktreesStartFromOrigin: "new worktrees start from origin",
};

/** The server settings a terminal client can flip, with their current values. */
export function serverSettingRows(settings: ServerSettings): ReadonlyArray<TuiServerSettingRow> {
  const rows: Array<TuiServerSettingRow> = [];
  for (const key of SERVER_SETTING_KEYS) {
    const value = settings[key];
    if (typeof value !== "boolean") continue;
    rows.push({ key, label: SERVER_SETTING_LABELS[key] ?? key, value });
  }
  return rows;
}

/** Which driver backs each harness CLI, so a harness's models can be found. */
const HARNESS_DRIVERS: Readonly<Record<string, string>> = {
  opencode: "opencode",
  codex: "codex",
  claude: "claudeAgent",
  "cursor-agent": "cursor",
  grok: "grok",
  pi: "pi",
};

/** The AgentTunnel provider's own catalog: every harness's models, unioned. */
export interface TuiAgentTunnelCatalog {
  readonly instanceId: string | null;
  readonly models: ReadonlyArray<{
    readonly slug: string;
    readonly name: string;
    readonly harnesses: ReadonlyArray<string> | null;
  }>;
}

/**
 * The workflow provider plans and dispatches, so a role's model comes from the
 * union of every harness's catalog. The workflow provider publishes that union
 * itself when it can; when it reports no models, fall back to each harness's own
 * provider, tagged with the harness that backs it.
 */
export function agentTunnelCatalog(config: ServerConfig): TuiAgentTunnelCatalog {
  const workflow = config.providers.find((entry) => entry.driver === "agenttunnel");
  if (workflow !== undefined && workflow.models.length > 0) {
    return {
      instanceId: workflow.instanceId,
      models: workflow.models.map((model) => ({
        slug: model.slug,
        name: model.name,
        harnesses: model.harnesses ?? null,
      })),
    };
  }
  const models: Array<{ slug: string; name: string; harnesses: ReadonlyArray<string> | null }> = [];
  for (const [harness, driver] of Object.entries(HARNESS_DRIVERS)) {
    for (const provider of config.providers) {
      if (provider.driver !== driver || !provider.installed || !provider.enabled) continue;
      for (const model of provider.models) {
        models.push({ slug: model.slug, name: model.name, harnesses: [harness] });
      }
    }
  }
  return { instanceId: workflow?.instanceId ?? null, models };
}
