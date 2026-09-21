import {
  Bot,
  Eye,
  FilePen,
  Globe,
  ListChecks,
  Search,
  Terminal,
  Wrench,
  type LucideIcon,
} from "lucide-react";

import { Tooltip, TooltipPopup, TooltipTrigger } from "./ui/tooltip";

/** Icon for a tool named on an orchestrator display line; unknown tools get the wrench. */
export function toolIcon(tool: string): LucideIcon {
  switch (tool.toLowerCase()) {
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

/**
 * One `tool · detail` line for the display lines the orchestrator emits, shared
 * by subagent output cards and the orchestrator thinking aside so both read the
 * same. Long detail truncates; the tooltip carries the untruncated string.
 */
export function ToolCallRow({
  tool,
  detail,
  fullDetail,
}: {
  tool: string;
  detail: string;
  fullDetail?: string | undefined;
}) {
  const Icon = toolIcon(tool);
  return (
    <div className="flex min-w-0 items-center gap-1.5 text-[.7rem] leading-relaxed">
      <Icon aria-hidden className="size-3 shrink-0 text-muted-foreground/60" />
      <span className="shrink-0 font-medium text-foreground/80">{tool}</span>
      {detail.length > 0 ? (
        <Tooltip>
          <TooltipTrigger
            render={<span className="min-w-0 flex-1 truncate font-mono text-muted-foreground/80" />}
          >
            {detail}
          </TooltipTrigger>
          <TooltipPopup side="top" className="max-w-sm break-all font-mono">
            {fullDetail ?? detail}
          </TooltipPopup>
        </Tooltip>
      ) : null}
    </div>
  );
}
