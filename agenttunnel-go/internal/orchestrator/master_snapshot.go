package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// Byte and count budgets that keep the master prompt bounded however many
// projects and threads exist. Detail goes to the most recent and running
// threads; the rest appear as one compact line each, until the section budget
// is spent.
const (
	masterThreadsPerProject = 15
	masterThreadTitleClip   = 60
	masterThreadsBudget     = 24 << 10
	masterDetailThreads     = 8
	masterDetailBytes       = 1536
	masterDetailAgents      = 6
	masterDetailMessages    = 4
	masterMessageClip       = 240
	masterReadThreads       = 3
	masterReadBytes         = 4 << 10
	masterReadMessageClip   = 1000
)

// prompt snapshots the projects, the threads in each, and the enabled runtimes,
// so the master can answer from what actually exists and pick a valid model.
// reads holds the content of threads the model asked to read in earlier rounds.
func (m *Master) prompt(ctx context.Context, text string, history []MasterMessage, reads []string) string {
	var b strings.Builder
	b.WriteString("You are the AgentTunnel orchestrator on the home screen. You talk with the user about their projects and threads, and when they ask for work you propose actions that the app runs. Answer from the snapshot below; do not invent projects, threads, agents, harnesses, or models.\n\n")

	b.WriteString("## Projects\n")
	projects := m.projects()
	if len(projects) == 0 {
		b.WriteString("(none configured)\n")
	} else {
		for _, p := range projects {
			fmt.Fprintf(&b, "- %s | %s | %s\n", p.ID, p.Name, p.Path)
		}
	}

	b.WriteString("\n## Threads\n")
	b.WriteString(m.threadsSection(ctx, projects))

	b.WriteString("\n## Harnesses and models\n")
	for _, line := range m.harnessLines() {
		b.WriteString(line)
		b.WriteString("\n")
	}

	if len(reads) > 0 {
		b.WriteString("\n## Requested thread content\n")
		for _, r := range reads {
			b.WriteString(r)
		}
	}

	b.WriteString("\n## Conversation so far\n")
	b.WriteString(masterHistory(history))

	b.WriteString("\n## User message\n")
	b.WriteString(text)
	b.WriteString("\n\n## Reply\n")
	b.WriteString("Answer in plain text when the user is asking about their projects or threads. To act, say what you are doing in one short sentence, then include exactly one fenced JSON block of the form:\n")
	b.WriteString("```json\n")
	b.WriteString(`{"actions":[{"type":"start_thread","projectId":"<id from the project list>","title":"<short title>","prompt":"<first message for the thread>","harness":"<optional harness>","model":"<optional model>","variant":"<optional variant>"}]}`)
	b.WriteString("\n```\n")
	b.WriteString("Use at most 5 actions. The action types are:\n")
	b.WriteString("- start_thread: projectId, title, prompt, optional harness, model, variant. Starts a new thread with its first turn.\n")
	b.WriteString("- send_message: threadId, prompt. Sends a message to a thread. If the thread is already running a turn, the message steers that turn.\n")
	b.WriteString("- stop_thread: threadId. Cancels the thread's running turn.\n")
	b.WriteString("- rename_thread: threadId, title.\n")
	b.WriteString("- set_peer_mailbox: threadId, enabled (true or false). Lets the thread's agents message each other.\n")
	b.WriteString("- delete_thread: threadId, confirm (true). Deletes the thread record; its worktree is always kept. Include it only when the user's latest message explicitly asks to delete that thread. Otherwise ask first and propose nothing.\n")
	b.WriteString("- read_thread: threadId. Asks for that thread's recent messages, which you will be given before you answer again. Send it alone in its block, and at most 2 times per message.\n")
	b.WriteString("Use only threadId and projectId values from the lists above. Omit harness and model to use the default. If the user has not said which project, ask instead of guessing. Do not stop, rename, or delete a thread the user did not ask about.\n")
	return b.String()
}

// threadsSection lists each project's threads, detail first for the most
// recent and running ones, within masterThreadsBudget.
func (m *Master) threadsSection(ctx context.Context, projects []settings.Project) string {
	byProject := m.threads(ctx)
	var b strings.Builder
	omitted := 0
	for _, p := range projects {
		list := byProject[p.ID]
		if len(list) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s (%s)\n", p.Name, p.ID)
		for _, t := range list {
			if b.Len() > masterThreadsBudget {
				omitted++
				continue
			}
			b.WriteString(t.line())
			b.WriteString("\n")
			b.WriteString(t.detail)
		}
	}
	if b.Len() == 0 {
		return "(none)\n" + omittedNote(omitted)
	}
	return b.String() + omittedNote(omitted)
}

func omittedNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("(%d more threads omitted to keep the snapshot small)\n", n)
}

// masterThread is one thread row in the snapshot. detail is set only for the
// threads that get a detail block; it is indented and ends in a newline.
type masterThread struct {
	ID        contracts.ThreadID
	ProjectID string
	Title     string
	Status    string
	Harness   string
	Model     string
	UpdatedAt time.Time
	detail    string
	state     *domain.ThreadState
}

// line is the compact row every thread gets.
func (t masterThread) line() string {
	s := fmt.Sprintf("- %s | %s | %s", t.ID, clipTitle(t.Title, masterThreadTitleClip), t.Status)
	if t.Harness != "" || t.Model != "" {
		s += fmt.Sprintf(" | %s/%s", t.Harness, t.Model)
	}
	return s
}

// threads groups every thread under its project. Running threads come first,
// then the most recently updated; the first masterDetailThreads get a detail
// block, and each project keeps its newest masterThreadsPerProject rows.
func (m *Master) threads(ctx context.Context) map[string][]masterThread {
	out := map[string][]masterThread{}
	if m.engine == nil {
		return out
	}
	ids, err := m.engine.Threads(ctx)
	if err != nil {
		return out
	}
	var all []masterThread
	for _, id := range ids {
		st, err := m.engine.State(ctx, id)
		if err != nil || st == nil || !st.Exists || st.ProjectID == "" {
			continue
		}
		harness, model := masterThreadRuntime(st)
		all = append(all, masterThread{
			ID: id, ProjectID: st.ProjectID, Title: st.Title, Status: masterThreadStatus(st),
			Harness: harness, Model: model, UpdatedAt: st.UpdatedAt, state: st,
		})
	}
	sort.SliceStable(all, func(i, j int) bool {
		ri, rj := all[i].Status == "running", all[j].Status == "running"
		if ri != rj {
			return ri
		}
		return newerThread(all[i], all[j])
	})
	for i := range all {
		if i < masterDetailThreads {
			all[i].detail = masterThreadDetail(all[i].state)
		}
		all[i].state = nil
		out[all[i].ProjectID] = append(out[all[i].ProjectID], all[i])
	}
	for pid, list := range out {
		sort.SliceStable(list, func(i, j int) bool { return newerThread(list[i], list[j]) })
		if len(list) > masterThreadsPerProject {
			list = list[:masterThreadsPerProject]
		}
		out[pid] = list
	}
	return out
}

func newerThread(a, b masterThread) bool {
	if a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.ID > b.ID
	}
	return a.UpdatedAt.After(b.UpdatedAt)
}

