import type { OrchestrationAgentLogEntry } from "@t3tools/contracts";
import {
  Bot,
  Check,
  ChevronDown,
  Eye,
  FilePen,
  Globe,
  ListChecks,
  Search,
  Terminal,
  Wrench,
  X,
  type LucideIcon,
} from "lucide-react";
import { useState } from "react";

import { cn } from "~/lib/utils";

import ChatMarkdown from "./ChatMarkdown";
import { ThinkingBlock } from "./ThinkingBlock";
import { foldAgentLogEntries, type AgentLogBlock } from "./AgentLogView.logic";

function toolIcon(tool: string): LucideIcon {
  switch (tool) {
    case "bash":
    case "shell":
      return Terminal;
    case "read":
      return Eye;
    case "edit":
    case "write":
    case "patch":
      return FilePen;
    case "grep":
    case "glob":
    case "list":
      return Search;
    case "webfetch":
    case "websearch":
      return Globe;
    case "task":
      return Bot;
    case "todowrite":
      return ListChecks;
    default:
      return Wrench;
  }
}

function MarkdownBlock({ text }: { text: string }) {
  return (
    <div className="text-xs leading-relaxed text-foreground/90">
      <ChatMarkdown text={text} cwd={undefined} />
    </div>
  );
}

function ToolCallBlock({
  entry,
}: {
  entry: Extract<OrchestrationAgentLogEntry, { kind: "tool" }>;
}) {
  const [open, setOpen] = useState(false);
  const failed = entry.status === "error";
  const running = entry.status === "running" || entry.status === "pending";
  const Icon = toolIcon(entry.tool);
  return (
    <div
      className={cn(
        "overflow-hidden rounded-md border",
        failed ? "border-destructive/40" : "border-border/50 bg-card/30",
      )}
    >
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-2 py-1.5 text-left hover:bg-accent/30"
      >
        <span
          className={cn(
            "flex size-4 shrink-0 items-center justify-center",
            failed
              ? "text-destructive-foreground"
              : running
                ? "text-info-foreground"
                : "text-success",
          )}
        >
          {failed ? (
            <X aria-hidden className="size-3.5" />
          ) : running ? (
            <span aria-hidden className="size-2 rounded-full bg-info" />
          ) : (
            <Check aria-hidden className="size-3.5" />
          )}
        </span>
        <Icon aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="min-w-0 flex-1 truncate text-[.8125rem]">
          <span className="font-medium text-foreground/90">{entry.tool}</span>
          {entry.title.length > 0 ? (
            <span className="text-muted-foreground"> · {entry.title}</span>
          ) : null}
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
        <div className="flex flex-col gap-1.5 border-t border-border/50 px-2 py-1.5">
          {entry.input !== undefined ? (
            <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-background/50 p-1.5 font-mono text-[.68rem] leading-relaxed text-muted-foreground/90">
              {entry.input}
            </pre>
          ) : null}
          {entry.error !== undefined ? (
            <p className="whitespace-pre-wrap break-words font-mono text-[.7rem] leading-relaxed text-destructive-foreground">
              {entry.error}
            </p>
          ) : null}
          {entry.output !== undefined ? (
            <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded bg-background/50 p-1.5 font-mono text-[.68rem] leading-relaxed text-muted-foreground/90">
              {entry.output}
            </pre>
          ) : null}
          {entry.input === undefined && entry.error === undefined && entry.output === undefined ? (
            <p className="text-xs text-muted-foreground">No details recorded.</p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function LogBlockView({ block }: { block: AgentLogBlock }) {
  switch (block.kind) {
    case "markdown":
      return <MarkdownBlock text={block.text} />;
    case "reasoning":
      return <ThinkingBlock text={block.text} />;
    case "tool":
      return <ToolCallBlock entry={block} />;
    case "raw":
      return (
        <pre className="whitespace-pre-wrap break-words font-mono text-[.7rem] leading-relaxed text-muted-foreground/70">
          {block.text}
        </pre>
      );
    case "error":
      return (
        <div className="flex items-start gap-2 rounded-md border border-destructive/40 px-2 py-1.5">
          <X aria-hidden className="mt-0.5 size-3.5 shrink-0 text-destructive-foreground" />
          <p className="min-w-0 whitespace-pre-wrap break-words font-mono text-[.7rem] leading-relaxed text-destructive-foreground">
            {block.text}
          </p>
        </div>
      );
  }
}

export function AgentLogView({ entries }: { entries: ReadonlyArray<OrchestrationAgentLogEntry> }) {
  const blocks = foldAgentLogEntries(entries);
  if (blocks.length === 0) {
    return <div className="text-xs text-muted-foreground">No activity recorded yet.</div>;
  }
  return (
    <div className="flex flex-col gap-2">
      {blocks.map((block) => (
        <LogBlockView key={block.id} block={block} />
      ))}
    </div>
  );
}
