import { formatWorkspaceRelativePath } from "~/filePathDisplay";

export type AgentWorkflowBlock =
  | { readonly id: string; readonly kind: "markdown"; readonly text: string }
  | {
      readonly id: string;
      readonly kind: "thinking";
      readonly text: string;
      readonly streaming: boolean;
    }
  | {
      readonly id: string;
      readonly kind: "output";
      readonly label: string;
      readonly text: string;
      readonly streaming: boolean;
    };

const DETAILS_OPEN = "<details>";
const DETAILS_CLOSE = "</details>";
const SUMMARY_OPEN = "<summary>";
const SUMMARY_CLOSE = "</summary>";
const THINKING_SUMMARY = "Orchestrator thinking";
const OUTPUT_SUMMARY_PREFIX = "📋";

function stripCodeFence(text: string): string {
  const trimmed = text.trim();
  const match = /^```[^\n]*\n([\s\S]*?)\n?```$/.exec(trimmed);
  return match?.[1] ?? trimmed;
}

function cleanBody(text: string): string {
  return text.replace(/^\n+/, "").replace(/\n+$/, "");
}

/**
 * Splits the orchestrator's assistant message into opencode-style blocks:
 * prose stays markdown, the thinking details become muted thinking blocks, and
 * per-agent output details become collapsible output cards. Malformed or
 * unknown details fall back to their original text so nothing is dropped.
 *
 * The emitter is `Orchestrator.formatWorkflowFinishMarkdown` and friends; this
 * parser is the matching reader and must stay in sync with that shape.
 */
export function parseAgentWorkflowMessage(text: string): ReadonlyArray<AgentWorkflowBlock> {
  const blocks: AgentWorkflowBlock[] = [];
  let cursor = 0;

  const pushMarkdown = (value: string) => {
    const trimmed = value.replace(/^\n+/, "").replace(/\n+$/, "");
    if (trimmed.trim().length === 0) return;
    blocks.push({ id: `markdown-${blocks.length}`, kind: "markdown", text: trimmed });
  };

  while (cursor < text.length) {
    const detailsIndex = text.indexOf(DETAILS_OPEN, cursor);
    if (detailsIndex === -1) {
      pushMarkdown(text.slice(cursor));
      break;
    }
    const summaryOpenIndex = text.indexOf(SUMMARY_OPEN, detailsIndex);
    const summaryCloseIndex = text.indexOf(SUMMARY_CLOSE, detailsIndex);
    if (
      summaryOpenIndex === -1 ||
      summaryCloseIndex === -1 ||
      summaryOpenIndex > summaryCloseIndex
    ) {
      pushMarkdown(text.slice(cursor));
      break;
    }

    const summary = text.slice(summaryOpenIndex + SUMMARY_OPEN.length, summaryCloseIndex).trim();
    const bodyStart = summaryCloseIndex + SUMMARY_CLOSE.length;
    const closeIndex = text.indexOf(DETAILS_CLOSE, bodyStart);
    const streaming = closeIndex === -1;
    const body = cleanBody(text.slice(bodyStart, streaming ? text.length : closeIndex));

    pushMarkdown(text.slice(cursor, detailsIndex));

    if (summary.includes(THINKING_SUMMARY)) {
      blocks.push({
        id: `thinking-${blocks.length}`,
        kind: "thinking",
        text: body,
        streaming,
      });
    } else if (summary.startsWith(OUTPUT_SUMMARY_PREFIX)) {
      const label = summary.replace(/^📋\s*Output\s*·?\s*/, "").trim();
      blocks.push({
        id: `output-${blocks.length}`,
        kind: "output",
        label: label.length > 0 ? label : "Output",
        text: stripCodeFence(body),
        streaming,
      });
    } else {
      pushMarkdown(
        `${summary.length > 0 ? `${summary}\n\n` : ""}${body}${
          streaming ? `\n\n${DETAILS_CLOSE}` : ""
        }`,
      );
    }

    cursor = streaming ? text.length : closeIndex + DETAILS_CLOSE.length;
  }

  return blocks;
}

export type AgentOutputToolSegment = {
  readonly id: string;
  readonly kind: "tool";
  readonly tool: string;
  readonly detail: string;
  /** `detail` with a workspace-absolute path shortened against the card's cwd. */
  readonly displayDetail: string;
};

export type AgentOutputSegment =
  | { readonly id: string; readonly kind: "prose"; readonly text: string }
  | AgentOutputToolSegment
  | { readonly id: string; readonly kind: "error"; readonly text: string };

export type AgentOutputToolRun = {
  readonly id: string;
  readonly kind: "toolRun";
  readonly tools: ReadonlyArray<AgentOutputToolSegment>;
  readonly summary: string;
};

export type AgentOutputGroup =
  | Extract<AgentOutputSegment, { kind: "prose" }>
  | AgentOutputToolSegment
  | Extract<AgentOutputSegment, { kind: "error" }>
  | AgentOutputToolRun;

const TOOL_LINE_PATTERN = /^→\s*(.*)$/;
const ERROR_LINE_PATTERN = /^✗\s*(.*)$/;

/** Consecutive tool calls at or above this count collapse into one expander row. */
const TOOL_RUN_COLLAPSE_THRESHOLD = 3;

