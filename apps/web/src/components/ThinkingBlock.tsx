import { ChevronDown, ChevronRight } from "lucide-react";
import { useState } from "react";

import { cn } from "~/lib/utils";

import ChatMarkdown from "./ChatMarkdown";
import { parseAgentOutputText } from "./AgentWorkflowMessage.logic";
import { ToolCallRow } from "./ToolCallRow";

/**
 * Muted, indented aside for model reasoning — opencode's thinking part. The
 * body carries the same `→ tool: detail` / `✗ error` display lines as subagent
 * output, so it parses and renders with the same rows.
 */
export function ThinkingBlock({ text, className }: { text: string; className?: string }) {
  const [open, setOpen] = useState(true);
  const segments = parseAgentOutputText(text, undefined);
  return (
    <div className={cn("ms-2 border-s border-border/60 ps-3", className)}>
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        aria-expanded={open}
        className="flex w-full items-center gap-1 text-left text-[.65rem] font-medium uppercase tracking-wider text-muted-foreground/70 hover:text-muted-foreground"
      >
        {open ? (
          <ChevronDown aria-hidden className="size-3 shrink-0" />
        ) : (
          <ChevronRight aria-hidden className="size-3 shrink-0" />
        )}
        Thinking
      </button>
      {open ? (
        <div className="flex flex-col gap-1 pt-1 opacity-75">
          {segments.map((segment) =>
            segment.kind === "tool" ? (
              <ToolCallRow
                key={segment.id}
                tool={segment.tool}
                detail={segment.displayDetail}
                fullDetail={segment.detail}
              />
            ) : segment.kind === "error" ? (
              <p
                key={segment.id}
                className="whitespace-pre-wrap break-words font-mono text-[.68rem] leading-relaxed text-destructive-foreground/90"
              >
                {segment.text}
              </p>
            ) : (
              <ChatMarkdown key={segment.id} text={segment.text} cwd={undefined} lineBreaks />
            ),
          )}
        </div>
      ) : null}
    </div>
  );
}
