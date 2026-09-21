import type { OrchestrationAgentLogEntry } from "@t3tools/contracts";

import type { HarnessStepUsage } from "./harnessUsage.ts";
import type { AgentTunnelProcessSpec } from "./Orchestrator.ts";

/**
 * AgentTunnel shells out to a harness CLI per role. The default harness is
 * `opencode`, whose `run` subcommand streams events over stdout/stderr. Other
 * harnesses (claude, codex, cursor, grok) have their own flag sets; this module
 * keeps the harness-specific argument and output translation at the adapter
 * boundary so the orchestrator stays transport-agnostic.
 */

function baseName(harness: string): string {
  const parts = harness.split(/[\\/]/);
  return parts[parts.length - 1] ?? harness;
}

export function isOpenCodeHarness(harness: string): boolean {
  return baseName(harness).replace(/\.exe$/, "") === "opencode";
}

/** Pi's CLI (`pi --mode json -p <prompt>`) is its own transport. */
export function isPiHarness(harness: string): boolean {
  return (
    baseName(harness)
      .replace(/\.exe$/, "")
      .toLowerCase() === "pi"
  );
}

export type AgentTunnelHarnessKind = "opencode" | "pi" | "other";

/** Which CLI protocol a harness speaks, so spawn args and parsing branch once. */
export function agentTunnelHarnessKind(harness: string): AgentTunnelHarnessKind {
  if (isOpenCodeHarness(harness)) return "opencode";
  if (isPiHarness(harness)) return "pi";
  return "other";
}

export function buildAgentTunnelSpawnArgs(spec: AgentTunnelProcessSpec): string[] {
  if (isPiHarness(spec.harness)) {
    // One prompt per process: `--no-session` keeps the workflow's short-lived
    // role runs out of pi's session store. Pi has no sandbox flag, so
    // `dangerouslySkipPermissions` is intentionally ignored here.
    const args = ["--mode", "json", "-p", spec.prompt, "--no-session"];
    if (spec.model) args.push("--model", spec.model);
    if (spec.variant) args.push("--thinking", spec.variant);
    return args;
  }
  const args = ["run"];
  if (spec.model) args.push("-m", spec.model);
  if (spec.dangerouslySkipPermissions) {
    args.push(isOpenCodeHarness(spec.harness) ? "--auto" : "--dangerously-skip-permissions");
  }
  if (isOpenCodeHarness(spec.harness)) {
    args.push("--format", "json", "--thinking");
    if (spec.variant) args.push("--variant", spec.variant);
  }
  args.push(spec.prompt);
  return args;
}

const TOOL_SUMMARY_MAX = 100;
const TOOL_TITLE_MAX = 200;
const TOOL_BODY_MAX = 8_000;

function truncate(value: string, max: number): string {
  return value.length <= max ? value : `${value.slice(0, max - 1)}…`;
}

function truncateSummary(value: string): string {
  return truncate(value, TOOL_SUMMARY_MAX);
}

function summarizeToolInput(input: unknown): string {
  if (!input || typeof input !== "object") return "";
  const record = input as Record<string, unknown>;
  const primary = record.command ?? record.path ?? record.file ?? record.pattern ?? record.query;
  if (typeof primary === "string" && primary.length > 0) return truncateSummary(primary);
  return truncateSummary(JSON.stringify(record));
}

function formatToolInput(input: unknown): string | undefined {
  if (input === undefined || input === null) return undefined;
  try {
    const json = JSON.stringify(input, null, 2);
    return json === undefined ? undefined : truncate(json, TOOL_BODY_MAX);
  } catch {
    return undefined;
  }
}

export type AgentLogEntry = OrchestrationAgentLogEntry;

function nonNegativeTokenCount(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value >= 0
    ? Math.round(value)
    : undefined;
}

/**
 * Positive dollar amount, or undefined: a harness that has no rate for the
 * model reports 0, which must read as "not reported" instead of "free".
 */
function reportedCostUsd(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : undefined;
}

