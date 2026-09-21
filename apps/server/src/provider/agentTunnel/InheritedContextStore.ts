import * as Clock from "effect/Clock";
import * as DateTime from "effect/DateTime";
import * as Effect from "effect/Effect";
import * as FileSystem from "effect/FileSystem";
import * as Option from "effect/Option";
import * as Path from "effect/Path";
import type * as PlatformError from "effect/PlatformError";
import * as Schema from "effect/Schema";
import * as Semaphore from "effect/Semaphore";

import { writeFileStringAtomically } from "../../atomicWrite.ts";

/**
 * A durable, cross-project lesson an orchestrator recorded at the end of a
 * workflow. `category` selects the rules file; `entry` is one or two sentences.
 */
export interface AgentTunnelContextLearning {
  readonly category: string;
  readonly entry: string;
}

export interface AgentTunnelContextAttribution {
  readonly threadId: string;
  readonly workflowId: string;
  readonly projectCwd: string;
  readonly status: "completed" | "failed" | "stopped";
}

/**
 * One dispatched agent's outcome, recorded so later orchestrators can compare
 * role candidates on completion rate, tokens burned, and cost. `role` is a
 * dispatched role, or "orchestrator" for the workflow's own passes.
 */
export interface AgentTunnelDispatchOutcome {
  readonly role: string;
  readonly harness: string;
  readonly model?: string;
  readonly status: "completed" | "failed" | "stopped";
  readonly durationMs: number;
  readonly totalTokens: number;
  readonly inputTokens: number;
  readonly outputTokens: number;
  readonly cachedInputTokens: number;
  readonly reasoningOutputTokens: number;
  /** Harness-computed cost, present only when the harness reported one. */
  readonly costUsd?: number;
}

export interface AgentTunnelInheritedContextRecord {
  readonly learnings: ReadonlyArray<AgentTunnelContextLearning>;
  /** Per-agent outcomes folded into the role-candidate stats. */
  readonly dispatches?: ReadonlyArray<AgentTunnelDispatchOutcome>;
  readonly attribution: AgentTunnelContextAttribution;
}

export interface AgentTunnelInheritedContextFlush {
  readonly learnings: number;
  readonly candidates: number;
}

/**
 * File-backed cross-project memory shared by every orchestrator in this
 * environment. `record` serializes flushes with an in-process semaphore and a
 * cross-process lock file so only one orchestrator updates the context at a time.
 */
export interface AgentTunnelInheritedContext {
  /** Renders the guide, every rules file, and the role candidates for a prompt. */
  readonly renderForPrompt: () => Effect.Effect<string>;
  /** Appends learnings and folds dispatch outcomes in, under the semaphore. */
  readonly record: (
    input: AgentTunnelInheritedContextRecord,
  ) => Effect.Effect<AgentTunnelInheritedContextFlush>;
}

const RULES_DIR = "rules";
const GUIDE_FILE = "GUIDE.md";
const LOG_FILE = "LOG.md";
const LOCK_FILE = ".lock";
const ROLE_STATS_FILE = "role-stats.json";
const ROLE_STATS_VERSION = 1;
/** Distinct harness+model candidates kept per role; least recently used drop first. */
const MAX_ROLE_CANDIDATES = 16;
/** Candidate rows per role rendered into the orchestrator prompt. */
const MAX_ROLE_ROWS = 6;
const MAX_ROLE_STATS_PROMPT_CHARS = 4_000;
const CATEGORY_MAX_CHARS = 40;
const ENTRY_MAX_CHARS = 1_200;
const MAX_PROMPT_CHARS = 20_000;
const MAX_RULE_FILE_PROMPT_CHARS = 6_000;
const MAX_GUIDE_PROMPT_CHARS = 4_000;
const LOCK_RETRY_DELAY = "50 millis";
const LOCK_MAX_ATTEMPTS = 200;
const LOCK_STALE_MS = 60_000;

interface SeedRule {
  readonly fileName: string;
  readonly title: string;
  readonly description: string;
  readonly body: string;
}

