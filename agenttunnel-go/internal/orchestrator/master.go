package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/models"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// The home-screen orchestrator is a one-shot model call over a snapshot of the
// environment. It answers questions and may propose a thread to start, but it
// starts nothing itself: the caller validates the proposal and dispatches.
const (
	masterTimeout           = 2 * time.Minute
	masterMaxText           = 8000
	masterHistoryMessages   = 12
	masterHistoryBudget     = 12 << 10
	masterThreadsPerProject = 15
	masterTitleClip         = 60
	masterThreadTitleClip   = 60
)

// MasterMessage is one prior home-screen exchange, oldest first. Only "user"
// and "assistant" roles are sent to the model.
type MasterMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// MasterAction is a proposed new thread. Harness, model, and variant are
// optional: empty means the orchestrator role's default runtime.
type MasterAction struct {
	Type      string            `json:"type"`
	ProjectID string            `json:"projectId"`
	Title     string            `json:"title"`
	Prompt    string            `json:"prompt"`
	Harness   contracts.Harness `json:"harness,omitempty"`
	Model     string            `json:"model,omitempty"`
	Variant   string            `json:"variant,omitempty"`
}

// MasterResult is the home-screen reply. Action is nil when the model only
// answered a question or proposed something the environment cannot honor; in
// that case Error says why. ThreadID and ProjectID are filled by the caller
// only after the thread was actually created.
type MasterResult struct {
	Reply     string             `json:"reply"`
	Action    *MasterAction      `json:"action"`
	ThreadID  contracts.ThreadID `json:"threadId,omitempty"`
	ProjectID string             `json:"projectId,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// MasterDeps are the services the home-screen orchestrator reads.
type MasterDeps struct {
	Runner   *Runner
	Engine   *domain.Engine
	Settings *settings.Store
	Models   *models.Service
}

// Master answers a home-screen turn from a snapshot of the environment.
type Master struct {
	runner   *Runner
	engine   *domain.Engine
	settings *settings.Store
	models   *models.Service
}

// NewMaster wires the home-screen orchestrator.
func NewMaster(d MasterDeps) *Master {
	return &Master{runner: d.Runner, engine: d.Engine, settings: d.Settings, models: d.Models}
}

// Send answers one home-screen turn. It builds the snapshot prompt, runs the
// orchestrator model once, and validates any thread the model proposed. It never
// starts the thread: the caller dispatches after Send returns.
func (m *Master) Send(ctx context.Context, text string, history []MasterMessage) (MasterResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return MasterResult{}, errors.New("text is required")
	}
	if len(text) > masterMaxText {
		text = clipHead(text, masterMaxText)
	}
	raw, err := m.ask(ctx, m.prompt(ctx, text, history))
	if err != nil {
		return MasterResult{}, err
	}

	reply, action := parseMasterOutput(raw)
	if action == nil {
		return MasterResult{Reply: reply}, nil
	}
	action, reply, invalid := m.validate(action, text, reply)
	return MasterResult{Reply: reply, Action: action, Error: invalid}, nil
}

// ask runs the master model once with no tool environment and no thread, so its
// spend is only a ledger entry. The workdir is the environment default, not a
// project checkout.
func (m *Master) ask(ctx context.Context, prompt string) (string, error) {
	r := m.runner
	if r == nil || r.roles == nil || r.registry == nil {
		return "", errors.New("master chat is unavailable")
	}
	role, ok := r.roles.Get(contracts.RoleOrchestrator)
	if !ok {
		return "", errors.New("orchestrator role is missing")
	}
	harness, model, variant := r.roleRuntime(role)
	if !r.harnessAllowed(harness) {
		return "", fmt.Errorf("harness %s is disabled", harness)
	}

	ctx, cancel := context.WithTimeout(ctx, masterTimeout)
	defer cancel()

	agentID := contracts.AgentID("agent-master-" + r.nextID())
	spec := providers.SessionSpec{
		AgentID: agentID,
		Role:    role.ID,
		Harness: harness,
		Model:   model,
		Variant: variant,
		Prompt:  prompt,
		Workdir: r.workdir(),
	}
	adapter, err := r.registry.NewAdapter(spec)
	if err != nil {
		return "", err
	}
	if err := adapter.Start(ctx, spec); err != nil {
		return "", err
	}
	events, err := adapter.Stream(ctx)
	if err != nil {
		return "", err
	}

	var text []string
	for ev := range events {
		if ev.Usage != nil && r.ledger != nil {
			// CmdUsageRecord needs a real thread; the master has none, so the
			// spend is ledger-only and never stamped onto a fake thread.
			r.ledger.Record(contracts.AgentID("agent-orchestrator"), contracts.RoleOrchestrator, true, *ev.Usage)
		}
		if ev.Kind == contracts.LogText && strings.TrimSpace(ev.Text) != "" {
			text = append(text, ev.Text)
		}
	}
	_ = adapter.Wait(ctx)

	if err := ctx.Err(); err != nil {
		return "", err
	}
	answer := strings.TrimSpace(strings.Join(text, "\n"))
	if answer == "" {
		return "", errors.New("the orchestrator gave no answer")
	}
	return answer, nil
}

// validate checks a proposal against the configured environment and fills its
// defaults. An invalid proposal is returned as nil with a reason, and the reply
// still goes back so the user sees what the model said.
func (m *Master) validate(action *MasterAction, text, reply string) (*MasterAction, string, string) {
	if action.Type != "" && action.Type != "start_thread" {
		return nil, reply, fmt.Sprintf("unsupported action %q", action.Type)
	}
	action.Type = "start_thread"

	projects := m.projects()
	project, ok := findProject(projects, action.ProjectID)
	if !ok {
		if len(projects) == 0 {
			return nil, "There are no projects yet. Add a project first, then tell me what to start.", "no projects are configured"
		}
		return nil, askForProject(projects), fmt.Sprintf("project %q is not configured", action.ProjectID)
	}
	action.ProjectID = project.ID

	if action.Harness != "" && (!action.Harness.Valid() || !m.harnessEnabled(action.Harness)) {
		return nil, reply, fmt.Sprintf("harness %q is not enabled", action.Harness)
	}
	if action.Model != "" {
		if reason := m.modelProblem(action.Harness, action.Model); reason != "" {
			return nil, reply, reason
		}
	}

	action.Title = masterTitle(action.Title, text)
	if action.Prompt == "" {
		action.Prompt = text
	}
	return action, reply, ""
}

// modelProblem reports why a requested model cannot run on the harness, or "".
// A harness that reports no models cannot be checked, and the role's own default
// model is always valid.
func (m *Master) modelProblem(harness contracts.Harness, model string) string {
	if m.models == nil {
		return ""
	}
	if role, ok := m.role(); ok {
		defHarness, defModel, _ := m.runner.roleRuntime(role)
		if harness == "" {
			harness = defHarness
		}
		if model == defModel {
			return ""
		}
	}
	list := m.models.List(harness)
	if len(list) == 0 {
		return ""
	}
	if slices.Contains(list, model) {
		return ""
	}
	return fmt.Sprintf("model %q is not available for %s", model, harness)
}

func (m *Master) role() (contracts.Role, bool) {
	if m.runner == nil || m.runner.roles == nil {
		return contracts.Role{}, false
	}
	return m.runner.roles.Get(contracts.RoleOrchestrator)
}

func (m *Master) projects() []settings.Project {
	if m.settings == nil {
		return nil
	}
	return m.settings.Projects()
}

func (m *Master) harnessEnabled(h contracts.Harness) bool {
	return m.settings == nil || m.settings.HarnessEnabled(h)
}

// prompt snapshots the projects, the threads in each, and the enabled runtimes,
// so the master can answer from what actually exists and pick a valid model.
func (m *Master) prompt(ctx context.Context, text string, history []MasterMessage) string {
	var b strings.Builder
	b.WriteString("You are the AgentTunnel orchestrator on the home screen. You talk with the user about their projects and threads, and when they ask for work to start you propose a new thread. Answer from the snapshot below; do not invent projects, threads, harnesses, or models.\n\n")

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
	byProject := m.threads(ctx)
	if len(byProject) == 0 {
		b.WriteString("(none)\n")
	}
	for _, p := range projects {
		threads := byProject[p.ID]
		if len(threads) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s (%s)\n", p.Name, p.ID)
		for _, t := range threads {
			fmt.Fprintf(&b, "- %s | %s | %s", t.ID, clipTitle(t.Title, masterThreadTitleClip), t.Status)
			if t.Harness != "" || t.Model != "" {
				fmt.Fprintf(&b, " | %s/%s", t.Harness, t.Model)
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\n## Harnesses and models\n")
	for _, line := range m.harnessLines() {
		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString("\n## Conversation so far\n")
	b.WriteString(masterHistory(history))

	b.WriteString("\n## User message\n")
	b.WriteString(text)
	b.WriteString("\n\n## Reply\n")
	b.WriteString("Answer in plain text when the user is asking about their projects or threads. To start a thread, say what you are starting in one short sentence and then include exactly one fenced JSON block:\n")
	b.WriteString("```json\n")
	b.WriteString(`{"type":"start_thread","projectId":"<id from the project list>","title":"<short title>","prompt":"<first message for the thread>","harness":"<optional harness>","model":"<optional model>","variant":"<optional variant>"}`)
	b.WriteString("\n```\n")
	b.WriteString("Use only a projectId, harness, and model from the lists above. Omit harness and model to use the default. If the user has not said which project, ask instead of guessing.\n")
	return b.String()
}

