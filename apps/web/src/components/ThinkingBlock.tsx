import { ChevronDown, ChevronRight, Terminal } from "lucide-react";
import { useState } from "react";

import { cn } from "~/lib/utils";

import ChatMarkdown from "./ChatMarkdown";
import { splitThinkingText } from "./ThinkingBlock.logic";

/** Muted, indented aside for model reasoning — opencode's thinking part. */
export function ThinkingBlock({ text, className }: { text: string; className?: string }) {
  const [open, setOpen] = useState(true);
  const segments = splitThinkingText(text);
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
              <div
                key={segment.id}
                className="flex items-start gap-1.5 font-mono text-[.68rem] leading-relaxed text-muted-foreground/80"
              >
                <Terminal aria-hidden className="mt-0.5 size-3 shrink-0 text-muted-foreground/60" />
                <span className="min-w-0 whitespace-pre-wrap break-words">{segment.text}</span>
              </div>
            ) : (
              <ChatMarkdown key={segment.id} text={segment.text} cwd={undefined} lineBreaks />
            ),
          )}
        </div>
      ) : null}
    </div>
  );
}
