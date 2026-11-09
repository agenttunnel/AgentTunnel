/** One task on the thread's agreed list, as the daemon's thread.get returns it. */
export type TaskItem = {
  name: string;
  title: string;
  status: string;
};

const STATUS_LABELS: Record<string, string> = {
  todo: "To do",
  doing: "In progress",
  done: "Done",
};

/**
 * The leads' agreed plan: an ordered checklist above the agent board. It is
 * deliberately not card-shaped, so a planned task never reads as a running agent.
 */
export function TaskList({ tasks }: { tasks: TaskItem[] }) {
  const done = tasks.filter((t) => t.status === "done").length;
  return (
    <section className="task-list" aria-label="Task list">
      <header className="task-list-head">
        <span>Plan</span>
        <span className="pill">
          {done}/{tasks.length} done
        </span>
      </header>
      <ol className="task-rows">
        {tasks.map((t) => (
          <li key={t.name} className={`task-row ${t.status}`}>
            <span className="task-glyph" title={STATUS_LABELS[t.status] ?? t.status} aria-hidden="true" />
            <span className="task-title">{t.title}</span>
            <span className="task-name">{t.name}</span>
          </li>
        ))}
      </ol>
    </section>
  );
}
