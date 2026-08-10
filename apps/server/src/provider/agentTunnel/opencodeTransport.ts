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

export function buildAgentTunnelSpawnArgs(spec: AgentTunnelProcessSpec): string[] {
  const args = ["run"];
  if (spec.model) args.push("-m", spec.model);
  if (spec.dangerouslySkipPermissions) {
    args.push(isOpenCodeHarness(spec.harness) ? "--auto" : "--dangerously-skip-permissions");
  }
  if (isOpenCodeHarness(spec.harness)) args.push("--format", "json");
  args.push(spec.prompt);
  return args;
}

const TOOL_SUMMARY_MAX = 100;

function truncateSummary(value: string): string {
  return value.length <= TOOL_SUMMARY_MAX ? value : `${value.slice(0, TOOL_SUMMARY_MAX - 1)}…`;
}

function summarizeToolInput(input: unknown): string {
  if (!input || typeof input !== "object") return "";
  const record = input as Record<string, unknown>;
  const primary = record.command ?? record.path ?? record.file ?? record.pattern ?? record.query;
  if (typeof primary === "string" && primary.length > 0) return truncateSummary(primary);
  return truncateSummary(JSON.stringify(record));
}

interface OpenCodeToolUseState {
  readonly title?: unknown;
  readonly input?: unknown;
  readonly status?: unknown;
}

interface OpenCodeToolUsePart {
  readonly tool?: unknown;
  readonly state?: OpenCodeToolUseState;
}

interface OpenCodePart {
  readonly text?: unknown;
}

interface OpenCodeEvent {
  readonly type?: unknown;
  readonly part?: unknown;
}

function splitTextLines(text: string): ReadonlyArray<string> {
  return text.split("\n").filter((line) => line.length > 0);
}

export function parseOpenCodeJsonLine(raw: string): ReadonlyArray<string> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return [raw];
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return [raw];

  const event = parsed as OpenCodeEvent;
  if (event.type === "text") {
    const text = (event.part as OpenCodePart | undefined)?.text;
    return typeof text === "string" ? splitTextLines(text) : [];
  }
  if (event.type === "tool_use") {
    const part = event.part as OpenCodeToolUsePart | undefined;
    if (!part) return [];
    const tool = typeof part.tool === "string" && part.tool.length > 0 ? part.tool : "tool";
    const state = part.state;
    const title =
      typeof state?.title === "string" && state.title.length > 0
        ? state.title
        : summarizeToolInput(state?.input);
    return typeof title === "string" && title.length > 0
      ? [`→ ${tool}: ${truncateSummary(title)}`]
      : [`→ ${tool}`];
  }
  return [];
}
