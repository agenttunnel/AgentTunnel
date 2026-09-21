import type { OrchestrationAgentLogEntry } from "@t3tools/contracts";

import type { HarnessStepUsage } from "./harnessUsage.ts";
import type { AgentTunnelProcessSpec } from "./Orchestrator.ts";

/**
 * AgentTunnel shells out to a harness CLI per role. Four CLI protocols are
 * translated here — OpenCode's `run --format json`, pi's `--mode json`, Claude
 * Code's `-p --output-format stream-json`, and Codex's `exec --json` — so a
 * role's harness only decides how a process is spawned and how its stream is
 * read. Any other basename keeps the generic `run` invocation and contributes
 * no usage rather than a guess.
 */

function baseName(harness: string): string {
  const parts = harness.split(/[\\/]/);
  return parts[parts.length - 1] ?? harness;
}

/** Basename without a Windows extension, lowercased for matching. */
function harnessName(harness: string): string {
  return baseName(harness)
    .replace(/\.exe$/, "")
    .toLowerCase();
}

export function isOpenCodeHarness(harness: string): boolean {
  return harnessName(harness) === "opencode";
}

export function isPiHarness(harness: string): boolean {
  return harnessName(harness) === "pi";
}

export function isClaudeHarness(harness: string): boolean {
  return harnessName(harness) === "claude";
}

export function isCodexHarness(harness: string): boolean {
  return harnessName(harness) === "codex";
}

export type AgentTunnelHarnessKind = "opencode" | "pi" | "claude" | "codex" | "other";

/** Kinds with a transport that reads usage frames. */
export type AgentTunnelHarnessTransportKind = Exclude<AgentTunnelHarnessKind, "other">;

