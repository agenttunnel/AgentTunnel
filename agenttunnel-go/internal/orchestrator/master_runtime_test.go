package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/models"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// newRuntimeMaster builds a master over a harness that records the
// harness/model/variant each session spawned with, so a test can prove which
// runtime a master turn really ran on.
func newRuntimeMaster(t *testing.T, reply string) (*Master, *settings.Store, string) {
	t.Helper()
	r, eng, _ := newHarness(t, "")
	runtimeFile := filepath.Join(t.TempDir(), "runtime.txt")
	script := "cat <<'OUT'\n" + masterReplyFrame(reply) + "\nOUT\n"
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		line := fmt.Sprintf("%s|%s|%s\n", spec.Harness, spec.Model, spec.Variant)
		if err := os.WriteFile(runtimeFile, []byte(line), 0o644); err != nil {
			return providers.CommandSpec{}, err
		}
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script, spec.Prompt}, PromptVia: "arg"}, nil
	}, providers.JSONLineTranslator{})
	r.registry = providers.NewRegistry(driver)
	m := NewMaster(MasterDeps{
		Runner:   r,
		Engine:   eng,
		Settings: r.settings,
		Models:   models.NewService(r.registry, r.settings),
	})
	return m, r.settings, runtimeFile
}

// masterReplyFrame renders the master reply as one JSON text line.
func masterReplyFrame(reply string) string {
	return fmt.Sprintf(`{"type":"text","text":%q}`, reply)
}

// A master turn runs on the persisted master runtime, not the role default.
func TestMasterRuntimeHonoredByMasterTurns(t *testing.T) {
	m, store, runtimeFile := newRuntimeMaster(t, "Nothing is running.")
	store.AddModel(contracts.HarnessOpenCode, "big-model")

	if err := m.SetRuntime(contracts.HarnessOpenCode, "big-model", ""); err != nil {
		t.Fatalf("SetRuntime: %v", err)
	}

	if _, err := m.Send(context.Background(), "what is running?", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, err := os.ReadFile(runtimeFile)
	if err != nil {
		t.Fatalf("read runtime capture: %v", err)
	}
	if want := "opencode|big-model|"; strings.TrimSpace(string(got)) != want {
		t.Fatalf("master ran on %q, want %q", got, want)
	}
}

// Without an override the master runs on the Orchestrator role's default.
func TestMasterRuntimeDefaultsToRoleRuntime(t *testing.T) {
	m, _, runtimeFile := newRuntimeMaster(t, "Nothing is running.")

	if _, err := m.Send(context.Background(), "what is running?", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, err := os.ReadFile(runtimeFile)
	if err != nil {
		t.Fatalf("read runtime capture: %v", err)
	}
	if want := "opencode||"; strings.TrimSpace(string(got)) != want {
		t.Fatalf("master ran on %q, want %q", got, want)
	}
}

// Overriding the harness drops the inherited model; setting one in the same
// override keeps it.
func TestMasterRuntimeResolution(t *testing.T) {
	m, store, _ := newRuntimeMaster(t, "")
	store.AddModel(contracts.HarnessClaude, "claude-sonnet-4-5")

	h, model, variant := m.Runtime()
	if h != contracts.HarnessOpenCode || model != "" || variant != "" {
		t.Fatalf("default runtime = %s/%s/%s, want opencode defaults", h, model, variant)
	}

	store.SetMasterRuntime(settings.RoleConfig{Model: "big-model"})
	h, model, _ = m.Runtime()
	if h != contracts.HarnessOpenCode || model != "big-model" {
		t.Fatalf("model override = %s/%s, want opencode/big-model", h, model)
	}

	store.SetMasterRuntime(settings.RoleConfig{Harness: contracts.HarnessClaude})
	h, model, variant = m.Runtime()
	if h != contracts.HarnessClaude || model != "" || variant != "" {
		t.Fatalf("harness override = %s/%s/%s, want claude with the inherited model dropped", h, model, variant)
	}

	store.SetMasterRuntime(settings.RoleConfig{Harness: contracts.HarnessClaude, Model: "claude-sonnet-4-5"})
	h, model, _ = m.Runtime()
	if h != contracts.HarnessClaude || model != "claude-sonnet-4-5" {
		t.Fatalf("full override = %s/%s, want claude/claude-sonnet-4-5", h, model)
	}

	store.SetMasterRuntime(settings.RoleConfig{})
	if _, ok := m.RuntimeOverride(); ok {
		t.Fatal("zero override should remove the stored runtime")
	}
	h, _, _ = m.Runtime()
	if h != contracts.HarnessOpenCode {
		t.Fatalf("runtime after clear = %s, want the role default", h)
	}
}

// SetRuntime rejects what a master turn could not run.
func TestMasterSetRuntimeValidates(t *testing.T) {
	m, store, _ := newRuntimeMaster(t, "")
	store.AddModel(contracts.HarnessOpenCode, "model-x")

	if err := m.SetRuntime("nope", "", ""); err == nil || !strings.Contains(err.Error(), "unknown harness") {
		t.Fatalf("err = %v, want unknown harness", err)
	}
	store.SetHarnessEnabled(contracts.HarnessClaude, false)
	if err := m.SetRuntime(contracts.HarnessClaude, "", ""); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("err = %v, want harness not enabled", err)
	}
	if err := m.SetRuntime(contracts.HarnessOpenCode, "not-a-model", ""); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v, want model not available", err)
	}
	if err := m.SetRuntime("", "", ""); err != nil {
		t.Fatalf("empty override should clear, got %v", err)
	}
}

