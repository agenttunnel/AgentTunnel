// @effect-diagnostics nodeBuiltinImport:off
/**
 * Read-only access to persisted subagent output logs for the Agents surface's
 * drill-in view.
 *
 * Containment rules (same shape as workflowScriptQuery):
 * - the resolved realpath must live under <providerLogsDir>/agent-output;
 * - only .log leaf files are served;
 * - reads are size-capped rather than failed, with a truncation marker.
 *
 * The client-supplied path is a hint from the subagent's task payload; it is
 * never trusted beyond these checks.
 */
import * as NodeFSP from "node:fs/promises";
import * as NodePath from "node:path";

import { OrchestrationAgentLogEntry, OrchestrationGetAgentOutputError } from "@t3tools/contracts";
import * as Effect from "effect/Effect";
import * as Option from "effect/Option";
import * as Schema from "effect/Schema";

import { ServerConfig } from "../config.ts";

const OUTPUT_BYTE_CAP = 256 * 1024;
const AGENT_LOG_ENTRY_LIMIT = 500;

const decodeLogEntry = Schema.decodeUnknownOption(OrchestrationAgentLogEntry);

function parseLogEntries(contents: string): {
  readonly entries: ReadonlyArray<OrchestrationAgentLogEntry>;
  readonly overflowed: boolean;
} {
  const entries: OrchestrationAgentLogEntry[] = [];
  for (const line of contents.split("\n")) {
    if (line.trim().length === 0) continue;
    let candidate: unknown;
    try {
      candidate = JSON.parse(line);
    } catch {
      entries.push({ kind: "raw", text: line });
      continue;
    }
    const decoded = decodeLogEntry(candidate);
    entries.push(Option.isSome(decoded) ? decoded.value : { kind: "raw", text: line });
  }
  if (entries.length <= AGENT_LOG_ENTRY_LIMIT) {
    return { entries, overflowed: false };
  }
  return { entries: entries.slice(-AGENT_LOG_ENTRY_LIMIT), overflowed: true };
}

export const agentOutputRoot = (providerLogsDir: string): string =>
  NodePath.join(providerLogsDir, "agent-output");

export const readAgentOutput = Effect.fn("orchestration.readAgentOutput")(function* (input: {
  readonly outputPath: string;
}) {
  const requested = input.outputPath;

  if (!NodePath.isAbsolute(requested) || NodePath.extname(requested) !== ".log") {
    return yield* Effect.fail(
      new OrchestrationGetAgentOutputError({ reason: "invalid-path", outputPath: requested }),
    );
  }

  const serverConfig = yield* ServerConfig;
  const root = yield* Effect.tryPromise({
    try: () => NodeFSP.realpath(agentOutputRoot(serverConfig.providerLogsDir)),
    catch: (cause) =>
      new OrchestrationGetAgentOutputError({
        reason: "root-unavailable",
        outputPath: requested,
        cause,
      }),
  });

  const resolved = yield* Effect.tryPromise({
    try: () => NodeFSP.realpath(requested),
    catch: (cause) =>
      new OrchestrationGetAgentOutputError({
        reason: "not-found",
        outputPath: requested,
        cause,
      }),
  });

  if (resolved !== root && !resolved.startsWith(`${root}${NodePath.sep}`)) {
    return yield* Effect.fail(
      new OrchestrationGetAgentOutputError({ reason: "outside-root", outputPath: resolved }),
    );
  }
  if (NodePath.extname(resolved) !== ".log") {
    return yield* Effect.fail(
      new OrchestrationGetAgentOutputError({ reason: "not-log", outputPath: resolved }),
    );
  }

  const read = yield* Effect.tryPromise({
    try: async () => {
      const handle = await NodeFSP.open(resolved, "r");
      try {
        const stat = await handle.stat();
        if (!stat.isFile()) {
          return { failure: "not-regular-file" as const };
        }
        const pathStat = await NodeFSP.lstat(resolved);
        if (stat.ino !== pathStat.ino || stat.dev !== pathStat.dev) {
          return { failure: "changed-during-read" as const };
        }
        const truncated = stat.size > OUTPUT_BYTE_CAP;
        const buffer = Buffer.alloc(Math.min(stat.size, OUTPUT_BYTE_CAP));
        const { bytesRead } = await handle.read(buffer, 0, buffer.length, 0);
        return {
          contents: buffer.subarray(0, bytesRead).toString("utf8"),
          truncated,
        };
      } finally {
        await handle.close();
      }
    },
    catch: (cause) =>
      new OrchestrationGetAgentOutputError({
        reason: "read-failed",
        outputPath: resolved,
        cause,
      }),
  });
  if ("failure" in read) {
    return yield* new OrchestrationGetAgentOutputError({
      reason: read.failure,
      outputPath: resolved,
    });
  }

  const parsed = parseLogEntries(read.contents);
  const raw = yield* readOptionalRawLog(resolved, root);

  return {
    outputPath: resolved,
    entries: parsed.entries,
    ...(raw === undefined ? {} : { raw }),
    truncated: read.truncated || parsed.overflowed,
  };
});

/** Sibling `*.raw.log` stream, when the adapter captured one. */
const readOptionalRawLog = (
  resolvedOutputPath: string,
  root: string,
): Effect.Effect<{ readonly contents: string; readonly truncated: boolean } | undefined> =>
  Effect.gen(function* () {
    const rawPath = resolvedOutputPath.endsWith(".log")
      ? `${resolvedOutputPath.slice(0, -4)}.raw.log`
      : `${resolvedOutputPath}.raw.log`;
    const resolvedRaw = yield* Effect.tryPromise(() => NodeFSP.realpath(rawPath)).pipe(
      Effect.option,
    );
    if (Option.isNone(resolvedRaw)) {
      return undefined;
    }
    const raw = resolvedRaw.value;
    if (raw !== root && !raw.startsWith(`${root}${NodePath.sep}`)) {
      return undefined;
    }
    if (NodePath.extname(raw) !== ".log") {
      return undefined;
    }
    const read = yield* Effect.tryPromise(async () => {
      const handle = await NodeFSP.open(raw, "r");
      try {
        const stat = await handle.stat();
        if (!stat.isFile()) {
          return undefined;
        }
        const truncated = stat.size > OUTPUT_BYTE_CAP;
        const buffer = Buffer.alloc(Math.min(stat.size, OUTPUT_BYTE_CAP));
        const { bytesRead } = await handle.read(buffer, 0, buffer.length, 0);
        return {
          contents: buffer.subarray(0, bytesRead).toString("utf8"),
          truncated,
        };
      } finally {
        await handle.close();
      }
    }).pipe(Effect.option);
    return Option.isSome(read) ? read.value : undefined;
  });
