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