// The reporter report lists threads with their status and what waits on the
// user, running threads first.
func TestMasterReportRendersThreads(t *testing.T) {
	m, _, _ := newRuntimeMaster(t, "")
	ctx := context.Background()
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj One", Path: t.TempDir()})
	m.settings.AddProject(settings.Project{ID: "p2", Name: "Proj Two", Path: t.TempDir()})

	// One running thread in p2 and one finished thread in p1.
	for _, tc := range []struct {
		id      contracts.ThreadID
		project string
		status  contracts.AgentStatus
		msg     string
	}{
		{"t_idle", "p1", contracts.AgentSucceeded, "the parsers pass"},
		{"t_run", "p2", contracts.AgentRunning, ""},
	} {
		if err := m.engine.Dispatch(ctx, contracts.Command{
			ID: "c-" + string(tc.id), Type: contracts.CmdThreadCreate, ThreadID: tc.id, ProjectID: tc.project, Title: "Thread " + string(tc.id),
		}); err != nil {
			t.Fatal(err)
		}
		if tc.msg != "" {
			if err := m.engine.Dispatch(ctx, contracts.Command{
				ID: "m-" + string(tc.id), Type: contracts.CmdMessageAppend, ThreadID: tc.id,
				Message: &contracts.MessageSent{MessageID: "msg-" + string(tc.id), Author: "user", Text: tc.msg},
			}); err != nil {
				t.Fatal(err)
			}
		}
		agent := contracts.AgentID("agent-" + string(tc.id))
		if err := m.engine.Dispatch(ctx, contracts.Command{
			ID: "d-" + string(tc.id), Type: contracts.CmdTaskDispatch, ThreadID: tc.id,
			Dispatch: &contracts.DispatchTask{AgentID: agent, Name: "a", Role: "executor"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := m.engine.Dispatch(ctx, contracts.Command{
			ID: "s-" + string(tc.id), Type: contracts.CmdAgentStatusSet, ThreadID: tc.id,
			Status: &contracts.AgentStatusChanged{AgentID: agent, Status: tc.status},
		}); err != nil {
			t.Fatal(err)
		}
	}

	report := m.Report(ctx)

	runAt := strings.Index(report, "Thread t_run")
	idleAt := strings.Index(report, "Thread t_idle")
	if runAt < 0 || idleAt < 0 {
		t.Fatalf("report missing threads:\n%s", report)
	}
	if runAt > idleAt {
		t.Fatalf("running thread should come first:\n%s", report)
	}
	if !strings.Contains(report, "Proj One") || !strings.Contains(report, "Proj Two") {
		t.Fatalf("report missing project names:\n%s", report)
	}
	if !strings.Contains(report, "running") || !strings.Contains(report, "done") {
		t.Fatalf("report missing statuses:\n%s", report)
	}
	if !strings.Contains(report, "user: the parsers pass") {
		t.Fatalf("report missing the last message:\n%s", report)
	}
}

// Report names pending questions, so the user knows a thread is blocked.
func TestMasterReportFlagsPendingQuestions(t *testing.T) {
	m, _, _ := newRuntimeMaster(t, "")
	ctx := context.Background()
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj One", Path: t.TempDir()})
	if err := m.engine.Dispatch(ctx, contracts.Command{
		ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t-blocked", ProjectID: "p1", Title: "Blocked",
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.engine.Dispatch(ctx, contracts.Command{
		ID: "q1", Type: contracts.CmdQuestionAsk, ThreadID: "t-blocked",
		QuestionAsk: &contracts.QuestionAsked{QuestionID: "q1", AgentID: "agent-a", Question: "which database?", Kind: contracts.QuestionText},
	}); err != nil {
		t.Fatal(err)
	}

	if report := m.Report(ctx); !strings.Contains(report, "waiting on you") || !strings.Contains(report, "which database?") {
		t.Fatalf("report missing the pending question:\n%s", report)
	}
}

// A report with no threads says so instead of sending an empty message.
func TestMasterReportNoThreads(t *testing.T) {
	m, _, _ := newRuntimeMaster(t, "")
	if report := m.Report(context.Background()); !strings.Contains(report, "no threads") {
		t.Fatalf("report = %q, want the no-threads notice", report)
	}
}

// Master conversations never appear in a report: they have no project.
func TestMasterReportSkipsMasterConversations(t *testing.T) {
	m, _, _ := newRuntimeMaster(t, "")
	ctx := context.Background()
	if err := m.engine.Dispatch(ctx, contracts.Command{
		ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t-conv", Title: "Old chat", Owner: contracts.ThreadOwnerMaster,
	}); err != nil {
		t.Fatal(err)
	}
	if report := m.Report(ctx); strings.Contains(report, "Old chat") {
		t.Fatalf("report leaked a master conversation:\n%s", report)
	}
}
