package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/models"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/steering"
	"github.com/agenttunnel/agenttunnel-go/internal/usage"
)

// newMasterRPCFixture wires a dispatcher to a scripted orchestrator harness and
// one non-git project, so master.send can dispatch without cutting a worktree.
func newMasterRPCFixture(t *testing.T, script string) (*domain.Engine, *Dispatcher) {
	t.Helper()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(context.Background())
	t.Cleanup(eng.Close)

	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script, spec.Prompt}, PromptVia: "arg"}, nil
	}, providers.JSONLineTranslator{})
	registry := providers.NewRegistry(driver)
	roles := contracts.NewRoleRegistry(contracts.BuiltinRoles()...)
	store := settings.New(t.TempDir())
	store.AddProject(settings.Project{ID: "p1", Name: "Proj One", Path: t.TempDir()})
	logs := logstore.New(t.TempDir())

	runner := orchestrator.NewRunner(orchestrator.Deps{
		Engine:    eng,
		Registry:  registry,
		Roles:     roles,
		Logs:      logs,
		Ledger:    usage.NewLedger(),
		Mailbox:   contextstore.NewMailbox(),
		Inherited: contextstore.NewInheritedContext(t.TempDir()),
		Settings:  store,
		Steering:  steering.New(),
	})
	disp := NewDispatcher(DispatcherDeps{
		Engine:       eng,
		Runner:       runner,
		Orchestrator: orchestrator.New(runner),
		Steering:     steering.New(),
		Ledger:       runner.Ledger(),
		Logs:         logs,
		Roles:        roles,
		Registry:     registry,
		Settings:     store,
		Models:       models.NewService(registry, store),
	})
	return eng, disp
}

// masterDispatchScript separates the master's one-shot prompt from the turn an
// agent runs: only the master writes the snapshot and proposes, and the turn's
// agent parks until the test creates releaseFile. A turn cannot finish while the
// release file is absent, which is what proves master.send did not await it.
func masterDispatchScript(promptFile, releaseFile, reply string) string {
	frame, _ := json.Marshal(map[string]string{"type": "text", "text": reply})
	return fmt.Sprintf(`case "$0" in
*"home screen"*)
  printf '%%s' "$0" > %q
  cat <<'MASTER_OUT'
%s
MASTER_OUT
  ;;
*)
  while [ ! -f %q ]; do sleep 0.01; done
  printf '%%s\n' '{"type":"text","text":"turn finished"}'
  ;;
esac
`, promptFile, string(frame), releaseFile)
}

