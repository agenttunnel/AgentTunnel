package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/models"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// The home-screen orchestrator is a model call over a snapshot of the
// environment. It answers questions and proposes up to masterMaxActions
// validated actions. It executes nothing itself: the transport dispatcher runs
// the validated actions through the same paths the RPC surface uses.
const (
	masterTimeout         = 2 * time.Minute
	masterMaxText         = 8000
	masterMaxActions      = 5
	masterReadRounds      = 2
	masterHistoryMessages = 12
	masterHistoryBudget   = 12 << 10
	masterTitleClip       = 60
)

// Action types the home-screen orchestrator may propose.
const (
	masterStartThread    = "start_thread"
	masterSendMessage    = "send_message"
	masterStopThread     = "stop_thread"
	masterRenameThread   = "rename_thread"
	masterSetPeerMailbox = "set_peer_mailbox"
	masterDeleteThread   = "delete_thread"
	masterReadThread     = "read_thread"
)

// MasterMessage is one prior home-screen exchange, oldest first. Only "user"
// and "assistant" roles are sent to the model.
type MasterMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// MasterAction is one proposed operation. Fields other than Type apply to some
// types only; see the prompt for the whitelist. Harness, model, and variant on a
// start_thread are optional: empty means the orchestrator role's default runtime.
type MasterAction struct {
	Type      string             `json:"type"`
	ProjectID string             `json:"projectId"`
	ThreadID  contracts.ThreadID `json:"threadId,omitempty"`
	Title     string             `json:"title"`
	Prompt    string             `json:"prompt"`
	Harness   contracts.Harness  `json:"harness,omitempty"`
	Model     string             `json:"model,omitempty"`
	Variant   string             `json:"variant,omitempty"`
	// Enabled is set_peer_mailbox's value. A pointer so a missing value is
	// rejected rather than read as false.
	Enabled *bool `json:"enabled,omitempty"`
	// Confirm is the user's explicit yes for a destructive action.
	Confirm bool `json:"confirm,omitempty"`
}

// MasterActionResult reports what happened to one action of a reply. Rejected
// proposals carry Error from validation; executed ones carry the transport's
// outcome. ThreadID and ProjectID are set when the action created or touched a
// thread.
type MasterActionResult struct {
	Type      string             `json:"type"`
	ThreadID  contracts.ThreadID `json:"threadId,omitempty"`
	ProjectID string             `json:"projectId,omitempty"`
	OK        bool               `json:"ok"`
	Error     string             `json:"error,omitempty"`
}

