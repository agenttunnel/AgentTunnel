package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/models"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// newMasterHarness is the side-question fixture with a master wired over the
// same scripted harness and a model catalog seeded through settings. The model
// service no longer ships a default catalog: it merges live discovery with the
// models a user added, so the fixture adds one explicitly to keep the model
// validation tests meaningful.
func newMasterHarness(t *testing.T, script string) (*Runner, *Master, *domain.Engine, context.Context) {
	t.Helper()
	r, eng, ctx := newBtwHarness(t, script)
	r.settings.AddModel(contracts.HarnessClaude, "claude-sonnet-4-5")
	m := NewMaster(MasterDeps{
		Runner:   r,
		Engine:   eng,
		Settings: r.settings,
		Models:   models.NewService(r.registry, r.settings),
	})
	return r, m, eng, ctx
}

// masterScript writes the prompt the master was given to promptFile, then emits
// reply as one text frame. The reply is JSON-marshaled first so fenced blocks and
// newlines survive the shell.
func masterScript(promptFile, reply string) string {
	frame, _ := json.Marshal(map[string]string{"type": "text", "text": reply})
	return fmt.Sprintf(`printf '%%s' "$0" > %q
cat <<'MASTER_OUT'
%s
MASTER_OUT
`, promptFile, string(frame))
}

func masterActionReply(fields string) string {
	return "Starting that.\n\n```json\n{" + fields + "}\n```"
}

func sendMaster(t *testing.T, m *Master, ctx context.Context, text string, history ...MasterMessage) MasterResult {
	t.Helper()
	res, err := m.Send(ctx, text, history)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return res
}

func TestMasterParsesFencedActionAndKeepsProse(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	reply := masterActionReply(`"type":"start_thread","projectId":"p1","title":"Fix auth","prompt":"Fix the auth flow","harness":"claude","model":"claude-sonnet-4-5"`)
	_, m, _, ctx := newMasterHarness(t, masterScript(promptFile, reply))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj", Path: t.TempDir()})

	res := sendMaster(t, m, ctx, "fix auth")

	if res.Error != "" {
		t.Fatalf("Error = %q, want none", res.Error)
	}
	if res.Action == nil {
		t.Fatalf("Action = nil, want start_thread")
	}
	if res.Action.Type != "start_thread" || res.Action.ProjectID != "p1" {
		t.Fatalf("Action = %+v", res.Action)
	}
	if res.Action.Title != "Fix auth" || res.Action.Prompt != "Fix the auth flow" {
		t.Fatalf("Action = %+v", res.Action)
	}
	if res.Action.Harness != contracts.HarnessClaude || res.Action.Model != "claude-sonnet-4-5" {
		t.Fatalf("Action runtime = %s/%s", res.Action.Harness, res.Action.Model)
	}
	if !strings.Contains(res.Reply, "Starting that.") || strings.Contains(res.Reply, "```") || strings.Contains(res.Reply, "{") {
		t.Fatalf("Reply = %q, want the prose without the JSON block", res.Reply)
	}
}

func TestMasterParsesActionFromLeadingProse(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	reply := `Sure. {"type":"start_thread","projectId":"p1","prompt":"do it"} — starting now.`
	_, m, _, ctx := newMasterHarness(t, masterScript(promptFile, reply))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj", Path: t.TempDir()})

	res := sendMaster(t, m, ctx, "do it")

	if res.Action == nil || res.Action.ProjectID != "p1" {
		t.Fatalf("Action = %+v", res.Action)
	}
	if !strings.Contains(res.Reply, "Sure.") || !strings.Contains(res.Reply, "starting now.") {
		t.Fatalf("Reply = %q", res.Reply)
	}
}

func TestMasterPlainTextIsTheReply(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	_, m, _, ctx := newMasterHarness(t, masterScript(promptFile, "You have two threads running."))

	res := sendMaster(t, m, ctx, "what is running?")

	if res.Action != nil || res.Error != "" {
		t.Fatalf("result = %+v, want no action and no error", res)
	}
	if res.Reply != "You have two threads running." {
		t.Fatalf("Reply = %q", res.Reply)
	}
}

func TestMasterRejectsUnknownProjectAndAsks(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	reply := masterActionReply(`"type":"start_thread","projectId":"nope","prompt":"do it"`)
	_, m, _, ctx := newMasterHarness(t, masterScript(promptFile, reply))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj One", Path: t.TempDir()})

	res := sendMaster(t, m, ctx, "do it")

	if res.Action != nil {
		t.Fatalf("Action = %+v, want nil", res.Action)
	}
	if !strings.Contains(res.Error, "not configured") {
		t.Fatalf("Error = %q", res.Error)
	}
	if !strings.Contains(res.Reply, "Which project") || !strings.Contains(res.Reply, "p1") {
		t.Fatalf("Reply = %q, want a question naming the projects", res.Reply)
	}
}

func TestMasterWithNoProjectsAsksToAddOne(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	reply := masterActionReply(`"type":"start_thread","projectId":"p1","prompt":"do it"`)
	_, m, _, ctx := newMasterHarness(t, masterScript(promptFile, reply))

	res := sendMaster(t, m, ctx, "do it")

	if res.Action != nil {
		t.Fatalf("Action = %+v, want nil", res.Action)
	}
	if !strings.Contains(res.Error, "no projects") {
		t.Fatalf("Error = %q", res.Error)
	}
	if !strings.Contains(res.Reply, "Add a project first") {
		t.Fatalf("Reply = %q", res.Reply)
	}
}