// masterThread is one compact thread row in the snapshot.
type masterThread struct {
	ID        contracts.ThreadID
	Title     string
	Status    string
	Harness   string
	Model     string
	UpdatedAt time.Time
}

// threads groups every thread under its project, newest first and capped per
// project, so a long-lived environment still produces a bounded prompt.
func (m *Master) threads(ctx context.Context) map[string][]masterThread {
	out := map[string][]masterThread{}
	if m.engine == nil {
		return out
	}
	ids, err := m.engine.Threads(ctx)
	if err != nil {
		return out
	}
	for _, id := range ids {
		st, err := m.engine.State(ctx, id)
		if err != nil || st == nil || !st.Exists || st.ProjectID == "" {
			continue
		}
		harness, model := masterThreadRuntime(st)
		out[st.ProjectID] = append(out[st.ProjectID], masterThread{
			ID: id, Title: st.Title, Status: masterThreadStatus(st),
			Harness: harness, Model: model, UpdatedAt: st.UpdatedAt,
		})
	}
	for pid, list := range out {
		sort.Slice(list, func(i, j int) bool {
			if list[i].UpdatedAt.Equal(list[j].UpdatedAt) {
				return list[i].ID > list[j].ID
			}
			return list[i].UpdatedAt.After(list[j].UpdatedAt)
		})
		if len(list) > masterThreadsPerProject {
			list = list[:masterThreadsPerProject]
		}
		out[pid] = list
	}
	return out
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

// masterHistory renders the last messages within a byte budget.
func masterHistory(history []MasterMessage) string {
	if len(history) > masterHistoryMessages {
		history = history[len(history)-masterHistoryMessages:]
	}
	var b strings.Builder
	for _, msg := range history {
		role := strings.TrimSpace(msg.Role)
		if role != "user" && role != "assistant" {
			continue
		}
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", role, text)
	}
	if b.Len() == 0 {
		return "(none)\n"
	}
	return clipTail(b.String(), masterHistoryBudget)
}

// masterProposal is the permissive shape the model's JSON block is decoded into.
type masterProposal struct {
	Type      string `json:"type"`
	Action    string `json:"action"`
	ProjectID string `json:"projectId"`
	Title     string `json:"title"`
	Prompt    string `json:"prompt"`
	Harness   string `json:"harness"`
	Model     string `json:"model"`
	Variant   string `json:"variant"`
}

// parseMasterOutput pulls the first balanced JSON object out of the model's
// text. Nothing that parses as a proposal leaves the raw text as the reply.
func parseMasterOutput(raw string) (string, *MasterAction) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	start, end, ok := findJSONObject(raw)
	if !ok {
		return raw, nil
	}
	var p masterProposal
	if err := json.Unmarshal([]byte(raw[start:end]), &p); err != nil {
		return raw, nil
	}
	kind := strings.TrimSpace(p.Type)
	if kind == "" {
		kind = strings.TrimSpace(p.Action)
	}
	if kind == "" && p.ProjectID == "" && p.Prompt == "" && p.Title == "" {
		return raw, nil
	}
	reply := strings.TrimSpace(stripFences(raw[:start] + "\n" + raw[end:]))
	if kind == "" {
		kind = "start_thread"
	}
	return reply, &MasterAction{
		Type:      kind,
		ProjectID: strings.TrimSpace(p.ProjectID),
		Title:     strings.TrimSpace(p.Title),
		Prompt:    strings.TrimSpace(p.Prompt),
		Harness:   contracts.Harness(strings.TrimSpace(p.Harness)),
		Model:     strings.TrimSpace(p.Model),
		Variant:   strings.TrimSpace(p.Variant),
	}
}

// findJSONObject returns the span of the first balanced {...}, honoring braces
// and quotes inside JSON strings.
func findJSONObject(s string) (int, int, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return 0, 0, false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return start, i + 1, true
			}
		}
	}
	return 0, 0, false
}

// stripFences removes code-fence lines left behind by extracting the object.
func stripFences(s string) string {
	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func findProject(projects []settings.Project, id string) (settings.Project, bool) {
	for _, p := range projects {
		if p.ID == id {
			return p, true
		}
	}
	return settings.Project{}, false
}

func askForProject(projects []settings.Project) string {
	var b strings.Builder
	b.WriteString("Which project should I start this in? Reply with its id: ")
	for i, p := range projects {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s (%s)", p.Name, p.ID)
	}
	b.WriteString(".")
	return b.String()
}

// masterTitle keeps a one-line title, defaulting to the user's message.
func masterTitle(title, text string) string {
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		title = strings.Join(strings.Fields(text), " ")
	}
	return clipTitle(title, masterTitleClip)
}

func clipTitle(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}