/**
 * Token frame from an OpenCode `step_finish` event. Each frame reports the
 * step's own usage (they are deltas, not session totals); `part.tokens.total`
 * includes input, output, reasoning, and cache reads and writes, and
 * `part.cost` is the step's API-equivalent cost when OpenCode priced it.
 */
export function parseOpenCodeStepUsage(raw: string): HarnessStepUsage | undefined {
  // The adapter runs this over every harness line; skip the JSON parse for the
  // overwhelming majority that cannot be a step frame.
  if (!raw.includes("step_finish")) {
    return undefined;
  }
  const event = parseJsonRecord(raw);
  if (event?.type !== "step_finish") {
    return undefined;
  }
  const part = asRecord(event.part);
  if (part === undefined) {
    return undefined;
  }
  const tokens = asRecord(part.tokens);
  if (tokens === undefined) {
    return undefined;
  }
  const totalTokens = nonNegativeTokenCount(tokens.total);
  if (totalTokens === undefined || totalTokens <= 0) {
    return undefined;
  }
  const cache = asRecord(tokens.cache) ?? {};
  const costUsd = reportedCostUsd(part.cost);
  return {
    totalTokens,
    inputTokens: nonNegativeTokenCount(tokens.input) ?? 0,
    cachedInputTokens:
      (nonNegativeTokenCount(cache.read) ?? 0) + (nonNegativeTokenCount(cache.write) ?? 0),
    outputTokens: nonNegativeTokenCount(tokens.output) ?? 0,
    reasoningOutputTokens: nonNegativeTokenCount(tokens.reasoning) ?? 0,
    ...(costUsd === undefined ? {} : { costUsd }),
  };
}

/**
 * Token frame from a pi `message_end` event, normalized to the same shape as
 * OpenCode's: pi's `input` is the uncached input, `cacheRead`/`cacheWrite` the
 * cached subset, and `reasoning` is folded INTO `output` (totalTokens covers
 * input + cache + output), so the plain output is the remainder.
 */
export function parsePiStepUsage(raw: string): HarnessStepUsage | undefined {
  if (!raw.includes("message_end")) {
    return undefined;
  }
  const event = parseJsonRecord(raw);
  if (event?.type !== "message_end") return undefined;
  const message = asRecord(event.message);
  if (message?.role !== "assistant") return undefined;
  const usage = asRecord(message.usage);
  if (usage === undefined) return undefined;

  const cachedInputTokens =
    (nonNegativeTokenCount(usage.cacheRead) ?? 0) + (nonNegativeTokenCount(usage.cacheWrite) ?? 0);
  const reasoningOutputTokens = nonNegativeTokenCount(usage.reasoning) ?? 0;
  const inputTokens = nonNegativeTokenCount(usage.input) ?? 0;
  const outputTokens = Math.max(
    0,
    (nonNegativeTokenCount(usage.output) ?? 0) - reasoningOutputTokens,
  );
  const totalTokens =
    nonNegativeTokenCount(usage.totalTokens) ?? inputTokens + cachedInputTokens + outputTokens;
  if (totalTokens <= 0) return undefined;

  const costUsd = reportedCostUsd(asRecord(usage.cost)?.total);
  return {
    totalTokens,
    inputTokens,
    cachedInputTokens,
    outputTokens,
    reasoningOutputTokens,
    ...(costUsd === undefined ? {} : { costUsd }),
  };
}

interface OpenCodeToolUseState {
  readonly title?: unknown;
  readonly input?: unknown;
  readonly output?: unknown;
  readonly error?: unknown;
  readonly status?: unknown;
}

interface OpenCodeToolUsePart {
  readonly tool?: unknown;
  readonly callID?: unknown;
  readonly state?: OpenCodeToolUseState;
}

interface OpenCodePart {
  readonly text?: unknown;
}

interface OpenCodeEvent {
  readonly type?: unknown;
  readonly part?: unknown;
  readonly error?: unknown;
}

function isToolStatus(value: unknown): value is "pending" | "running" | "completed" | "error" {
  return value === "pending" || value === "running" || value === "completed" || value === "error";
}