// MasterResult is the home-screen reply. Actions holds the validated proposals
// in the order the model gave them; Results holds one entry per rejected
// proposal first, then one per executed action. Action is the first validated
// action, kept for clients that read a single action. ThreadID and ProjectID
// are the first thread a start_thread created, filled by the transport only
// after it was actually created.
type MasterResult struct {
	Reply     string               `json:"reply"`
	Action    *MasterAction        `json:"action"`
	Actions   []MasterAction       `json:"actions,omitempty"`
	Results   []MasterActionResult `json:"results,omitempty"`
	ThreadID  contracts.ThreadID   `json:"threadId,omitempty"`
	ProjectID string               `json:"projectId,omitempty"`
	Error     string               `json:"error,omitempty"`
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

// Send answers one home-screen turn. The model may first ask to read a thread's
// recent messages; each such round resolves the request and asks again, at most
// masterReadRounds times. Then it validates every proposed action. It never
// executes one: the caller runs Actions after Send returns.
func (m *Master) Send(ctx context.Context, text string, history []MasterMessage) (MasterResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return MasterResult{}, errors.New("text is required")
	}
	if len(text) > masterMaxText {
		text = clipHead(text, masterMaxText)
	}

	var (
		reads     []string
		reply     string
		proposals []MasterAction
		rejected  []MasterActionResult
	)
	for round := 0; ; round++ {
		raw, err := m.ask(ctx, m.prompt(ctx, text, history, reads))
		if err != nil {
			return MasterResult{}, err
		}
		reply, proposals = parseMasterOutput(raw)
		var wanted []MasterAction
		proposals, wanted = splitReads(proposals)
		if len(wanted) == 0 {
			break
		}
		if round >= masterReadRounds {
			for _, w := range wanted {
				rejected = append(rejected, MasterActionResult{
					Type: masterReadThread, ThreadID: w.ThreadID,
					Error: fmt.Sprintf("read limit reached: at most %d reads per message", masterReadRounds),
				})
			}
			break
		}
		// A reply that asks to read runs none of its other actions: the model
		// proposes again once it has the content in view.
		proposals = nil
		reads = append(reads, m.readThreads(ctx, wanted))
	}

	var errs []string
	if len(proposals) > masterMaxActions {
		for _, extra := range proposals[masterMaxActions:] {
			rejected = append(rejected, rejectedAction(extra, fmt.Sprintf("at most %d actions per reply", masterMaxActions)))
			errs = append(errs, fmt.Sprintf("at most %d actions per reply", masterMaxActions))
		}
		proposals = proposals[:masterMaxActions]
	}

	var actions []MasterAction
	override := ""
	for _, p := range proposals {
		action, reason, replace := m.validate(ctx, p, text)
		if reason != "" {
			rejected = append(rejected, rejectedAction(p, reason))
			errs = append(errs, reason)
			if replace != "" && override == "" {
				override = replace
			}
			continue
		}
		actions = append(actions, action)
	}
	if len(actions) == 0 && override != "" {
		reply = override
	}
	res := MasterResult{Reply: reply, Actions: actions, Results: rejected, Error: strings.Join(errs, "; ")}
	if len(actions) > 0 {
		res.Action = &actions[0]
	}
	return res, nil
}

func rejectedAction(a MasterAction, reason string) MasterActionResult {
	return MasterActionResult{Type: a.Type, ThreadID: a.ThreadID, ProjectID: a.ProjectID, Error: reason}
}

