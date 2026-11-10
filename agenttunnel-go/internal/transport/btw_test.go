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
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/usage"
)

// newBtwFixture wires a dispatcher to a runner whose harness answers every
// side question with ANSWER, so the RPC can be exercised without a real CLI.
func newBtwFixture(t *testing.T) (*domain.Engine, *Dispatcher) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{
			Binary:    "sh",
			Args:      []string{"-c", `printf '{"type":"text","text":"ANSWER"}\n'`, spec.Prompt},
			PromptVia: "arg",
		}, nil
	}, providers.JSONLineTranslator{})
	runner := orchestrator.NewRunner(orchestrator.Deps{
		Engine:    eng,
		Registry:  providers.NewRegistry(driver),
		Roles:     contracts.NewRoleRegistry(contracts.BuiltinRoles()...),
		Logs:      logstore.New(t.TempDir()),
		Ledger:    usage.NewLedger(),
		Mailbox:   contextstore.NewMailbox(),
		Inherited: contextstore.NewInheritedContext(t.TempDir()),
	})
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := eng.Dispatch(ctx, contracts.Command{
		ID:       "m1",
		Type:     contracts.CmdMessageAppend,
		ThreadID: "t1",
		Message:  &contracts.MessageSent{MessageID: "msg-1", Author: "user", Text: "refactor the parser"},
	}); err != nil {
		t.Fatal(err)
	}
	return eng, NewDispatcher(DispatcherDeps{Engine: eng, Runner: runner})
}

func btwRPC(t *testing.T, disp *Dispatcher, threadID, question string) (string, error) {
	t.Helper()
	params, err := json.Marshal(map[string]string{"threadId": threadID, "question": question})
	if err != nil {
		t.Fatal(err)
	}
	out, err := disp.Handle(context.Background(), Request{Method: "thread.btw", Params: params})
	if err != nil {
		return "", err
	}
	return out.(map[string]any)["answer"].(string), nil
}

func TestBtwAnswersWhileTurnRunningWithoutInterruptingIt(t *testing.T) {
	ctx := context.Background()
	eng, disp := newBtwFixture(t)

	// Hold the thread's turn slot, the way a running turn does.
	turnCtx, release, err := disp.beginTurn(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	before, _ := eng.State(ctx, "t1")

	answer, err := btwRPC(t, disp, "t1", "what is it doing?")
	if err != nil {
		t.Fatalf("thread.btw while a turn runs: %v", err)
	}
	if answer != "ANSWER" {
		t.Fatalf("answer = %q, want ANSWER", answer)
	}
	if turnCtx.Err() != nil {
		t.Fatalf("side question cancelled the running turn: %v", turnCtx.Err())
	}

	after, _ := eng.State(ctx, "t1")
	if after.Turn != before.Turn || after.Turn != 1 {
		t.Fatalf("turn = %d, want unchanged at %d", after.Turn, before.Turn)
	}
	if len(after.Messages) != len(before.Messages) {
		t.Fatalf("messages = %d, want unchanged %d: a side question is not steering", len(after.Messages), len(before.Messages))
	}
	for _, m := range after.Messages {
		if m.Author == "steering" {
			t.Fatalf("side question was recorded as steering: %+v", m)
		}
	}
}

func TestBtwValidatesInput(t *testing.T) {
	_, disp := newBtwFixture(t)

	if _, err := btwRPC(t, disp, "t1", "   "); err == nil || !strings.Contains(err.Error(), "question is required") {
		t.Fatalf("blank question err = %v, want question is required", err)
	}
	if _, err := btwRPC(t, disp, "missing", "hi"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unknown thread err = %v, want does not exist", err)
	}
}
