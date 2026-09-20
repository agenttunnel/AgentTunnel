import * as Clock from "effect/Clock";
import * as DateTime from "effect/DateTime";
import * as Effect from "effect/Effect";
import * as FileSystem from "effect/FileSystem";
import * as Option from "effect/Option";
import * as Path from "effect/Path";
import type * as PlatformError from "effect/PlatformError";
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

export interface AgentTunnelInheritedContextRecord {
  readonly learnings: ReadonlyArray<AgentTunnelContextLearning>;
  readonly attribution: AgentTunnelContextAttribution;
}

/**
 * File-backed cross-project memory shared by every orchestrator in this
 * environment. `record` serializes flushes with an in-process semaphore and a
 * cross-process lock file so only one orchestrator updates the context at a time.
 */
export interface AgentTunnelInheritedContext {
  /** Renders the guide plus every rules file for injection into a prompt. */
  readonly renderForPrompt: () => Effect.Effect<string>;
  /** Appends learnings under the semaphore; returns how many entries landed. */
  readonly record: (input: AgentTunnelInheritedContextRecord) => Effect.Effect<number>;
}

const RULES_DIR = "rules";
const GUIDE_FILE = "GUIDE.md";
const LOG_FILE = "LOG.md";
const LOCK_FILE = ".lock";
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

export const makeAgentTunnelInheritedContext = (contextDir: string) =>
  Effect.gen(function* () {
    const fs = yield* FileSystem.FileSystem;
    const path = yield* Path.Path;
    const semaphore = yield* Semaphore.make(1);
    const rulesDir = path.join(contextDir, RULES_DIR);
    const guidePath = path.join(contextDir, GUIDE_FILE);
    const logPath = path.join(contextDir, LOG_FILE);
    const lockPath = path.join(contextDir, LOCK_FILE);

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

        const summary = [...groups].map(([category, entries]) => `${category} ×${entries.length}`);
        const logEntry = `- ${updatedAt} · ${attribution.status} · project \`${project}\` · thread \`${attribution.threadId}\` · workflow \`${attribution.workflowId}\` · ${learnings.length} ${learnings.length === 1 ? "entry" : "entries"} (${summary.join(", ")})`;
        const existingLog = yield* readIfExists(logPath);
        const logPrefix =
          existingLog.trim().length > 0 ? existingLog.trimEnd() : renderLogHeader().trimEnd();
        yield* writeAtomically(logPath, `${logPrefix}\n\n${logEntry}\n`);

        yield* writeAtomically(guidePath, renderGuide(yield* listRulesFiles, updatedAt));

        return learnings.length;
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
      if (learnings.length === 0) return Effect.succeed(0);

      return Effect.gen(function* () {
        yield* ensureSeeded;
        return yield* semaphore.withPermits(1)(
          Effect.gen(function* () {
            const acquired = yield* acquireLock;
            if (!acquired) {
              yield* trace("Another orchestrator held the context lock too long; skipping flush.", {
                contextDir,
              });
              return 0;
            }
            return yield* appendEntries(learnings, input.attribution).pipe(
              Effect.ensuring(releaseLock),
              Effect.catch((error) =>
                trace("Failed to append inherited context entries.", {
                  contextDir,
                  error: String(error),
                }).pipe(Effect.as(0)),
              ),
            );
          }),
        );
      });
    };

    return { renderForPrompt, record } satisfies AgentTunnelInheritedContext;
  });
