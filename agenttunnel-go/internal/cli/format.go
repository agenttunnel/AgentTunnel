package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// shortThreadIDLen matches docker's 12-character short ids, so `at ps` rows can
// be pasted back into `at logs`/`at attach` by prefix.
const shortThreadIDLen = 12

// threadListRow mirrors transport.ThreadSummary: the list projection ps reads.
type threadListRow struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	ProjectID      string    `json:"projectId,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
	Messages       int       `json:"messages"`
	Agents         int       `json:"agents"`
	RunningAgents  int       `json:"runningAgents"`
	Stale          bool      `json:"stale"`
	ParentThreadID string    `json:"parentThreadId,omitempty"`
}

// threadRowStatus is docker-ps's STATUS word: a thread with an agent in flight
// is running, an old idle one is stale, everything else is idle.
func threadRowStatus(row threadListRow) string {
	switch {
	case row.RunningAgents > 0:
		return "running"
	case row.Stale:
		return "stale"
	default:
		return "idle"
	}
}

func shortThreadID(id string) string {
	if len(id) <= shortThreadIDLen {
		return id
	}
	return id[:shortThreadIDLen]
}

// shortDuration renders an age the way docker does: 12s, 6m, 3h, 4d.
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// relativeAgo renders a timestamp as a docker-style age. An unknown time is a
// dash, never "now": the row has no update to report.
func relativeAgo(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return shortDuration(now.Sub(t)) + " ago"
}

// projectLabel is what the PROJECT column shows: the registered name, else the
// path, else the id.
func projectLabel(p projectsRow) string {
	switch {
	case strings.TrimSpace(p.Name) != "":
		return p.Name
	case strings.TrimSpace(p.Path) != "":
		return p.Path
	default:
		return usageDash(p.ID)
	}
}

func projectByID(projects []projectsRow, id string) (projectsRow, bool) {
	for _, p := range projects {
		if p.ID == id {
			return p, true
		}
	}
	return projectsRow{}, false
}

// expandHome turns "~" and "~/x" into an absolute path. Anything else is
// returned unchanged.
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// resolveThreadRow resolves a thread reference (exact id, unique id prefix, or
// unique title substring) against thread.list, returning the row itself so the
// command can render its title and project without a second lookup.
func resolveThreadRow(ctx context.Context, c cliCaller, ref string) (threadListRow, error) {
	var rows []threadListRow
	if err := c.Call(ctx, "thread.list", nil, &rows); err != nil {
		return threadListRow{}, err
	}
	id, err := matchThreadRow(rows, ref)
	if err != nil {
		return threadListRow{}, err
	}
	for _, row := range rows {
		if row.ID == id {
			return row, nil
		}
	}
	return threadListRow{}, fmt.Errorf("thread %q is no longer listed", id)
}

// matchThreadRow is the job commands' ref resolution: an exact id, then an
// exact title, then a unique id prefix or title substring. A user copying
// `at ps`'s 12-character id sees that id truncated at the front, so a prefix of
// the id and a prefix of its non-"th_" body both count.
func matchThreadRow(rows []threadListRow, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", usageError("thread reference is empty")
	}
	lower := strings.ToLower(ref)
	var exactID, exactTitle []threadListRow
	matched := make(map[string]threadListRow)
	for _, row := range rows {
		switch {
		case row.ID == ref:
			exactID = append(exactID, row)
		case row.Title == ref:
			exactTitle = append(exactTitle, row)
		}
		if threadIDMatches(row.ID, ref) || strings.Contains(strings.ToLower(row.Title), lower) {
			matched[row.ID] = row
		}
	}
	if len(exactID) == 1 {
		return exactID[0].ID, nil
	}
	if len(exactTitle) == 1 {
		return exactTitle[0].ID, nil
	}
	if len(matched) == 1 {
		for id := range matched {
			return id, nil
		}
	}
	if len(matched) == 0 {
		return "", fmt.Errorf("no thread matches %q", ref)
	}
	candidates := make([]string, 0, len(matched))
	for _, row := range rows {
		if _, ok := matched[row.ID]; !ok {
			continue
		}
		label := row.ID
		if row.Title != "" {
			label += fmt.Sprintf(" (%q)", row.Title)
		}
		candidates = append(candidates, label)
	}
	return "", fmt.Errorf("thread reference %q is ambiguous; candidates: %s", ref, strings.Join(candidates, ", "))
}

// threadIDMatches accepts a prefix of the id, a prefix of the id without its
// "th_" prefix, or a prefix of the hex body, so both `at ps`'s short id and a
// bare hash fragment resolve.
func threadIDMatches(id, ref string) bool {
	if strings.HasPrefix(id, ref) {
		return true
	}
	body := strings.TrimPrefix(id, "th_")
	return body != id && strings.HasPrefix(body, ref)
}

// writeLogEntry prints one agent output line with a time, agent, and kind
// prefix. Continuation lines are indented under the first so multi-line
// reasoning stays readable.
func writeLogEntry(w io.Writer, e contracts.LogEntry) {
	stamp := "-"
	if !e.At.IsZero() {
		stamp = e.At.Local().Format("15:04:05")
	}
	kind := string(e.Kind)
	if kind == "" {
		kind = string(contracts.LogText)
	}
	prefix := fmt.Sprintf("%s %s %s ", stamp, e.AgentID, kind)
	for i, line := range strings.Split(strings.TrimRight(e.Text, "\n"), "\n") {
		if i == 0 {
			fmt.Fprintln(w, prefix+line)
			continue
		}
		fmt.Fprintln(w, strings.Repeat(" ", len(prefix))+line)
	}
}

// writeStatusLine reports an agent lifecycle change in the same shape as an
// output line, so a tail reads as one column.
func writeStatusLine(w io.Writer, s contracts.AgentStatusChanged) {
	suffix := ""
	if reason := strings.TrimSpace(s.Reason); reason != "" {
		suffix = " (" + reason + ")"
	}
	fmt.Fprintf(w, "%s %s status %s%s\n", time.Now().Format("15:04:05"), s.AgentID, s.Status, suffix)
}

// logKey identifies a line for backlog/live dedupe. Agent output frames carry
// no timestamp, so identity is (agent, kind, text).
type logKey struct {
	agent string
	kind  string
	text  string
}

// logDeduper drops live frames that the backlog snapshot already printed. It
// counts repeats instead of using a set: a harness may legitimately emit the
// same line twice, and docker-style tails must not lose the second one.
type logDeduper struct {
	remaining map[logKey]int
}

func newLogDeduper(entries []contracts.LogEntry) *logDeduper {
	d := &logDeduper{remaining: make(map[logKey]int, len(entries))}
	for _, e := range entries {
		d.remaining[logKey{agent: string(e.AgentID), kind: string(e.Kind), text: e.Text}]++
	}
	return d
}

// duplicate reports whether text was already printed from the backlog.
func (d *logDeduper) duplicate(agent string, kind contracts.LogKind, text string) bool {
	key := logKey{agent: agent, kind: string(kind), text: text}
	if d.remaining[key] == 0 {
		return false
	}
	d.remaining[key]--
	return true
}

// agentListEntry is the slice of agents.list a tail needs: identity and status.
type agentListEntry struct {
	Agent struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"agent"`
}