func TestMasterRejectsDisabledHarness(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	reply := masterActionReply(`"type":"start_thread","projectId":"p1","prompt":"do it","harness":"claude"`)
	r, m, _, ctx := newMasterHarness(t, masterScript(promptFile, reply))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj", Path: t.TempDir()})
	r.settings.SetHarnessEnabled(contracts.HarnessClaude, false)

	res := sendMaster(t, m, ctx, "do it")

	if res.Action != nil || !strings.Contains(res.Error, "not enabled") {
		t.Fatalf("result = %+v, want an invalid harness", res)
	}
	if res.Reply != "Starting that." {
		t.Fatalf("Reply = %q, the model's text still comes back", res.Reply)
	}
}

func TestMasterRejectsUnknownModel(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	reply := masterActionReply(`"type":"start_thread","projectId":"p1","prompt":"do it","harness":"claude","model":"not-a-model"`)
	_, m, _, ctx := newMasterHarness(t, masterScript(promptFile, reply))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj", Path: t.TempDir()})

	res := sendMaster(t, m, ctx, "do it")

	if res.Action != nil || !strings.Contains(res.Error, "not available") {
		t.Fatalf("result = %+v, want an invalid model", res)
	}
}

func TestMasterRejectsUnsupportedAction(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	reply := masterActionReply(`"type":"delete_everything","projectId":"p1","prompt":"do it"`)
	_, m, _, ctx := newMasterHarness(t, masterScript(promptFile, reply))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj", Path: t.TempDir()})

	res := sendMaster(t, m, ctx, "do it")

	if res.Action != nil || !strings.Contains(res.Error, "unsupported action") {
		t.Fatalf("result = %+v, want an unsupported action", res)
	}
}

func TestMasterDefaultsTitleFromText(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	reply := masterActionReply(`"type":"start_thread","projectId":"p1","prompt":"do it"`)
	_, m, _, ctx := newMasterHarness(t, masterScript(promptFile, reply))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj", Path: t.TempDir()})

	text := "  refactor\nthe parser   please  "
	res := sendMaster(t, m, ctx, text)

	if res.Action == nil || res.Action.Title != "refactor the parser please" {
		t.Fatalf("Action = %+v, want a title from the text", res.Action)
	}
}

func TestMasterSelectionIsLedgerOnly(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	script := masterScript(promptFile, "done") + `printf '{"usage":{"input_tokens":3,"output_tokens":2}}\n'`
	r, m, eng, ctx := newMasterHarness(t, script)

	if _, err := m.Send(ctx, "anything", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	usage, ok := r.Ledger().Agent("agent-orchestrator")
	if !ok || usage.Usage.Total() != 5 {
		t.Fatalf("ledger = %+v, want 5 orchestrator tokens", usage)
	}
	snap := r.Ledger().Snapshot()
	if snap.Orchestrator.Total() != 5 {
		t.Fatalf("orchestrator usage = %d", snap.Orchestrator.Total())
	}
	// Nothing may be recorded against a thread: the master is not a turn.
	ids, err := eng.Threads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		st, _ := eng.State(ctx, id)
		if got := st.ThreadUsage().Orchestrator.Total(); got != 0 {
			t.Fatalf("thread %s recorded master usage: %d", id, got)
		}
	}
}

func TestMasterPromptSnapshotsProjectsAndThreads(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	_, m, eng, ctx := newMasterHarness(t, masterScript(promptFile, "ok"))
	one, two := t.TempDir(), t.TempDir()
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj One", Path: one})
	m.settings.AddProject(settings.Project{ID: "p2", Name: "Proj Two", Path: two})

	for _, cmd := range []contracts.Command{
		{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "th_p1", Title: "Parser rewrite", ProjectID: "p1"},
		{ID: "c3", Type: contracts.CmdThreadCreate, ThreadID: "th_p2", Title: "Auth hardening", ProjectID: "p2"},
	} {
		if err := eng.Dispatch(ctx, cmd); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := m.Send(ctx, "what am I working on?", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	prompt := readPromptFile(t, promptFile)
	for _, want := range []string{
		"Proj One", "p1", one,
		"Proj Two", "p2", two,
		"th_p1", "Parser rewrite",
		"th_p2", "Auth hardening",
		"claude", "claude-sonnet-4-5",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestMasterClipsHistoryToRecentMessages(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	_, m, _, ctx := newMasterHarness(t, masterScript(promptFile, "ok"))

	var history []MasterMessage
	for i := 0; i < 20; i++ {
		history = append(history, MasterMessage{Role: "user", Text: fmt.Sprintf("HISTORY-OLD-%02d", i)})
	}
	if _, err := m.Send(ctx, "continue", history); err != nil {
		t.Fatalf("Send: %v", err)
	}
	prompt := readPromptFile(t, promptFile)
	if !strings.Contains(prompt, "HISTORY-OLD-19") {
		t.Fatalf("prompt lost the newest history:\n%s", prompt)
	}
	if strings.Contains(prompt, "HISTORY-OLD-07") {
		t.Fatalf("prompt kept history older than the last 12 messages:\n%s", prompt)
	}
}

func TestMasterClipsHistoryToByteBudget(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	_, m, _, ctx := newMasterHarness(t, masterScript(promptFile, "ok"))

	big := "HEAD-MARKER" + strings.Repeat("x", 20<<10) + "TAIL-MARKER"
	history := []MasterMessage{{Role: "assistant", Text: big}}
	if _, err := m.Send(ctx, "continue", history); err != nil {
		t.Fatalf("Send: %v", err)
	}
	prompt := readPromptFile(t, promptFile)
	if !strings.Contains(prompt, "TAIL-MARKER") {
		t.Fatalf("prompt lost the tail of the history:\n%s", prompt)
	}
	if strings.Contains(prompt, "HEAD-MARKER") {
		t.Fatalf("prompt kept more than the history byte budget")
	}
}

func readPromptFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