const SEED_RULES: ReadonlyArray<SeedRule> = [
  {
    fileName: "design-decisions.md",
    title: "Design decisions",
    description: "Design decisions and taste the user has confirmed they like or dislike.",
    body: `Durable, cross-project design taste. Record what the user liked or disliked and why,
so future orchestrators design the same way without being told twice.`,
  },
  {
    fileName: "model-fit.md",
    title: "Harness and model fit",
    description: "Which harness and model combinations fit which kinds of tasks.",
    body: `Which harness + model combinations worked for which kinds of tasks, and which did not.
Record the exact harness and model slugs and the task shape (for example "multi-file
TypeScript refactor" or "small CSS fix").`,
  },
  {
    fileName: "model-quality.md",
    title: "Model quality",
    description: "Overall quality judgments per model, independent of task type.",
    body: `Overall judgments about each model: reasoning depth, instruction following, speed,
verbosity, and where it tends to fail. Independent of any single task type.`,
  },
  {
    fileName: "role-fit.md",
    title: "Best candidates per role",
    description: "Which harness and model is the best candidate for each dispatched role.",
    body: `The best known candidate per role — planner, executor, tester, reviewer — and why:
completion rate, tokens burned, cost, and speed. Name the harness and the exact model slug,
and say when a small, cheap model is good enough for a role. The machine-maintained
\`${ROLE_STATS_FILE}\` file carries the recorded numbers; this file carries the judgment.`,
  },
];

const trace = (message: string, fields: Record<string, string | number>) =>
  Effect.logWarning(message, fields).pipe(
    Effect.annotateLogs({ scope: "agenttunnel.inherited-context" }),
  );

const isAlreadyExists = (error: PlatformError.PlatformError): boolean =>
  error.reason._tag === "AlreadyExists";

function clipTail(text: string, max: number): string {
  const trimmed = text.trim();
  if (trimmed.length <= max) return trimmed;
  return `_(older entries omitted)_\n\n${trimmed.slice(trimmed.length - max)}`;
}

function renderSeedRule(rule: SeedRule): string {
  return `# ${rule.title}

${rule.body}

<!-- Orchestrators append dated entries below. Newest entries win on conflict. -->
`;
}

function renderGuide(
  rules: ReadonlyArray<{ readonly fileName: string; readonly description: string }>,
  updatedAt: string,
): string {
  const rows =
    rules.length > 0
      ? rules
          .map(
            (rule) => `| \`${RULES_DIR}/${rule.fileName}\` | ${rule.description} | ${updatedAt} |`,
          )
          .join("\n")
      : "| _(none yet)_ | | |";
  return `# Orchestrator inherited context

This folder is the cross-project memory that every AgentTunnel orchestrator inherits and
updates at the end of a workflow. It is shared by every project in this environment and
lives outside any repository.

## How to navigate

1. Read this guide first, then the rules files it lists.
2. Each rules file is one category of durable rule. Entries are appended at the bottom,
   newest last, stamped with the date, project, thread, and workflow that recorded them.
3. When entries conflict, the newer entry wins. Treat rules as binding guidance unless the
   user's current task overrides them.
4. \`${LOG_FILE}\` is the chronological ledger of every context update.
5. \`${ROLE_STATS_FILE}\` is machine-maintained: the per-role outcome ledger (runs, tokens,
   cost) behind the "Recorded role candidates" section of the orchestrator prompt. It is
   rewritten at the end of every workflow, so record judgments in
   \`${RULES_DIR}/role-fit.md\` instead of editing it.

## Rules files

| File | What it covers | Last updated |
| --- | --- | --- |
${rows}
`;
}

function renderLogHeader(): string {
  return `# Context update log

Chronological ledger of inherited-context updates, oldest first.
`;
}

function slugifyCategory(value: string): string | undefined {
  const slug = value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, CATEGORY_MAX_CHARS)
    .replace(/-+$/g, "");
  return slug.length > 0 ? slug : undefined;
}

