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
    <div className="overflow-hidden rounded-md border border-info/25 bg-info/[0.04]">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-2 py-1.5 text-left hover:bg-info/[0.08]"
      >
        <Bot aria-hidden className="size-3.5 shrink-0 text-info-foreground" />
        <span className="min-w-0 flex-1 truncate text-[.8125rem]">
          <span className="font-medium text-info-foreground">Output</span>
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
        <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words border-t border-info/20 bg-background/50 p-2 font-mono text-[.68rem] leading-relaxed text-foreground/80">
          {text.length > 0 ? text : "No output."}
        </pre>
      ) : null}
    </div>
  );
}

/**
 * The orchestrator's narration is one markdown stream per workflow, so the
 * surface itself carries the workflow identity: a tinted panel with a colored
 * rail, on top of the element accents in `.agent-workflow` (index.css).
 */
const WORKFLOW_PANEL_CLASS = cn(
  "agent-workflow relative overflow-hidden rounded-xl border border-info/15 bg-info/[0.035] py-3 pe-3 ps-5",
  "before:absolute before:inset-y-3 before:start-2 before:w-[3px] before:rounded-full",
  "before:bg-linear-to-b before:from-sky-500/70 before:via-violet-500/60 before:to-amber-500/60",
);

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
      <div className={WORKFLOW_PANEL_CLASS}>
        <ChatMarkdown
          text={text}
          cwd={cwd}
          threadRef={threadRef}
          isStreaming={isStreaming}
          lineBreaks={lineBreaks}
          skills={skills}
        />
      </div>
    );
  }

  const lastIndex = blocks.length - 1;
  return (
    <div className={cn(WORKFLOW_PANEL_CLASS, "flex flex-col gap-2")}>
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
            return (
              <ThinkingBlock key={block.id} text={block.text} className="agent-workflow-thinking" />
            );
          case "output":
            return <OutputCard key={block.id} label={block.label} text={block.text} />;
        }
      })}
    </div>
  );
}