// fetchAgentStatuses reads the agents' current lifecycle states.
func fetchAgentStatuses(ctx context.Context, c cliCaller, threadID string) (map[string]contracts.AgentStatus, error) {
	var entries []agentListEntry
	if err := c.Call(ctx, "agents.list", map[string]string{"threadId": threadID}, &entries); err != nil {
		return nil, err
	}
	out := make(map[string]contracts.AgentStatus, len(entries))
	for _, entry := range entries {
		out[entry.Agent.ID] = contracts.AgentStatus(entry.Agent.Status)
	}
	return out, nil
}

// turnTracker decides when a followed turn is over. It is event-driven: the
// lead's terminal status arrives as agent.status-changed, and a dispatch adds
// the agent it names. Polling refreshes statuses and is what catches a turn
// that finished between two events.
type turnTracker struct {
	statuses map[string]contracts.AgentStatus
	// initial is the roster at follow start. requireActivity compares against
	// it so `run --follow` waits on the turn it just started, never on a
	// previous turn's already-terminal roster — while still noticing a turn
	// that starts and finishes entirely between polls.
	initial         map[string]contracts.AgentStatus
	requireActivity bool
	activity        bool
}

func newTurnTracker(statuses map[string]contracts.AgentStatus, requireActivity bool) *turnTracker {
	initial := make(map[string]contracts.AgentStatus, len(statuses))
	for id, status := range statuses {
		initial[id] = status
	}
	return &turnTracker{statuses: statuses, initial: initial, requireActivity: requireActivity}
}

// update replaces the roster with a polled snapshot and records whether the
// turn has visibly moved since the follow began.
func (t *turnTracker) update(statuses map[string]contracts.AgentStatus) {
	if len(statuses) != len(t.initial) {
		t.activity = true
	}
	for id, status := range statuses {
		if before, ok := t.initial[id]; !ok || before != status {
			t.activity = true
		}
	}
	t.statuses = statuses
}

// observe records a status delivered by an event. An event for this thread
// during the follow is itself proof the turn is live.
func (t *turnTracker) observe(id string, status contracts.AgentStatus) {
	t.statuses[id] = status
	t.activity = true
}

// note adds or updates one agent without treating it as activity. A dispatch
// frame names an agent that is about to run; the status that follows is what
// ends the turn.
func (t *turnTracker) note(id string, status contracts.AgentStatus) {
	t.statuses[id] = status
}

func (t *turnTracker) anyActive() bool {
	for _, status := range t.statuses {
		if status == contracts.AgentRunning || status == contracts.AgentPending {
			return true
		}
	}
	return false
}

// started reports whether the turn this follower waits on has shown any life:
// a non-empty roster, or an event.
func (t *turnTracker) started() bool {
	return t.activity || len(t.statuses) > 0
}

func (t *turnTracker) ended() bool {
	if t.anyActive() {
		return false
	}
	// An empty roster means the turn has not been recorded yet, not that it is
	// over: `at logs -f` right after `at run` races the detached lead's first
	// status write. followOutput bounds this wait.
	if !t.started() {
		return false
	}
	if t.requireActivity && !t.activity {
		return false
	}
	return true
}

// withInterrupt cancels ctx on the first Ctrl-C. A follower exits through the
// cancelled context, so its deferred cleanup runs and the shell prompt comes
// back without a killed process. A second signal keeps the process default
// behavior: a hung command must still be escapable.
func withInterrupt(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
			signal.Stop(sigs)
			cancel()
		case <-done:
		}
	}()
	return ctx, func() {
		signal.Stop(sigs)
		close(done)
		cancel()
	}
}