function cleanEntry(value: string): string {
  const normalized = value
    .replace(/\s*\n+\s*/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  return normalized.length <= ENTRY_MAX_CHARS
    ? normalized
    : `${normalized.slice(0, ENTRY_MAX_CHARS - 1).trimEnd()}…`;
}

/**
 * Normalizes one orchestrator-recorded learning. Returns null when the
 * category does not reduce to a usable slug or the entry is empty.
 */
export function normalizeContextLearning(
  category: string,
  entry: string,
): AgentTunnelContextLearning | null {
  const slug = slugifyCategory(category);
  const cleaned = cleanEntry(entry);
  if (slug === undefined || cleaned.length === 0) return null;
  return { category: slug, entry: cleaned };
}

/**
 * What one harness + model candidate has done for one role: how often it ran,
 * how it ended, and what it burned. Tokens and cost are sums, so callers can
 * read `tasks` for the average.
 */
export interface AgentTunnelRoleCandidate {
  readonly harness: string;
  readonly model: string | null;
  readonly tasks: number;
  readonly completed: number;
  readonly failed: number;
  readonly stopped: number;
  readonly totalTokens: number;
  readonly inputTokens: number;
  readonly outputTokens: number;
  readonly cachedInputTokens: number;
  readonly reasoningOutputTokens: number;
  readonly costUsd: number;
  readonly pricedTasks: number;
  readonly durationMs: number;
  readonly lastUsedAt: string;
}

export interface AgentTunnelRoleStats {
  readonly version: number;
  readonly updatedAt: string;
  readonly roles: Record<string, ReadonlyArray<AgentTunnelRoleCandidate>>;
}

const candidateKey = (candidate: { harness: string; model?: string | null }): string =>
  `${candidate.harness}\u0000${candidate.model ?? ""}`;

const emptyRoleStats = (updatedAt: string): AgentTunnelRoleStats => ({
  version: ROLE_STATS_VERSION,
  updatedAt,
  roles: {},
});

const readNumber = (value: unknown): number =>
  typeof value === "number" && Number.isFinite(value) ? value : 0;

/** Structurally validates one stored candidate, dropping anything unusable. */
function parseRoleCandidate(value: unknown): AgentTunnelRoleCandidate | null {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  if (typeof record.harness !== "string" || record.harness.length === 0) return null;
  return {
    harness: record.harness,
    model: typeof record.model === "string" && record.model.length > 0 ? record.model : null,
    tasks: readNumber(record.tasks),
    completed: readNumber(record.completed),
    failed: readNumber(record.failed),
    stopped: readNumber(record.stopped),
    totalTokens: readNumber(record.totalTokens),
    inputTokens: readNumber(record.inputTokens),
    outputTokens: readNumber(record.outputTokens),
    cachedInputTokens: readNumber(record.cachedInputTokens),
    reasoningOutputTokens: readNumber(record.reasoningOutputTokens),
    costUsd: readNumber(record.costUsd),
    pricedTasks: readNumber(record.pricedTasks),
    durationMs: readNumber(record.durationMs),
    lastUsedAt: typeof record.lastUsedAt === "string" ? record.lastUsedAt : "",
  };
}

/** The stats file is machine-owned JSON: decode and encode it through one schema. */
const roleStatsJson = Schema.fromJsonString(Schema.Unknown);
const decodeRoleStatsJson = Schema.decodeUnknownOption(roleStatsJson);

/**
 * Parses the stored stats file. A hand-edited or truncated file falls back to
 * empty stats rather than failing the flush: the ledger is a cache of outcomes,
 * not a source of truth anything else depends on.
 */
export function parseRoleStats(raw: string, updatedAt: string): AgentTunnelRoleStats {
  if (raw.trim().length === 0) return emptyRoleStats(updatedAt);
  const decoded = decodeRoleStatsJson(raw);
  if (Option.isNone(decoded)) return emptyRoleStats(updatedAt);
  const parsed = decoded.value;
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return emptyRoleStats(updatedAt);
  }
  const record = parsed as Record<string, unknown>;
  if (record.version !== ROLE_STATS_VERSION) return emptyRoleStats(updatedAt);
  const rolesValue = record.roles;
  if (rolesValue === null || typeof rolesValue !== "object" || Array.isArray(rolesValue)) {
    return emptyRoleStats(updatedAt);
  }
  const roles: Record<string, ReadonlyArray<AgentTunnelRoleCandidate>> = {};
  for (const [role, candidates] of Object.entries(rolesValue as Record<string, unknown>)) {
    if (!Array.isArray(candidates)) continue;
    const parsedCandidates = candidates
      .map(parseRoleCandidate)
      .filter((candidate): candidate is AgentTunnelRoleCandidate => candidate !== null);
    if (parsedCandidates.length > 0) roles[role] = parsedCandidates;
  }
  return {
    version: ROLE_STATS_VERSION,
    updatedAt: typeof record.updatedAt === "string" ? record.updatedAt : updatedAt,
    roles,
  };
}

