import { useMemo } from "react";
import { agentName, agentPhase, askingAgents, resolveDeps } from "../lib/agentTree";
import type { AgentQuestion } from "../lib/questions";
import { PhaseGlyph } from "./AgentStatus";

/** The fields the board reads from a thread agent. */
export type BoardAgent = {
  id: string;
  name?: string;
  role: string;
  task: string;
  status: string;
  dependsOn?: string[];
};

export type BoardColumnId = "todo" | "doing" | "done" | "failed";

/** Columns in workflow order. Every agent status lands in exactly one column. */
export const BOARD_COLUMNS: { id: BoardColumnId; title: string }[] = [
  { id: "todo", title: "To do" },
  { id: "doing", title: "In progress" },
  { id: "done", title: "Done" },
  { id: "failed", title: "Failed" },
];

/** Maps an agent status to its board column. Unknown statuses count as to-do. */
export function boardColumn(status: string): BoardColumnId {
  switch (status) {
    case "running":
      return "doing";
    case "succeeded":
      return "done";
    case "failed":
    case "cancelled":
      return "failed";
    default:
      return "todo";
  }
}

/**
 * Groups task agents into board columns. Team leads are excluded: the board
 * shows the work the leads dispatched, not the leads themselves.
 */
export function groupTasks<T extends BoardAgent>(agents: T[]): Record<BoardColumnId, T[]> {
  const out: Record<BoardColumnId, T[]> = { todo: [], doing: [], done: [], failed: [] };
  for (const a of agents) {
    if (a.role === "orchestrator") continue;
    out[boardColumn(a.status)].push(a);
  }
  return out;
}

/**
 * The dispatched work as a board. A to-do card names what it waits on and
 * whether that is blocking it (✓ for a dependency that already succeeded), so
 * a dependency chain reads at a glance.
 */
export function KanbanBoard({
  agents,
  roleNames,
  questions,
  onOpenAgent,
}: {
  agents: BoardAgent[];
  roleNames: Record<string, string>;
  questions?: AgentQuestion[] | null;
  onOpenAgent: (id: string) => void;
}) {
  const columns = useMemo(() => groupTasks(agents), [agents]);
  const asking = useMemo(() => askingAgents(questions), [questions]);

  return (
    <div className="board" role="region" aria-label="Task board">
      {BOARD_COLUMNS.map((col) => {
        const cards = columns[col.id];
        return (
          <section key={col.id} className="board-col" aria-label={col.title}>
            <header className="board-col-head">
              <span>{col.title}</span>
              <span className="pill">{cards.length}</span>
            </header>
            <ul className="board-cards">
              {cards.map((a) => {
                const phase = agentPhase(a, agents, asking);
                const deps = resolveDeps(a, agents);
                return (
                  <li key={a.id}>
                    <button
                      type="button"
                      className={`board-card${phase === "blocked" ? " blocked" : ""}`}
                      onClick={() => onOpenAgent(a.id)}
                    >
                      <span className="board-card-top">
                        <PhaseGlyph phase={phase} />
                        <span className="board-card-name">{a.name || a.id}</span>
                        <span className="board-card-role">{roleNames[a.role] || a.role}</span>
                      </span>
                      <span className="board-card-task">{a.task}</span>
                      {deps.length > 0 && (
                        <span className="board-card-deps">
                          {phase === "blocked" ? "blocked by " : "after "}
                          {deps.map((d, i) => (
                            <span key={d.id} className={d.status === "succeeded" ? "dep-ok" : "dep-wait"}>
                              {i > 0 && ", "}
                              {agentName(d, roleNames)}
                              {d.status === "succeeded" ? " ✓" : ""}
                            </span>
                          ))}
                        </span>
                      )}
                    </button>
                  </li>
                );
              })}
            </ul>
          </section>
        );
      })}
    </div>
  );
}