/** Tools whose detail names a file, counted in a collapsed run's summary. */
const FILE_TOOLS: Record<string, true> = { read: true, write: true, edit: true, patch: true };

/** Splits one `→ tool: detail` display line; a missing detail or an unknown tool is kept. */
function parseToolLine(line: string): { tool: string; detail: string } | undefined {
  const match = TOOL_LINE_PATTERN.exec(line.trimStart());
  if (match === null) return undefined;
  const body = (match[1] ?? "").trim();
  const separator = body.indexOf(":");
  const tool = (separator === -1 ? body : body.slice(0, separator)).trim();
  if (tool.length === 0) return undefined;
  return { tool, detail: separator === -1 ? "" : body.slice(separator + 1).trim() };
}

/**
 * Display form of a tool detail: a bare workspace path shortens against `cwd`
 * (`/repo/src/a.ts` → `repo/src/a.ts`). Shell commands and search patterns are
 * left alone, and the raw detail stays available for the row's tooltip.
 */
function shortenDetail(detail: string, cwd: string | undefined): string {
  if (cwd === undefined || /\s/.test(detail) || !detail.startsWith("/")) return detail;
  return formatWorkspaceRelativePath(detail, cwd);
}

/**
 * Reads a subagent output or thinking body — the lines `parseOpenCodeJsonLine`
 * emits for the orchestrator, i.e. prose interleaved with `→ tool: detail` and
 * `✗ error` display lines — into ordered render segments. Lines the grammar does
 * not recognise stay prose, so nothing is lost or reordered.
 */
export function parseAgentOutputText(
  text: string,
  cwd: string | undefined,
): ReadonlyArray<AgentOutputSegment> {
  const segments: AgentOutputSegment[] = [];
  let prose: string[] = [];

  const flushProse = () => {
    while (prose.length > 0 && (prose[0] ?? "").trim().length === 0) prose.shift();
    while (prose.length > 0 && (prose[prose.length - 1] ?? "").trim().length === 0) prose.pop();
    const value = prose.join("\n");
    prose = [];
    if (value.trim().length > 0) {
      segments.push({ id: `prose-${segments.length}`, kind: "prose", text: value });
    }
  };

  for (const rawLine of stripCodeFence(text).split("\n")) {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    const tool = parseToolLine(line);
    if (tool !== undefined) {
      flushProse();
      segments.push({
        id: `tool-${segments.length}`,
        kind: "tool",
        tool: tool.tool,
        detail: tool.detail,
        displayDetail: shortenDetail(tool.detail, cwd),
      });
      continue;
    }
    const error = ERROR_LINE_PATTERN.exec(line.trimStart())?.[1];
    if (error !== undefined && error.trim().length > 0) {
      flushProse();
      segments.push({ id: `error-${segments.length}`, kind: "error", text: `✗ ${error.trim()}` });
      continue;
    }
    prose.push(line);
  }

  flushProse();
  return segments;
}

/** `12 tool calls · 4 files read` — the label on a collapsed run of tool rows. */
export function summarizeToolRun(tools: ReadonlyArray<AgentOutputToolSegment>): string {
  const files = new Set<string>();
  let readsOnly = true;
  for (const tool of tools) {
    const name = tool.tool.toLowerCase();
    if (FILE_TOOLS[name] !== true || tool.detail.length === 0) continue;
    files.add(tool.detail);
    if (name !== "read") readsOnly = false;
  }
  const summary = `${tools.length} tool calls`;
  if (files.size === 0) return summary;
  const fileLabel = files.size === 1 ? "file" : "files";
  return `${summary} · ${files.size} ${fileLabel} ${readsOnly ? "read" : "touched"}`;
}

/** Groups consecutive tool segments so the card can collapse long runs. */
export function groupAgentOutputSegments(
  segments: ReadonlyArray<AgentOutputSegment>,
): ReadonlyArray<AgentOutputGroup> {
  const groups: AgentOutputGroup[] = [];
  let run: AgentOutputToolSegment[] = [];

  const flushRun = () => {
    if (run.length === 0) return;
    const tools = run;
    run = [];
    if (tools.length < TOOL_RUN_COLLAPSE_THRESHOLD) {
      groups.push(...tools);
      return;
    }
    groups.push({
      id: `tool-run-${groups.length}`,
      kind: "toolRun",
      tools,
      summary: summarizeToolRun(tools),
    });
  };

  for (const segment of segments) {
    if (segment.kind === "tool") {
      run.push(segment);
      continue;
    }
    flushRun();
    groups.push(segment);
  }
  flushRun();
  return groups;
}

/** Counts for the output card header; zero counts are omitted at render time. */
export function summarizeAgentOutput(segments: ReadonlyArray<AgentOutputSegment>): {
  readonly lines: number;
  readonly toolCalls: number;
  readonly errors: number;
} {
  let lines = 0;
  let toolCalls = 0;
  let errors = 0;
  for (const segment of segments) {
    switch (segment.kind) {
      case "prose":
        lines += segment.text.split("\n").filter((line) => line.trim().length > 0).length;
        break;
      case "tool":
        lines += 1;
        toolCalls += 1;
        break;
      case "error":
        lines += 1;
        errors += 1;
        break;
    }
  }
  return { lines, toolCalls, errors };
}