/** The stats file is machine-owned, so it is written from the same shape it is read as. */
const encodeRoleStats = Schema.encodeSync(roleStatsJson);

const foldDispatch = (
  candidate: AgentTunnelRoleCandidate,
  dispatch: AgentTunnelDispatchOutcome,
  updatedAt: string,
): AgentTunnelRoleCandidate => ({
  harness: candidate.harness,
  model: candidate.model,
  tasks: candidate.tasks + 1,
  completed: candidate.completed + (dispatch.status === "completed" ? 1 : 0),
  failed: candidate.failed + (dispatch.status === "failed" ? 1 : 0),
  stopped: candidate.stopped + (dispatch.status === "stopped" ? 1 : 0),
  totalTokens: candidate.totalTokens + dispatch.totalTokens,
  inputTokens: candidate.inputTokens + dispatch.inputTokens,
  outputTokens: candidate.outputTokens + dispatch.outputTokens,
  cachedInputTokens: candidate.cachedInputTokens + dispatch.cachedInputTokens,
  reasoningOutputTokens: candidate.reasoningOutputTokens + dispatch.reasoningOutputTokens,
  costUsd: candidate.costUsd + (dispatch.costUsd ?? 0),
  pricedTasks: candidate.pricedTasks + (dispatch.costUsd === undefined ? 0 : 1),
  durationMs: candidate.durationMs + dispatch.durationMs,
  lastUsedAt: updatedAt,
});

/** Folds this workflow's dispatch outcomes into the stored per-role stats. */
export function mergeDispatchOutcomes(
  stats: AgentTunnelRoleStats,
  dispatches: ReadonlyArray<AgentTunnelDispatchOutcome>,
  updatedAt: string,
): AgentTunnelRoleStats {
  if (dispatches.length === 0) return stats;
  const roles: Record<string, ReadonlyArray<AgentTunnelRoleCandidate>> = { ...stats.roles };
  for (const dispatch of dispatches) {
    const role = dispatch.role.trim();
    if (role.length === 0) continue;
    const candidates = [...(roles[role] ?? [])];
    const key = candidateKey(dispatch);
    const index = candidates.findIndex((candidate) => candidateKey(candidate) === key);
    const existing = index >= 0 ? candidates[index] : undefined;
    const base: AgentTunnelRoleCandidate = existing ?? {
      harness: dispatch.harness,
      model: dispatch.model ?? null,
      tasks: 0,
      completed: 0,
      failed: 0,
      stopped: 0,
      totalTokens: 0,
      inputTokens: 0,
      outputTokens: 0,
      cachedInputTokens: 0,
      reasoningOutputTokens: 0,
      costUsd: 0,
      pricedTasks: 0,
      durationMs: 0,
      lastUsedAt: updatedAt,
    };
    const folded = foldDispatch(base, dispatch, updatedAt);
    if (index >= 0) candidates[index] = folded;
    else candidates.push(folded);
    candidates.sort((left, right) => right.lastUsedAt.localeCompare(left.lastUsedAt));
    roles[role] = candidates.slice(0, MAX_ROLE_CANDIDATES);
  }
  return { version: ROLE_STATS_VERSION, updatedAt, roles };
}

