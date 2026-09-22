import * as Clock from "effect/Clock";
import * as DateTime from "effect/DateTime";
import * as Effect from "effect/Effect";
import * as FileSystem from "effect/FileSystem";
import * as Option from "effect/Option";
import * as Path from "effect/Path";
import * as Schema from "effect/Schema";

import { writeFileStringAtomically } from "../../atomicWrite.ts";
import { parseSharedContextDump } from "./SharedContext.ts";

export type AgentTunnelThreadRunStatus = "completed" | "failed" | "stopped";

/** One finished orchestrator turn in a thread. */
export interface AgentTunnelThreadRun {
  readonly at: string;
  readonly task: string;
  readonly status: AgentTunnelThreadRunStatus;
  readonly summary: string;
}

/**
 * A thread's durable orchestrator memory: the notebook the last turn ended
 * with, plus what every turn in the thread was asked to do and how it ended.
 */
export interface AgentTunnelThreadContext {
  readonly threadId: string;
  readonly updatedAt: string;
  /** Rendered notebook (`AgentTunnelSharedContext.renderForPrompt`) of the last turn. */
  readonly sharedContext: string;
  /** Oldest first, newest last. */
  readonly runs: ReadonlyArray<AgentTunnelThreadRun>;
}

export interface AgentTunnelThreadContextRecord {
  readonly threadId: string;
  readonly task: string;
  readonly status: AgentTunnelThreadRunStatus;
  readonly summary: string;
  /** The notebook this turn ended with; omitted when the turn crashed. */
  readonly sharedContext?: string;
}

/**
 * Per-thread orchestrator context that outlives the process. The adapter used to
 * keep the notebook and the run history in memory only, so closing the laptop
 * lost everything the team had established: the next turn started from scratch.
 * One JSON file per thread, written atomically, read back when a session starts.
 */
export interface AgentTunnelThreadContextStore {
  readonly load: (threadId: string) => Effect.Effect<AgentTunnelThreadContext | null>;
  readonly record: (input: AgentTunnelThreadContextRecord) => Effect.Effect<void>;
}

/**
 * What a session that starts on an existing thread inherits: the thread's
 * recent runs, oldest first, and the notebook its last turn ended with. A
 * thread with no record yet resumes empty rather than failing.
 */
export interface AgentTunnelThreadResume {
  readonly threadHistory: ReadonlyArray<{ readonly task: string; readonly summary: string }>;
  readonly sharedContext: string | null;
}

export function threadContextResume(
  prior: AgentTunnelThreadContext | null,
): AgentTunnelThreadResume {
  if (prior === null) {
    return { threadHistory: [], sharedContext: null };
  }
  return {
    threadHistory: prior.runs.slice(-MAX_RESUMED_RUNS).map((run) => ({
      task: run.task,
      summary: run.summary,
    })),
    sharedContext: prior.sharedContext.trim().length > 0 ? prior.sharedContext : null,
  };
}

const THREADS_DIR = "threads";
/** Turns kept per thread: long enough to resume a real session, bounded so a
    months-old thread cannot grow its prompt forever. */
const MAX_RUNS = 50;
/** Runs handed to a resumed session: the orchestrator prompt renders the last
    few, so this only needs to cover the prompt's window plus slack. */
const MAX_RESUMED_RUNS = 10;
/** Next steps reprinted when a thread resumes; the rest stay in the notebook. */
const MAX_RESUME_STEPS = 3;
const MAX_SHARED_CONTEXT_CHARS = 24_000;
const MAX_SUMMARY_CHARS = 600;
const MAX_TASK_CHARS = 400;
const MAX_THREAD_ID_CHARS = 200;

/**
 * The line a resumed session prints, so the user can see where the thread
 * stands: how much context came back, what the last turn produced, and the next
 * steps that turn left. Returns null for a thread with no record yet — a first
 * session has nothing to resume and says nothing.
 */
export function threadResumeNotice(prior: AgentTunnelThreadContext | null): string | null {
  const resumed = threadContextResume(prior);
  if (resumed.threadHistory.length === 0) return null;

  const lastSummary = resumed.threadHistory.at(-1)?.summary.trim() ?? "";
  const nextSteps = (parseSharedContextDump(resumed.sharedContext ?? "").get("next_steps") ?? "")
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0)
    .slice(0, MAX_RESUME_STEPS);

  const turns = resumed.threadHistory.length;
  return [
    `Resumed with ${turns} prior turn${turns === 1 ? "" : "s"} of context.`,
    lastSummary.length > 0 ? `**Last turn:** ${lastSummary}` : null,
    nextSteps.length > 0 ? `**Next steps from that turn:**\n${nextSteps.join("\n")}` : null,
  ]
    .filter((part): part is string => part !== null)
    .join("\n\n");
}