// masterThreadDetail is the bounded block for one thread: where it runs, its
// agents, what waits on the user, its spend, and its last messages. Every part is
// clipped, and the whole block is capped at masterDetailBytes.
func masterThreadDetail(st *domain.ThreadState) string {
	var lines []string
	if st.Worktree != nil {
		lines = append(lines, fmt.Sprintf("worktree: branch %s at %s", st.Worktree.Branch, st.Worktree.Path))
	} else {
		lines = append(lines, "worktree: none (project checkout)")
	}

	var agents []string
	for i, id := range st.AgentOrder {
		if i == masterDetailAgents {
			break
		}
		a := st.Agents[id]
		name := a.Name
		if name == "" {
			name = string(id)
		}
		s := fmt.Sprintf("%s (%s) %s", name, a.Role, a.Status)
		if a.Status == contracts.AgentFailed && a.Reason != "" {
			s += ": " + clipTitle(oneLine(a.Reason), 120)
		}
		agents = append(agents, s)
	}
	if len(agents) > 0 {
		lines = append(lines, "agents: "+strings.Join(agents, "; "))
	}

	var waiting []string
	for i := range st.Questions {
		q := st.Questions[i]
		if q.Status == domain.QuestionPending {
			waiting = append(waiting, fmt.Sprintf("question %s: %s", q.QuestionID, clipTitle(oneLine(q.Question), 160)))
		}
	}
	openReviews := 0
	for i := range st.ReviewThreads {
		if st.ReviewThreads[i].Status == contracts.ReviewStatusOpen {
			openReviews++
		}
	}
	if openReviews > 0 {
		waiting = append(waiting, fmt.Sprintf("%d open review comments", openReviews))
	}
	if len(waiting) > 0 {
		lines = append(lines, "waiting on user: "+strings.Join(waiting, "; "))
	}

	if total := st.ThreadUsage().Total(); total.CostUSD > 0 || total.InputTokens > 0 || total.OutputTokens > 0 {
		lines = append(lines, fmt.Sprintf("spend: $%.4f (%d in, %d out tokens)", total.CostUSD, total.InputTokens, total.OutputTokens))
	}

	if n := len(st.Messages); n > 0 {
		lines = append(lines, "recent:")
		from := max(0, n-masterDetailMessages)
		for _, msg := range st.Messages[from:] {
			lines = append(lines, fmt.Sprintf("  - %s: %s", msg.Author, clipTitle(oneLine(msg.Text), masterMessageClip)))
		}
	}

	var b strings.Builder
	for _, line := range lines {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return clipHead(b.String(), masterDetailBytes)
}

// readThreads renders the content of the threads the model asked to read, at
// most masterReadThreads per round. Unknown ids are reported inline so the model
// can correct itself.
func (m *Master) readThreads(ctx context.Context, wanted []MasterAction) string {
	var b strings.Builder
	seen := map[contracts.ThreadID]bool{}
	for i, w := range wanted {
		if seen[w.ThreadID] {
			continue
		}
		seen[w.ThreadID] = true
		if i >= masterReadThreads {
			fmt.Fprintf(&b, "(thread %s skipped: at most %d threads per read)\n", w.ThreadID, masterReadThreads)
			continue
		}
		st, ok := m.thread(ctx, w.ThreadID)
		if !ok {
			fmt.Fprintf(&b, "### Thread %s\n(not found)\n", w.ThreadID)
			continue
		}
		b.WriteString(masterThreadRead(w.ThreadID, st))
	}
	return b.String()
}

// masterThreadRead is the thread's recent messages, newest kept when the budget
// runs out, oldest first in the output.
func masterThreadRead(id contracts.ThreadID, st *domain.ThreadState) string {
	var rows []string
	used := 0
	for i := len(st.Messages) - 1; i >= 0; i-- {
		msg := st.Messages[i]
		row := fmt.Sprintf("- %s: %s\n", msg.Author, clipTitle(oneLine(msg.Text), masterReadMessageClip))
		if used+len(row) > masterReadBytes {
			break
		}
		used += len(row)
		rows = append(rows, row)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### Thread %s (%s) status %s\n", id, clipTitle(st.Title, masterThreadTitleClip), masterThreadStatus(st))
	if len(rows) == 0 {
		b.WriteString("(no messages)\n")
	}
	for i := len(rows) - 1; i >= 0; i-- {
		b.WriteString(rows[i])
	}
	return b.String()
}

// oneLine collapses whitespace so a multi-line value stays on one snapshot line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// masterThreadStatus reduces the thread's agents to one word a model can read.
func masterThreadStatus(st *domain.ThreadState) string {
	running := false
	var latest contracts.AgentStatus
	var latestAt time.Time
	for _, id := range st.AgentOrder {
		a := st.Agents[id]
		if a.Status == contracts.AgentRunning || a.Status == contracts.AgentPending {
			running = true
		}
		at := a.StartedAt
		if a.EndedAt != nil {
			at = *a.EndedAt
		}
		if latestAt.IsZero() || at.After(latestAt) {
			latestAt, latest = at, a.Status
		}
	}
	if running {
		return "running"
	}
	switch latest {
	case contracts.AgentFailed:
		return "failed"
	case contracts.AgentCancelled:
		return "stopped"
	case contracts.AgentSucceeded:
		return "done"
	}
	if len(st.Messages) > 0 {
		return "idle"
	}
	return "new"
}

// masterThreadRuntime is the thread's lead runtime when it is already recorded,
// falling back to whatever the thread itself carries.
func masterThreadRuntime(st *domain.ThreadState) (string, string) {
	if a, ok := st.Agents[leadAgentID(0)]; ok {
		return string(a.Harness), a.Model
	}
	if rt := st.LeadRuntimeFor(0); rt.Harness != "" || rt.Model != "" {
		return string(rt.Harness), rt.Model
	}
	return "", st.Model.Model
}

// harnessLines lists every enabled harness with the models it reports.
func (m *Master) harnessLines() []string {
	var lines []string
	for _, h := range contracts.AllHarnesses() {
		if !m.harnessEnabled(h) {
			continue
		}
		line := "- " + string(h)
		if m.models != nil {
			if list := m.models.List(h); len(list) > 0 {
				line += ": " + strings.Join(list, ", ")
			}
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return []string{"(none)"}
	}
	return lines
}