const formatTokenCount = (value: number): string =>
  Math.round(value)
    .toString()
    .replace(/\B(?=(\d{3})+(?!\d))/g, ",");

const formatUsd = (value: number): string =>
  value >= 0.01 ? `$${value.toFixed(2)}` : `$${value.toFixed(4)}`;

const averageTokens = (candidate: AgentTunnelRoleCandidate): number =>
  candidate.tasks === 0 ? 0 : candidate.totalTokens / candidate.tasks;

/**
 * Renders the recorded candidates for the orchestrator prompt. Newest first
 * per role, capped rows, capped total: this section competes with the rules
 * files for one prompt budget.
 */
export function renderRoleCandidates(stats: AgentTunnelRoleStats): string {
  const roles = Object.keys(stats.roles).sort();
  const rows: string[] = [];
  let clipped = false;
  for (const role of roles) {
    const candidates = [...(stats.roles[role] ?? [])].sort((left, right) =>
      right.lastUsedAt.localeCompare(left.lastUsedAt),
    );
    if (candidates.length > MAX_ROLE_ROWS) clipped = true;
    for (const candidate of candidates.slice(0, MAX_ROLE_ROWS)) {
      rows.push(
        `| ${role} | ${candidate.harness} | ${candidate.model ?? "default"} | ${candidate.tasks} | ${candidate.completed} | ${candidate.failed} | ${formatTokenCount(averageTokens(candidate))} | ${formatTokenCount(candidate.totalTokens)} | ${candidate.pricedTasks > 0 ? formatUsd(candidate.costUsd) : "—"} | ${candidate.lastUsedAt.slice(0, 10) || "—"} |`,
      );
    }
  }
  if (rows.length === 0) return "";
  const section = `## Recorded role candidates

Only the newest rows per role are kept; the rest are dropped as the ledger grows.
Completion rate is the first signal, tokens and cost the second: a cheap model that
finishes the task beats an expensive one that stalls.${clipped ? "\n_(Older candidates omitted.)_" : ""}

| Role | Harness | Model | Runs | Completed | Failed | Avg tokens | Total tokens | Cost | Last used |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
${rows.join("\n")}`;
  return section.length <= MAX_ROLE_STATS_PROMPT_CHARS
    ? section
    : `${section.slice(0, MAX_ROLE_STATS_PROMPT_CHARS - 20).trimEnd()}\n_(truncated)_`;
}