/** Which CLI protocol a harness speaks, so spawn args and parsing branch once. */
export function agentTunnelHarnessKind(harness: string): AgentTunnelHarnessKind {
  if (isOpenCodeHarness(harness)) return "opencode";
  if (isPiHarness(harness)) return "pi";
  if (isClaudeHarness(harness)) return "claude";
  if (isCodexHarness(harness)) return "codex";
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
  if (isClaudeHarness(spec.harness)) {
    // `--print` drives Claude headless, and stream-json (which requires
    // --verbose) is the mode that carries per-message usage and the run's cost.
    const args = ["--print", "--output-format", "stream-json", "--verbose"];
    if (spec.model) args.push("--model", spec.model);
    // Claude accepts low|medium|high|xhigh|max. The workflow's `minimal` sits
    // below `low`, so the nearest accepted level stands in instead of letting
    // Claude warn and silently fall back to its default.
    if (spec.variant) args.push("--effort", spec.variant === "minimal" ? "low" : spec.variant);
    if (spec.dangerouslySkipPermissions) args.push("--dangerously-skip-permissions");
    args.push(spec.prompt);
    return args;
  }
  if (isCodexHarness(spec.harness)) {
    // `exec --json` is Codex's JSONL mode: per-item events plus the turn's
    // usage. `--ephemeral` mirrors pi's `--no-session` (role runs stay out of
    // the session store) and `--skip-git-repo-check` keeps plain directories
    // usable, which no other harness rejects.
    const args = ["exec", "--json", "--ephemeral", "--skip-git-repo-check"];
    if (spec.model) args.push("--model", spec.model);
    // A role still has to edit its workspace to be useful; the bypass setting
    // escalates to Codex's full-access sandbox, which is what the other
    // harnesses' skip-permission flags mean.
    args.push(
      ...(spec.dangerouslySkipPermissions
        ? ["--dangerously-bypass-approvals-and-sandbox"]
        : ["--sandbox", "workspace-write"]),
    );
    if (spec.variant) args.push("-c", `model_reasoning_effort="${spec.variant}"`);
    args.push(spec.prompt);
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

/**
 * Token frame from Claude Code's `--output-format stream-json` stream. Claude
 * emits one `assistant` event per content block and each repeats that message's
 * usage — uncached `input_tokens` beside the cache-creation and cache-read
 * columns, `output_tokens` with thinking folded in as
 * `output_tokens_details.thinking_tokens` — so the step carries the message id
 * and the caller folds a message once.
 *
 * Claude prices the whole run instead of the message, and reports it on the
 * closing `result` event — after the tokens it covers — so that frame comes
 * back as a cost-only step (zero tokens, no step id) and the caller keeps the
 * last token-bearing step for it.
 */
export function parseClaudeStepUsage(raw: string): HarnessStepUsage | undefined {
  if (!raw.includes("usage") && !raw.includes("total_cost_usd")) {
    return undefined;
  }
  const event = parseJsonRecord(raw);
  if (event === undefined) return undefined;

  if (event.type === "result") {
    const costUsd = reportedCostUsd(event.total_cost_usd);
    if (costUsd === undefined) return undefined;
    return {
      totalTokens: 0,
      inputTokens: 0,
      cachedInputTokens: 0,
      outputTokens: 0,
      reasoningOutputTokens: 0,
      costUsd,
    };
  }
  if (event.type !== "assistant") return undefined;

  const message = asRecord(event.message);
  const usage = asRecord(message?.usage);
  if (usage === undefined) return undefined;

  const inputTokens = nonNegativeTokenCount(usage.input_tokens) ?? 0;
  const cachedInputTokens =
    (nonNegativeTokenCount(usage.cache_creation_input_tokens) ?? 0) +
    (nonNegativeTokenCount(usage.cache_read_input_tokens) ?? 0);
  const reasoningOutputTokens =
    nonNegativeTokenCount(asRecord(usage.output_tokens_details)?.thinking_tokens) ?? 0;
  const outputTokens = Math.max(
    0,
    (nonNegativeTokenCount(usage.output_tokens) ?? 0) - reasoningOutputTokens,
  );
  const totalTokens = inputTokens + cachedInputTokens + outputTokens + reasoningOutputTokens;
  if (totalTokens <= 0) return undefined;

  const messageId = message?.id;
  return {
    totalTokens,
    inputTokens,
    cachedInputTokens,
    outputTokens,
    reasoningOutputTokens,
    ...(typeof messageId === "string" && messageId.length > 0 ? { stepId: messageId } : {}),
  };
}

/**
 * Token frame from Codex's `exec --json` stream. Codex reports the turn's usage
 * on `turn.completed`, where `cached_input_tokens` and `reasoning_output_tokens`
 * are subsets of the input/output totals — the same convention the Codex
 * adapter's snapshots use — so both are subtracted out here.
 */
export function parseCodexStepUsage(raw: string): HarnessStepUsage | undefined {
  if (!raw.includes("turn.completed")) {
    return undefined;
  }
  const event = parseJsonRecord(raw);
  if (event?.type !== "turn.completed") return undefined;
  const usage = asRecord(event.usage);
  if (usage === undefined) return undefined;

  const cachedInputTokens = nonNegativeTokenCount(usage.cached_input_tokens) ?? 0;
  const reasoningOutputTokens = nonNegativeTokenCount(usage.reasoning_output_tokens) ?? 0;
  const inputTokens = Math.max(
    0,
    (nonNegativeTokenCount(usage.input_tokens) ?? 0) - cachedInputTokens,
  );
  const outputTokens = Math.max(
    0,
    (nonNegativeTokenCount(usage.output_tokens) ?? 0) - reasoningOutputTokens,
  );
  const totalTokens = inputTokens + cachedInputTokens + outputTokens + reasoningOutputTokens;
  if (totalTokens <= 0) return undefined;

  return { totalTokens, inputTokens, cachedInputTokens, outputTokens, reasoningOutputTokens };
}

type AgentUsageLogEntry = Extract<AgentLogEntry, { readonly kind: "usage" }>;

/**
 * One model step's usage, as persisted inline in the agent log so the
 * per-subagent stats view can rebuild tokens per turn. Cost rides along when
 * the harness priced the step. The adapter writes this only for steps its
 * rollup accepted, so a harness repeating a step's frames cannot double-count
 * the same tokens in the drill-in.
 */
export function harnessUsageLogEntry(usage: HarnessStepUsage): AgentUsageLogEntry {
  return {
    kind: "usage",
    totalTokens: usage.totalTokens,
    inputTokens: usage.inputTokens,
    cachedInputTokens: usage.cachedInputTokens,
    outputTokens: usage.outputTokens,
    reasoningOutputTokens: usage.reasoningOutputTokens,
    ...(usage.costUsd === undefined ? {} : { costUsd: usage.costUsd }),
  };
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
  return [];
}

function splitTextLines(text: string): ReadonlyArray<string> {
  return text.split("\n").filter((line) => line.length > 0);
}

/**
 * Display lines for the orchestrator chat message, derived from a harness's log
 * entries. Shared by every transport so a new harness only has to say how its
 * stream becomes entries.
 */
function displayEntries(
  entries: ReadonlyArray<AgentLogEntry>,
  options: { readonly includeReasoning?: boolean },
): ReadonlyArray<string> {
  const lines: string[] = [];
  for (const entry of entries) {
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

/** Display lines for the orchestrator chat message, derived from a log record. */
export function parseOpenCodeJsonLine(
  raw: string,
  options: { readonly includeReasoning?: boolean } = {},
): ReadonlyArray<string> {
  return displayEntries(parseOpenCodeLogLine(raw), options);
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
    // Usage rides on the same frame but is persisted by the adapter's usage
    // tap, so the log only carries the message's prose here.
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
  return displayEntries(parsePiLogLine(raw), options);
}

/**
 * Structured log entries for one raw Claude Code JSONL record. The stream
 * carries the assistant's own narration; tool calls and their results arrive in
 * separate frames (`tool_use` on the assistant record, `tool_result` on the
 * following user record), and this transport holds no cross-frame state, so it
 * logs prose and usage rather than a tool row that can never leave "running".
 * A failed run still reports its error.
 */
export function parseClaudeLogLine(raw: string): ReadonlyArray<AgentLogEntry> {
  const event = parseJsonRecord(raw);
  if (event === undefined) return [];

  if (event.type === "assistant") {
    const message = asRecord(event.message);
    const content = message?.content;
    if (!Array.isArray(content)) return [];
    const entries: AgentLogEntry[] = [];
    for (const candidate of content) {
      const part = asRecord(candidate);
      if (part === undefined) continue;
      if (part.type === "text" && typeof part.text === "string" && part.text.length > 0) {
        entries.push({ kind: "text", text: part.text });
      } else if (
        part.type === "thinking" &&
        typeof part.thinking === "string" &&
        part.thinking.length > 0
      ) {
        entries.push({ kind: "reasoning", text: part.thinking });
      }
    }
    // Usage and cost ride on the same frames but are persisted by the adapter's
    // usage tap, which folds each message once no matter how many content-block
    // events repeat it.
    return entries;
  }
  if (event.type === "result" && event.is_error === true) {
    const text =
      typeof event.result === "string" && event.result.length > 0
        ? event.result
        : "Claude Code reported an error.";
    return [{ kind: "error", text: truncate(text, TOOL_BODY_MAX) }];
  }
  return [];
}

/** Display lines for the orchestrator chat message, derived from a Claude record. */
export function parseClaudeJsonLine(
  raw: string,
  options: { readonly includeReasoning?: boolean } = {},
): ReadonlyArray<string> {
  return displayEntries(parseClaudeLogLine(raw), options);
}

/**
 * Structured log entries for one raw Codex `exec --json` record. Codex reports
 * items with stable ids and a status, so tool rows fold (start → completion)
 * the same way OpenCode's do. Item bodies that carry prose — the agent message,
 * its reasoning, and a failed command's output — reach the log; the rest of
 * Codex's event vocabulary is bookkeeping.
 */
export function parseCodexLogLine(raw: string): ReadonlyArray<AgentLogEntry> {
  const event = parseJsonRecord(raw);
  if (event === undefined) return [];

  if (event.type === "error" || event.type === "turn.failed") {
    const failure = asRecord(event.error)?.message ?? event.message;
    return typeof failure === "string" && failure.length > 0
      ? [{ kind: "error", text: truncate(failure, TOOL_BODY_MAX) }]
      : [];
  }

  const item = asRecord(event.item);
  if (item === undefined) return [];
  const callId = typeof item.id === "string" && item.id.length > 0 ? item.id : undefined;
  const running = item.status === "in_progress";

  if (item.type === "agent_message" || item.type === "reasoning") {
    if (typeof item.text !== "string" || item.text.length === 0) return [];
    return [{ kind: item.type === "agent_message" ? "text" : "reasoning", text: item.text }];
  }
  if (item.type === "command_execution") {
    if (typeof item.command !== "string" || item.command.length === 0) return [];
    const output = typeof item.aggregated_output === "string" ? item.aggregated_output : undefined;
    const exitCode = typeof item.exit_code === "number" ? item.exit_code : undefined;
    return [
      {
        kind: "tool",
        tool: "shell",
        title: truncateSummary(item.command),
        status: running
          ? "running"
          : exitCode === 0 || exitCode === undefined
            ? "completed"
            : "error",
        ...(callId === undefined ? {} : { callId }),
        input: truncate(item.command, TOOL_BODY_MAX),
        ...(output === undefined || output.length === 0
          ? {}
          : { output: truncate(output, TOOL_BODY_MAX) }),
      },
    ];
  }
  if (item.type === "file_change") {
    const changes = Array.isArray(item.changes) ? item.changes : [];
    const paths = changes
      .map((change) => asRecord(change)?.path)
      .filter((path): path is string => typeof path === "string");
    if (paths.length === 0) return [];
    const input = formatToolInput(changes);
    return [
      {
        kind: "tool",
        tool: "edit",
        title: truncateSummary(paths.join(", ")),
        status: running ? "running" : "completed",
        ...(callId === undefined ? {} : { callId }),
        ...(input === undefined ? {} : { input }),
      },
    ];
  }
  return [];
}

/** Display lines for the orchestrator chat message, derived from a Codex record. */
export function parseCodexJsonLine(
  raw: string,
  options: { readonly includeReasoning?: boolean } = {},
): ReadonlyArray<string> {
  return displayEntries(parseCodexLogLine(raw), options);
}

/**
 * Harness stream → log entries. A harness without a transport keeps its raw
 * line, which is everything this side can honestly say about an unknown CLI.
 */
export function parseHarnessLogLine(
  raw: string,
  kind: AgentTunnelHarnessKind,
): ReadonlyArray<AgentLogEntry> {
  switch (kind) {
    case "opencode":
      return parseOpenCodeLogLine(raw);
    case "pi":
      return parsePiLogLine(raw);
    case "claude":
      return parseClaudeLogLine(raw);
    case "codex":
      return parseCodexLogLine(raw);
    case "other":
      return [{ kind: "raw", text: raw }];
  }
}

/** Harness stream → display lines for the orchestrator's chat message. */
export function parseHarnessJsonLines(
  raw: string,
  kind: AgentTunnelHarnessTransportKind,
  options: { readonly includeReasoning?: boolean } = {},
): ReadonlyArray<string> {
  return displayEntries(parseHarnessLogLine(raw, kind), options);
}

/**
 * Harness stream → that step's usage, when the harness reports one. Every
 * transport normalizes to `HarnessStepUsage`, so the adapter's accumulation and
 * the thread snapshot stay identical whichever CLI a role runs.
 */
export function parseHarnessUsageStep(
  raw: string,
  kind: AgentTunnelHarnessTransportKind,
): HarnessStepUsage | undefined {
  switch (kind) {
    case "opencode":
      return parseOpenCodeStepUsage(raw);
    case "pi":
      return parsePiStepUsage(raw);
    case "claude":
      return parseClaudeStepUsage(raw);
    case "codex":
      return parseCodexStepUsage(raw);
  }
}