// splitReads separates read_thread requests from the actions that run.
func splitReads(actions []MasterAction) (rest, reads []MasterAction) {
	for _, a := range actions {
		if a.Type == masterReadThread {
			reads = append(reads, a)
			continue
		}
		rest = append(rest, a)
	}
	return rest, reads
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

// validate checks one proposal against real state and fills its defaults. It
// returns the reason it is rejected, or "" when it may run. replace, when set,
// is a reply that stands in for the model's prose because the proposal named a
// project that does not exist.
func (m *Master) validate(ctx context.Context, a MasterAction, text string) (action MasterAction, reason, replace string) {
	switch a.Type {
	case masterStartThread:
		return m.validateStart(a, text)
	case masterSendMessage, masterStopThread, masterRenameThread, masterSetPeerMailbox, masterDeleteThread:
		return m.validateThread(ctx, a)
	}
	return a, fmt.Sprintf("unsupported action %q", a.Type), ""
}

func (m *Master) validateStart(a MasterAction, text string) (MasterAction, string, string) {
	projects := m.projects()
	project, ok := findProject(projects, a.ProjectID)
	if !ok {
		if len(projects) == 0 {
			return a, "no projects are configured", "There are no projects yet. Add a project first, then tell me what to start."
		}
		return a, fmt.Sprintf("project %q is not configured", a.ProjectID), askForProject(projects)
	}
	a.ProjectID = project.ID

	if a.Harness != "" && (!a.Harness.Valid() || !m.harnessEnabled(a.Harness)) {
		return a, fmt.Sprintf("harness %q is not enabled", a.Harness), ""
	}
	if a.Model != "" {
		if reason := m.modelProblem(a.Harness, a.Model); reason != "" {
			return a, reason, ""
		}
	}

	a.Title = masterTitle(a.Title, text)
	if a.Prompt == "" {
		a.Prompt = text
	}
	return a, "", ""
}

// validateThread checks an action on an existing thread. The thread must exist
// in the engine; its project is filled from it, never trusted from the model.
func (m *Master) validateThread(ctx context.Context, a MasterAction) (MasterAction, string, string) {
	st, ok := m.thread(ctx, a.ThreadID)
	if !ok {
		return a, fmt.Sprintf("thread %q does not exist", a.ThreadID), ""
	}
	a.ProjectID = st.ProjectID

	switch a.Type {
	case masterSendMessage:
		if a.Prompt == "" {
			return a, "send_message needs a prompt", ""
		}
	case masterRenameThread:
		a.Title = clipTitle(strings.Join(strings.Fields(a.Title), " "), masterTitleClip)
		if a.Title == "" {
			return a, "rename_thread needs a title", ""
		}
	case masterSetPeerMailbox:
		if a.Enabled == nil {
			return a, "set_peer_mailbox needs enabled true or false", ""
		}
	case masterDeleteThread:
		if !a.Confirm {
			return a, "delete_thread needs the user's explicit confirmation: ask them first", ""
		}
	}
	return a, "", ""
}

// thread returns the named thread's state when it exists and is not deleted.
func (m *Master) thread(ctx context.Context, id contracts.ThreadID) (*domain.ThreadState, bool) {
	if m.engine == nil || id == "" {
		return nil, false
	}
	st, err := m.engine.State(ctx, id)
	if err != nil || st == nil || !st.Exists || st.Deleted {
		return nil, false
	}
	return st, true
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

// masterProposal is the permissive shape one JSON action is decoded into. A
// reply may carry {"actions":[...]} or a single bare action object.
type masterProposal struct {
	Type      string           `json:"type"`
	Action    string           `json:"action"`
	ThreadID  string           `json:"threadId"`
	ProjectID string           `json:"projectId"`
	Title     string           `json:"title"`
	Prompt    string           `json:"prompt"`
	Harness   string           `json:"harness"`
	Model     string           `json:"model"`
	Variant   string           `json:"variant"`
	Enabled   *bool            `json:"enabled"`
	Confirm   bool             `json:"confirm"`
	Actions   []masterProposal `json:"actions"`
}

// action converts one decoded object, or reports that it is not an action at all.
func (p masterProposal) action() (MasterAction, bool) {
	kind := strings.TrimSpace(p.Type)
	if kind == "" {
		kind = strings.TrimSpace(p.Action)
	}
	if kind == "" && p.ThreadID == "" && p.ProjectID == "" && p.Prompt == "" && p.Title == "" {
		return MasterAction{}, false
	}
	if kind == "" {
		kind = masterStartThread
	}
	return MasterAction{
		Type:      kind,
		ProjectID: strings.TrimSpace(p.ProjectID),
		ThreadID:  contracts.ThreadID(strings.TrimSpace(p.ThreadID)),
		Title:     strings.TrimSpace(p.Title),
		Prompt:    strings.TrimSpace(p.Prompt),
		Harness:   contracts.Harness(strings.TrimSpace(p.Harness)),
		Model:     strings.TrimSpace(p.Model),
		Variant:   strings.TrimSpace(p.Variant),
		Enabled:   p.Enabled,
		Confirm:   p.Confirm,
	}, true
}

// parseMasterOutput pulls the first balanced JSON object out of the model's
// text. Its "actions" array, or the object itself when there is none, becomes
// the proposals. Nothing that parses as an action leaves the raw text as reply.
func parseMasterOutput(raw string) (string, []MasterAction) {
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
	items := p.Actions
	if items == nil {
		items = []masterProposal{p}
	}
	var out []MasterAction
	for _, it := range items {
		if a, ok := it.action(); ok {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return raw, nil
	}
	reply := strings.TrimSpace(stripFences(raw[:start] + "\n" + raw[end:]))
	return reply, out
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