export const makeAgentTunnelInheritedContext = (contextDir: string) =>
  Effect.gen(function* () {
    const fs = yield* FileSystem.FileSystem;
    const path = yield* Path.Path;
    const semaphore = yield* Semaphore.make(1);
    const rulesDir = path.join(contextDir, RULES_DIR);
    const guidePath = path.join(contextDir, GUIDE_FILE);
    const logPath = path.join(contextDir, LOG_FILE);
    const lockPath = path.join(contextDir, LOCK_FILE);
    const roleStatsPath = path.join(contextDir, ROLE_STATS_FILE);

    const readIfExists = (filePath: string) =>
      fs.readFileString(filePath).pipe(Effect.orElseSucceed(() => ""));

    // The store closes over its services so its callers can run the returned
    // Effects without re-providing context.
    const writeAtomically = (filePath: string, contents: string) =>
      writeFileStringAtomically({ filePath, contents }).pipe(
        Effect.provideService(FileSystem.FileSystem, fs),
        Effect.provideService(Path.Path, path),
      );

    const listRulesFiles = Effect.gen(function* () {
      const names = yield* fs
        .readDirectory(rulesDir)
        .pipe(Effect.orElseSucceed(() => [] as ReadonlyArray<string>));
      return names
        .filter((name) => name.endsWith(".md"))
        .sort()
        .map((fileName) => ({
          fileName,
          description:
            SEED_RULES.find((seed) => seed.fileName === fileName)?.description ??
            "Rules added by a workflow.",
        }));
    });

    const ensureSeeded = Effect.gen(function* () {
      yield* fs.makeDirectory(rulesDir, { recursive: true });
      for (const rule of SEED_RULES) {
        const target = path.join(rulesDir, rule.fileName);
        if (!(yield* fs.exists(target))) {
          yield* writeAtomically(target, renderSeedRule(rule));
        }
      }
      if (!(yield* fs.exists(guidePath))) {
        const updatedAt = DateTime.formatIso(DateTime.makeUnsafe(yield* Clock.currentTimeMillis));
        yield* writeAtomically(guidePath, renderGuide(yield* listRulesFiles, updatedAt));
      }
      if (!(yield* fs.exists(logPath))) {
        yield* writeAtomically(logPath, renderLogHeader());
      }
    }).pipe(
      Effect.catch((error) =>
        trace("Failed to seed the inherited context folder.", { contextDir, error: String(error) }),
      ),
    );

    const renderForPrompt = () =>
      Effect.gen(function* () {
        yield* ensureSeeded;
        const guide = yield* readIfExists(guidePath);
        const rules = yield* listRulesFiles;
        const sections: string[] = [];
        let budget = MAX_PROMPT_CHARS;
        if (guide.trim().length > 0) {
          const clippedGuide = clipTail(guide, Math.min(MAX_GUIDE_PROMPT_CHARS, budget));
          sections.push(clippedGuide);
          budget -= clippedGuide.length;
        }
        // Candidates come before the rules files: they are the freshest signal
        // for model choice, and a table never survives being clipped from the top.
        const updatedAt = DateTime.formatIso(DateTime.makeUnsafe(yield* Clock.currentTimeMillis));
        const candidates = renderRoleCandidates(
          parseRoleStats(yield* readIfExists(roleStatsPath), updatedAt),
        );
        if (candidates.length > 0) {
          if (candidates.length <= budget) {
            sections.push(candidates);
            budget -= candidates.length;
          } else {
            sections.push(`_(role candidates omitted to fit the prompt budget)_`);
          }
        }
        for (const rule of rules) {
          if (budget <= 0) {
            sections.push(`_(remaining rules files omitted to fit the prompt budget)_`);
            break;
          }
          const contents = yield* readIfExists(path.join(rulesDir, rule.fileName));
          if (contents.trim().length === 0) continue;
          const clipped = clipTail(contents, Math.min(MAX_RULE_FILE_PROMPT_CHARS, budget));
          sections.push(`### ${RULES_DIR}/${rule.fileName}\n\n${clipped}`);
          budget -= clipped.length;
        }
        return sections.join("\n\n");
      });

    const acquireLock = Effect.gen(function* () {
      for (let attempt = 0; attempt < LOCK_MAX_ATTEMPTS; attempt += 1) {
        const acquired = yield* fs.writeFileString(lockPath, "", { flag: "wx" }).pipe(
          Effect.as(true),
          Effect.catch((error) =>
            isAlreadyExists(error) ? Effect.succeed(false) : Effect.fail(error),
          ),
        );
        if (acquired) return true;

        const now = yield* Clock.currentTimeMillis;
        const info = yield* fs.stat(lockPath).pipe(Effect.option);
        const mtime = Option.flatMap(info, (value) => value.mtime);
        if (Option.isSome(mtime) && now - mtime.value.getTime() > LOCK_STALE_MS) {
          yield* fs.remove(lockPath, { force: true }).pipe(Effect.ignore);
          continue;
        }
        yield* Effect.sleep(LOCK_RETRY_DELAY);
      }
      return false;
    }).pipe(Effect.orElseSucceed(() => false));

    const releaseLock = fs.remove(lockPath, { force: true }).pipe(Effect.ignore);

    const appendEntries = (
      learnings: ReadonlyArray<AgentTunnelContextLearning>,
      dispatches: ReadonlyArray<AgentTunnelDispatchOutcome>,
      attribution: AgentTunnelContextAttribution,
    ) =>
      Effect.gen(function* () {
        const updatedAt = DateTime.formatIso(DateTime.makeUnsafe(yield* Clock.currentTimeMillis));
        const project = path.basename(attribution.projectCwd) || attribution.projectCwd;
        const groups = new Map<string, string[]>();
        for (const learning of learnings) {
          const entries = groups.get(learning.category) ?? [];
          entries.push(learning.entry);
          groups.set(learning.category, entries);
        }

        for (const [category, entries] of groups) {
          const target = path.join(rulesDir, `${category}.md`);
          const existing = yield* readIfExists(target);
          const bullets = entries.map((entry) => `- ${entry}`).join("\n");
          const block = `\n\n## ${updatedAt} · ${attribution.status}\n\n_From thread \`${attribution.threadId}\`, project \`${project}\`, workflow \`${attribution.workflowId}\`._\n\n${bullets}\n`;
          const prefix = existing.trim().length > 0 ? existing.trimEnd() : `# ${category}`;
          yield* writeAtomically(target, `${prefix}${block}`);
        }

        // The candidate ledger is machine-owned: read, fold this workflow's
        // outcomes in, and write the whole file back. It never depends on the
        // rules files, so a workflow that taught nothing still updates it.
        if (dispatches.length > 0) {
          const stats = mergeDispatchOutcomes(
            parseRoleStats(yield* readIfExists(roleStatsPath), updatedAt),
            dispatches,
            updatedAt,
          );
          yield* writeAtomically(roleStatsPath, `${encodeRoleStats(stats)}\n`);
        }

        const summary = [...groups].map(([category, entries]) => `${category} ×${entries.length}`);
        const logEntry = `- ${updatedAt} · ${attribution.status} · project \`${project}\` · thread \`${attribution.threadId}\` · workflow \`${attribution.workflowId}\` · ${learnings.length} ${learnings.length === 1 ? "entry" : "entries"} (${summary.join(", ")}) · ${dispatches.length} ${dispatches.length === 1 ? "agent outcome" : "agent outcomes"}`;
        const existingLog = yield* readIfExists(logPath);
        const logPrefix =
          existingLog.trim().length > 0 ? existingLog.trimEnd() : renderLogHeader().trimEnd();
        yield* writeAtomically(logPath, `${logPrefix}\n\n${logEntry}\n`);

        if (learnings.length > 0) {
          yield* writeAtomically(guidePath, renderGuide(yield* listRulesFiles, updatedAt));
        }

        return { learnings: learnings.length, candidates: dispatches.length };
      });

    const record = (input: AgentTunnelInheritedContextRecord) => {
      const seen = new Set<string>();
      const learnings: AgentTunnelContextLearning[] = [];
      for (const learning of input.learnings) {
        const normalized = normalizeContextLearning(learning.category, learning.entry);
        if (normalized === null) continue;
        const key = `${normalized.category}\u0000${normalized.entry}`;
        if (seen.has(key)) continue;
        seen.add(key);
        learnings.push(normalized);
      }
      const dispatches = (input.dispatches ?? []).filter(
        (dispatch) => dispatch.role.trim().length > 0,
      );
      if (learnings.length === 0 && dispatches.length === 0) {
        return Effect.succeed({ learnings: 0, candidates: 0 });
      }

      return Effect.gen(function* () {
        yield* ensureSeeded;
        return yield* semaphore.withPermits(1)(
          Effect.gen(function* () {
            const acquired = yield* acquireLock;
            if (!acquired) {
              yield* trace("Another orchestrator held the context lock too long; skipping flush.", {
                contextDir,
              });
              return { learnings: 0, candidates: 0 };
            }
            return yield* appendEntries(learnings, dispatches, input.attribution).pipe(
              Effect.ensuring(releaseLock),
              Effect.catch((error) =>
                trace("Failed to append inherited context entries.", {
                  contextDir,
                  error: String(error),
                }).pipe(Effect.as({ learnings: 0, candidates: 0 })),
              ),
            );
          }),
        );
      });
    };

    return { renderForPrompt, record } satisfies AgentTunnelInheritedContext;
  });
