package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
)

// newCommandFixture builds a dispatcher over a real in-memory engine with one
// thread, t1. No project is attached, so the command runs in the test's cwd.
func newCommandFixture(t *testing.T) (*domain.Engine, *Dispatcher) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)
	runner := orchestrator.NewRunner(orchestrator.Deps{
		Engine:  eng,
		Logs:    logstore.New(t.TempDir()),
		Mailbox: contextstore.NewMailbox(),
	})
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	return eng, NewDispatcher(DispatcherDeps{Engine: eng, Runner: runner})
}

func runCommandRPC(t *testing.T, disp *Dispatcher, threadID, command string) (map[string]any, error) {
	t.Helper()
	params, err := json.Marshal(map[string]string{"threadId": threadID, "command": command})
	if err != nil {
		t.Fatal(err)
	}
	out, err := disp.Handle(context.Background(), Request{Method: "thread.runCommand", Params: params})
	if err != nil {
		return nil, err
	}
	return out.(map[string]any), nil
}

func TestRunCommandRecordsShellMessageWithoutOpeningTurn(t *testing.T) {
	skipWithoutPOSIXShell(t)
	eng, disp := newCommandFixture(t)

	res, err := runCommandRPC(t, disp, "t1", "echo hi")
	if err != nil {
		t.Fatalf("thread.runCommand: %v", err)
	}
	if res["exitCode"] != 0 {
		t.Fatalf("exitCode = %v, want 0", res["exitCode"])
	}

	st, err := eng.State(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) != 1 {
		t.Fatalf("messages = %d, want exactly one shell message", len(st.Messages))
	}
	msg := st.Messages[0]
	if msg.Author != "shell" {
		t.Fatalf("author = %q, want shell", msg.Author)
	}
	if !strings.Contains(msg.Text, "$ echo hi") || !strings.Contains(msg.Text, "hi\n") {
		t.Fatalf("message text = %q, want command and output", msg.Text)
	}
	// A shell command is not a user turn: the turn counter (and per-turn spend)
	// must not advance.
	if st.Turn != 0 {
		t.Fatalf("turn = %d, want 0", st.Turn)
	}
}

func TestRunCommandRefusedWhileTurnRunning(t *testing.T) {
	skipWithoutPOSIXShell(t)
	eng, disp := newCommandFixture(t)

	_, release, err := disp.beginTurn(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runCommandRPC(t, disp, "t1", "echo hi"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("err = %v, want a turn-already-running refusal", err)
	}
	st, _ := eng.State(context.Background(), "t1")
	if len(st.Messages) != 0 {
		t.Fatalf("refused command still wrote %d message(s)", len(st.Messages))
	}

	release()
	if _, err := runCommandRPC(t, disp, "t1", "echo hi"); err != nil {
		t.Fatalf("after the turn ends the command should run: %v", err)
	}
}

func TestRunCommandValidatesInput(t *testing.T) {
	_, disp := newCommandFixture(t)

	if _, err := runCommandRPC(t, disp, "t1", "   "); err == nil || !strings.Contains(err.Error(), "command is required") {
		t.Fatalf("blank command err = %v, want command is required", err)
	}
	if _, err := runCommandRPC(t, disp, "missing", "echo hi"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unknown thread err = %v, want does not exist", err)
	}
}