func callMasterSend(t *testing.T, disp *Dispatcher, text string) orchestrator.MasterResult {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"text":    text,
		"history": []orchestrator.MasterMessage{{Role: "user", Text: "earlier"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := disp.Handle(context.Background(), Request{Method: "master.send", Params: params})
	if err != nil {
		t.Fatalf("master.send: %v", err)
	}
	return out.(orchestrator.MasterResult)
}

// waitThreadState blocks until cond sees the thread state it wants, waking on
// committed events rather than polling with sleeps. The subscription is made
// before the first read, so an event between the read and the wait is buffered.
func waitThreadState(t *testing.T, eng *domain.Engine, id contracts.ThreadID, cond func(*domain.ThreadState) bool) *domain.ThreadState {
	t.Helper()
	events, cancel := eng.Subscribe(id)
	defer cancel()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		st, err := eng.State(context.Background(), id)
		if err != nil {
			t.Fatalf("state %s: %v", id, err)
		}
		if cond(st) {
			return st
		}
		select {
		case <-events:
		case <-deadline.C:
			t.Fatalf("timed out waiting for thread %s; last state: %+v", id, st)
		}
	}
}

func TestMasterSendStartsThreadWithoutAwaitingTheTurn(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	releaseFile := filepath.Join(dir, "release")
	reply := "On it.\n\n```json\n" +
		`{"type":"start_thread","projectId":"p1","title":"Started work","prompt":"do the work","harness":"opencode","model":"model-x"}` +
		"\n```"
	eng, disp := newMasterRPCFixture(t, masterDispatchScript(promptFile, releaseFile, reply))

	res := callMasterSend(t, disp, "start the work")

	if res.Error != "" {
		t.Fatalf("Error = %q", res.Error)
	}
	if res.Action == nil || res.Action.Title != "Started work" || res.Action.Prompt != "do the work" {
		t.Fatalf("Action = %+v", res.Action)
	}
	if res.ThreadID == "" || res.ProjectID != "p1" {
		t.Fatalf("threadId/projectId = %q/%q, want a dispatched thread", res.ThreadID, res.ProjectID)
	}

	state, err := eng.State(context.Background(), res.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Exists || state.ProjectID != "p1" || state.Title != "Started work" {
		t.Fatalf("thread = %+v", state)
	}
	if got := state.LeadRuntimeFor(0); got.Harness != contracts.HarnessOpenCode || got.Model != "model-x" {
		t.Fatalf("lead runtime = %+v, want the proposed runtime", got)
	}

	// The turn is still parked: it cannot finish until release exists.
	if state.Agents[orchestratorAgentID()].Status == contracts.AgentSucceeded {
		t.Fatalf("master.send awaited the turn: lead already succeeded")
	}
	if _, err := os.Stat(releaseFile); err == nil {
		t.Fatal("test released the turn too early")
	}

	live := waitThreadState(t, eng, res.ThreadID, func(st *domain.ThreadState) bool {
		s := st.Agents[orchestratorAgentID()].Status
		return s == contracts.AgentRunning || s.Terminal()
	})
	if got := live.Agents[orchestratorAgentID()].Status; got != contracts.AgentRunning {
		t.Fatalf("turn ended early with status %s: %+v", got, live.Messages)
	}

	final := waitAfterRelease(t, eng, res.ThreadID, releaseFile)

	if final.Agents[orchestratorAgentID()].Status != contracts.AgentSucceeded {
		t.Fatalf("lead status = %s, want succeeded", final.Agents[orchestratorAgentID()].Status)
	}
	if !hasMessage(final, "turn finished") {
		t.Fatalf("turn output missing: %+v", final.Messages)
	}
}

// waitAfterRelease creates the park-release file and waits for the turn's
// final report message and its lead settling. The report is recorded before
// the lead is marked terminal, so both must be seen before the turn counts
// as finished.
func waitAfterRelease(t *testing.T, eng *domain.Engine, id contracts.ThreadID, releaseFile string) *domain.ThreadState {
	t.Helper()
	if err := os.WriteFile(releaseFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	events, cancel := eng.Subscribe(id)
	defer cancel()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		st, err := eng.State(context.Background(), id)
		if err != nil {
			t.Fatalf("state %s: %v", id, err)
		}
		if hasMessage(st, "turn finished") {
			if s := st.Agents[orchestratorAgentID()].Status; s.Terminal() {
				return st
			}
		} else if s := st.Agents[orchestratorAgentID()].Status; s.Terminal() && s != contracts.AgentSucceeded {
			t.Fatalf("turn ended as %s before reporting: %+v", s, st.Messages)
		}
		select {
		case <-events:
		case <-deadline.C:
			t.Fatalf("timed out waiting for the turn report; last state: %+v", st)
		}
	}
}

func hasMessage(st *domain.ThreadState, text string) bool {
	for _, m := range st.Messages {
		if strings.Contains(m.Text, text) {
			return true
		}
	}
	return false
}

// orchestratorAgentID is lead zero's id, the only lead a fresh thread has.
func orchestratorAgentID() contracts.AgentID { return "agent-orchestrator" }

func TestMasterSendInvalidActionReturnsReplyWithoutThread(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	reply := "Starting.\n\n```json\n" +
		`{"type":"start_thread","projectId":"missing","prompt":"do the work"}` +
		"\n```"
	eng, disp := newMasterRPCFixture(t, masterDispatchScript(promptFile, "", reply))

	res := callMasterSend(t, disp, "start the work")

	if res.Action != nil || res.ThreadID != "" || res.ProjectID != "" {
		t.Fatalf("result = %+v, want no dispatched thread", res)
	}
	if !strings.Contains(res.Error, "not configured") {
		t.Fatalf("Error = %q", res.Error)
	}
	if !strings.Contains(res.Reply, "Which project") {
		t.Fatalf("Reply = %q, want the model's asking reply", res.Reply)
	}
	ids, err := eng.Threads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("threads = %v, want none", ids)
	}
}

func TestMasterSendRequiresText(t *testing.T) {
	_, disp := newMasterRPCFixture(t, `true`)
	params, _ := json.Marshal(map[string]any{"text": "   "})
	if _, err := disp.Handle(context.Background(), Request{Method: "master.send", Params: params}); err == nil ||
		!strings.Contains(err.Error(), "text is required") {
		t.Fatalf("err = %v, want text is required", err)
	}
}