function errorMessage(error: unknown): string | undefined {
  if (typeof error === "string" && error.length > 0) return error;
  if (!error || typeof error !== "object") return undefined;
  const record = error as Record<string, unknown>;
  const data = record.data;
  if (data && typeof data === "object") {
    const message = (data as Record<string, unknown>).message;
    if (typeof message === "string" && message.length > 0) return message;
  }
  if (typeof record.message === "string" && record.message.length > 0) return record.message;
  if (typeof record.name === "string" && record.name.length > 0) return record.name;
  return undefined;
}

/**
 * Structured log entries for one raw harness line. OpenCode's `run --format
 * json` stream is translated into typed entries; anything else stays a raw
 * line so non-opencode harnesses still produce a readable log.
 */
export function parseOpenCodeLogLine(raw: string): ReadonlyArray<AgentLogEntry> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return [{ kind: "raw", text: raw }];
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    return [{ kind: "raw", text: raw }];
  }

  const event = parsed as OpenCodeEvent;
  if (event.type === "text" || event.type === "reasoning") {
    const text = (event.part as OpenCodePart | undefined)?.text;
    if (typeof text !== "string" || text.length === 0) return [];
    return event.type === "text" ? [{ kind: "text", text }] : [{ kind: "reasoning", text }];
  }
  if (event.type === "tool_use") {
    const part = event.part as OpenCodeToolUsePart | undefined;
    if (!part) return [];
    const tool = typeof part.tool === "string" && part.tool.length > 0 ? part.tool : "tool";
    const state = part.state;
    const status = isToolStatus(state?.status) ? state.status : "completed";
    const title =
      typeof state?.title === "string" && state.title.length > 0
        ? truncate(state.title, TOOL_TITLE_MAX)
        : summarizeToolInput(state?.input);
    const input = formatToolInput(state?.input);
    const output =
      typeof state?.output === "string" && state.output.length > 0
        ? truncate(state.output, TOOL_BODY_MAX)
        : undefined;
    const error =
      typeof state?.error === "string" && state.error.length > 0
        ? truncate(state.error, TOOL_BODY_MAX)
        : undefined;
    return [
      {
        kind: "tool",
        tool,
        title,
        status,
        ...(typeof part.callID === "string" && part.callID.length > 0
          ? { callId: part.callID }
          : {}),
        ...(input === undefined ? {} : { input }),
        ...(output === undefined ? {} : { output }),
        ...(error === undefined ? {} : { error }),
      },
    ];
  }
  if (event.type === "error") {
    const message = errorMessage(event.error);
    return message === undefined ? [] : [{ kind: "error", text: truncate(message, TOOL_BODY_MAX) }];
  }
  if (event.type === "step_finish") {
    const usage = parseOpenCodeStepUsage(raw);
    return usage === undefined
      ? []
      : [
          {
            kind: "usage",
            totalTokens: usage.totalTokens,
            inputTokens: usage.inputTokens,
            cachedInputTokens: usage.cachedInputTokens,
            outputTokens: usage.outputTokens,
            reasoningOutputTokens: usage.reasoningOutputTokens,
            ...(usage.costUsd === undefined ? {} : { costUsd: usage.costUsd }),
          },
        ];
  }
  return [];
}

function splitTextLines(text: string): ReadonlyArray<string> {
  return text.split("\n").filter((line) => line.length > 0);
}