const StoredRun = Schema.Struct({
  at: Schema.String,
  task: Schema.String,
  status: Schema.Literals(["completed", "failed", "stopped"]),
  summary: Schema.String,
});

const StoredThreadContext = Schema.Struct({
  threadId: Schema.String,
  updatedAt: Schema.String,
  sharedContext: Schema.String,
  runs: Schema.Array(StoredRun),
});

const decodeStoredThreadContext = Schema.decodeUnknownEffect(
  Schema.fromJsonString(StoredThreadContext),
);
const encodeStoredThreadContext = Schema.encodeSync(Schema.fromJsonString(StoredThreadContext));

const trace = (message: string, fields: Record<string, string>) =>
  Effect.logWarning(message, fields).pipe(
    Effect.annotateLogs({ scope: "agenttunnel.thread-context" }),
  );

/** Keeps the newest text when a value outgrows its budget. */
function clipTail(text: string, max: number): string {
  return text.length <= max ? text : `…${text.slice(text.length - max + 1).trimStart()}`;
}

function clip(text: string, max: number): string {
  const normalized = text.replace(/\s+/g, " ").trim();
  return normalized.length <= max ? normalized : `${normalized.slice(0, max - 1).trimEnd()}…`;
}

/**
 * Thread ids reach us from the client, so they are not trusted as path
 * segments: anything outside the safe set is folded into `_` and the name is
 * length-capped.
 */
function fileNameFor(threadId: string): string {
  return `${threadId.replace(/[^A-Za-z0-9._-]/g, "_").slice(0, MAX_THREAD_ID_CHARS)}.json`;
}

export const makeAgentTunnelThreadContext = (contextDir: string) =>
  Effect.gen(function* () {
    const fs = yield* FileSystem.FileSystem;
    const path = yield* Path.Path;
    const threadsDir = path.join(contextDir, THREADS_DIR);

    // The store closes over its services so its callers can run the returned
    // Effects without re-providing context.
    const writeAtomically = (filePath: string, contents: string) =>
      writeFileStringAtomically({ filePath, contents }).pipe(
        Effect.provideService(FileSystem.FileSystem, fs),
        Effect.provideService(Path.Path, path),
      );

    const filePathFor = (threadId: string) => path.join(threadsDir, fileNameFor(threadId));

    const load = (threadId: string) =>
      Effect.gen(function* () {
        const raw = yield* fs
          .readFileString(filePathFor(threadId))
          .pipe(Effect.orElseSucceed(() => ""));
        if (raw.trim().length === 0) return null;
        const decoded = yield* decodeStoredThreadContext(raw).pipe(Effect.option);
        return Option.getOrNull(decoded);
      });

    /**
     * Appends this turn to the thread's record. Failures are logged, never
     * raised: losing the durable copy must not fail the turn the user asked for.
     */
    const record = (input: AgentTunnelThreadContextRecord) =>
      Effect.gen(function* () {
        const existing = yield* load(input.threadId);
        const at = DateTime.formatIso(DateTime.makeUnsafe(yield* Clock.currentTimeMillis));
        const run: AgentTunnelThreadRun = {
          at,
          task: clip(input.task, MAX_TASK_CHARS),
          status: input.status,
          summary: clip(input.summary, MAX_SUMMARY_CHARS),
        };
        const runs = [...(existing?.runs ?? []), run].slice(-MAX_RUNS);
        const dumped = input.sharedContext?.trim() ?? "";
        const sharedContext =
          dumped.length > 0
            ? clipTail(dumped, MAX_SHARED_CONTEXT_CHARS)
            : (existing?.sharedContext ?? "");
        const next: AgentTunnelThreadContext = {
          threadId: input.threadId,
          updatedAt: at,
          sharedContext,
          runs,
        };
        yield* writeAtomically(filePathFor(input.threadId), `${encodeStoredThreadContext(next)}\n`);
      }).pipe(
        Effect.catch((error) =>
          trace("Failed to persist thread context.", {
            threadId: input.threadId,
            error: String(error),
          }),
        ),
      );

    return { load, record } satisfies AgentTunnelThreadContextStore;
  });
