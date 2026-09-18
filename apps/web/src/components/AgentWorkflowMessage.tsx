import type { ScopedThreadRef, ServerProviderSkill } from "@t3tools/contracts";
import { Bot, ChevronDown } from "lucide-react";
import { useMemo, useState } from "react";

import { cn } from "~/lib/utils";

import ChatMarkdown from "./ChatMarkdown";
import { parseAgentWorkflowMessage } from "./AgentWorkflowMessage.logic";
import { ThinkingBlock } from "./ThinkingBlock";

function OutputCard({ label, text }: { label: string; text: string }) {
  const [open, setOpen] = useState(false);
  const lineCount = text.length === 0 ? 0 : text.split("\n").length;
  return (
    <div className="overflow-hidden rounded-md border border-border/50 bg-card/30">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-2 py-1.5 text-left hover:bg-accent/30"
      >
        <Bot aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="min-w-0 flex-1 truncate text-[13px]">
          <span className="font-medium text-foreground/90">Output</span>
          <span className="text-muted-foreground"> · {label}</span>
        </span>
        <span className="shrink-0 font-mono text-[.65rem] text-muted-foreground/60">
          {lineCount} {lineCount === 1 ? "line" : "lines"}
        </span>
        <ChevronDown
          aria-hidden
          className={cn(
            "size-3 shrink-0 text-muted-foreground/50 transition-transform",
            open && "rotate-180",
          )}
        />
      </button>
      {open ? (
        <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words border-t border-border/50 bg-background/50 p-2 font-mono text-[.68rem] leading-relaxed text-muted-foreground/90">
          {text.length > 0 ? text : "No output."}
        </pre>
      ) : null}
    </div>
  );
}

/** Opencode-style rendering for an AgentTunnel orchestrator turn. */
export function AgentWorkflowMessage({
  text,
  cwd,
  threadRef,
  isStreaming,
  lineBreaks,
  skills,
}: {
  text: string;
  cwd: string | undefined;
  threadRef?: ScopedThreadRef | undefined;
  isStreaming: boolean;
  lineBreaks: boolean;
  skills: ReadonlyArray<Pick<ServerProviderSkill, "name" | "displayName">>;
}) {
  const blocks = useMemo(() => parseAgentWorkflowMessage(text), [text]);

  if (blocks.length === 1 && blocks[0]?.kind === "markdown") {
    return (
      <ChatMarkdown
        text={text}
        cwd={cwd}
        threadRef={threadRef}
        isStreaming={isStreaming}
        lineBreaks={lineBreaks}
        skills={skills}
      />
    );
  }

  const lastIndex = blocks.length - 1;
  return (
    <div className="flex flex-col gap-2">
      {blocks.map((block, index) => {
        const streaming = isStreaming && index === lastIndex;
        switch (block.kind) {
          case "markdown":
            return (
              <ChatMarkdown
                key={block.id}
                text={block.text}
                cwd={cwd}
                threadRef={threadRef}
                isStreaming={streaming}
                lineBreaks={lineBreaks}
                skills={skills}
              />
            );
          case "thinking":
            return <ThinkingBlock key={block.id} text={block.text} />;
          case "output":
            return <OutputCard key={block.id} label={block.label} text={block.text} />;
        }
      })}
    </div>
  );
}