/** Display lines for the orchestrator chat message, derived from log entries. */
export function parseOpenCodeJsonLine(
  raw: string,
  options: { readonly includeReasoning?: boolean } = {},
): ReadonlyArray<string> {
  const lines: string[] = [];
  for (const entry of parseOpenCodeLogLine(raw)) {
    switch (entry.kind) {
      case "text":
        lines.push(...splitTextLines(entry.text));
        break;
      case "tool":
        lines.push(entry.title.length > 0 ? `→ ${entry.tool}: ${entry.title}` : `→ ${entry.tool}`);
        break;
      case "error":
        lines.push(`✗ ${truncateSummary(entry.text)}`);
        break;
      case "raw":
        lines.push(entry.text);
        break;
      case "reasoning":
        if (options.includeReasoning === true) {
          lines.push(...splitTextLines(entry.text));
        }
        break;
      // Usage entries feed the stats view, not the chat transcript.
      case "usage":
        break;
    }
  }
  return lines;
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function parseJsonRecord(raw: string): Record<string, unknown> | undefined {
  try {
    return asRecord(JSON.parse(raw));
  } catch {
    return undefined;
  }
}

interface PiTextPart {
  readonly kind: "text" | "reasoning";
  readonly text: string;
}

/**
 * Assistant text/reasoning from a pi `message_end` payload. Pi's `--mode json`
 * stream carries the full assistant message here; deltas arrive separately on
 * `message_update`, so keying on the final message keeps the
 * `__AGENTTUNNEL_TOOL__` lines intact instead of splitting them across
 * streamed fragments.
 */
function piAssistantTextParts(message: Record<string, unknown>): ReadonlyArray<PiTextPart> {
  const content = message.content;
  if (typeof content === "string") {
    return content.length > 0 ? [{ kind: "text", text: content }] : [];
  }
  if (!Array.isArray(content)) return [];
  const parts: Array<PiTextPart> = [];
  for (const candidate of content) {
    const part = asRecord(candidate);
    if (part === undefined) continue;
    const text = part.text;
    if (typeof text !== "string" || text.length === 0) continue;
    if (part.type === "text") {
      parts.push({ kind: "text", text });
    } else if (part.type === "thinking" || part.type === "reasoning") {
      parts.push({ kind: "reasoning", text });
    }
  }
  return parts;
}

/**
 * Structured log entries for one raw pi JSONL record. Unknown frames are
 * dropped (they are internal pi bookkeeping, not workflow output); a line that
 * is not JSON is preserved verbatim.
 */
export function parsePiLogLine(raw: string): ReadonlyArray<AgentLogEntry> {
  const event = parseJsonRecord(raw);
  if (event === undefined) {
    return [{ kind: "raw", text: raw }];
  }
  if (event.type === "message_end") {
    const message = asRecord(event.message);
    if (message?.role !== "assistant") return [];
    const entries: AgentLogEntry[] = piAssistantTextParts(message).map(
      (part): AgentLogEntry =>
        part.kind === "text"
          ? { kind: "text", text: part.text }
          : { kind: "reasoning", text: part.text },
    );
    // The message's final usage is the step's own total, so it feeds the
    // per-subagent turns table exactly like OpenCode's step_finish.
    const usage = parsePiStepUsage(raw);
    if (usage !== undefined) {
      entries.push({
        kind: "usage",
        totalTokens: usage.totalTokens,
        inputTokens: usage.inputTokens,
        cachedInputTokens: usage.cachedInputTokens,
        outputTokens: usage.outputTokens,
        reasoningOutputTokens: usage.reasoningOutputTokens,
        ...(usage.costUsd === undefined ? {} : { costUsd: usage.costUsd }),
      });
    }
    return entries;
  }
  if (event.type === "error") {
    const message = errorMessage(event.error);
    return message === undefined ? [] : [{ kind: "error", text: truncate(message, TOOL_BODY_MAX) }];
  }
  return [];
}

/** Display lines for the orchestrator chat message, derived from a pi record. */
export function parsePiJsonLine(
  raw: string,
  options: { readonly includeReasoning?: boolean } = {},
): ReadonlyArray<string> {
  const lines: string[] = [];
  for (const entry of parsePiLogLine(raw)) {
    switch (entry.kind) {
      case "text":
        lines.push(...splitTextLines(entry.text));
        break;
      case "tool":
        lines.push(entry.title.length > 0 ? `→ ${entry.tool}: ${entry.title}` : `→ ${entry.tool}`);
        break;
      case "error":
        lines.push(`✗ ${truncateSummary(entry.text)}`);
        break;
      case "raw":
        lines.push(entry.text);
        break;
      case "reasoning":
        if (options.includeReasoning === true) {
          lines.push(...splitTextLines(entry.text));
        }
        break;
      // Usage entries feed the stats view, not the chat transcript.
      case "usage":
        break;
    }
  }
  return lines;
}
