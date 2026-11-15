package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// newBtwHarness is newHarness with the prompt passed as the script's final
// argument ($0) rather than on stdin. A stdin prompt keeps the pipe open until
// the adapter closes it, so a script cannot read its prompt to EOF.
func newBtwHarness(t *testing.T, script string) (*Runner, *domain.Engine, context.Context) {
	t.Helper()
	r, eng, ctx := newHarness(t, "")
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script, spec.Prompt}, PromptVia: "arg"}, nil
	}, providers.JSONLineTranslator{})
	r.registry = providers.NewRegistry(driver)
	return r, eng, ctx
}

// newBtwHarnessPerThread is newBtwHarness with each call's prompt written to a
// file named after the thread it ran under, so concurrent Btw calls on
// different threads can each be inspected without racing on one file.
func newBtwHarnessPerThread(t *testing.T, dir string) (*Runner, *domain.Engine, context.Context) {
	t.Helper()
	r, eng, ctx := newHarness(t, "")
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		script := fmt.Sprintf(`printf '%%s' "$0" > %s; printf '{"type":"text","text":"ANSWER"}\n'`, filepath.Join(dir, string(spec.ThreadID)))
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script, spec.Prompt}, PromptVia: "arg"}, nil
	}, providers.JSONLineTranslator{})
	r.registry = providers.NewRegistry(driver)
	return r, eng, ctx
}

// btwScript answers every side question with ANSWER and writes the prompt it
// was given to promptFile, so a test can inspect the snapshot.
func btwScript(promptFile string) string {
	return `printf '%s' "$0" > ` + promptFile + `; printf '{"type":"text","text":"ANSWER"}\n'`
}

func appendUserMessage(t *testing.T, r *Runner, threadID contracts.ThreadID, text string) {
	t.Helper()
	err := r.engine.Dispatch(context.Background(), contracts.Command{
		ID:       "msg-" + text,
		Type:     contracts.CmdMessageAppend,
		ThreadID: threadID,
		Message:  &contracts.MessageSent{MessageID: "m-" + text, Author: "user", Text: text},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBtwAnswersWithoutRecordingOrBumpingTurn(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	r, eng, ctx := newBtwHarness(t, btwScript(promptFile))
	appendUserMessage(t, r, "t1", "build the parser")

	before, _ := eng.State(ctx, "t1")

	answer, err := r.Btw(ctx, "t1", "what is the parser doing?")
	if err != nil {
		t.Fatalf("Btw: %v", err)
	}
	if answer != "ANSWER" {
		t.Fatalf("answer = %q, want ANSWER", answer)
	}

	after, _ := eng.State(ctx, "t1")
	if after.Turn != before.Turn {
		t.Fatalf("turn = %d, want unchanged %d", after.Turn, before.Turn)
	}
	if len(after.Messages) != len(before.Messages) {
		t.Fatalf("messages = %d, want unchanged %d", len(after.Messages), len(before.Messages))
	}
	if len(after.Agents) != len(before.Agents) {
		t.Fatalf("agents = %d, want unchanged %d: the side agent must not be recorded", len(after.Agents), len(before.Agents))
	}

	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"build the parser", "what is the parser doing?", "Side question"} {
		if !strings.Contains(string(prompt), want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBtwSideAgentHasNoToolEnvironment(t *testing.T) {
	// Btw passes no Env, so the child inherits only the test process. Clear the
	// daemon's own variables so an agent-hosted test run cannot leak them in.
	for _, k := range []string{"AT_TOOL", "AT_AGENT_ID", "AT_THREAD_ID", "AT_DAEMON_URL"} {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	envFile := filepath.Join(t.TempDir(), "env.txt")
	r, _, ctx := newBtwHarness(t, `env > `+envFile+`; printf '{"type":"text","text":"ANSWER"}\n'`)

	if _, err := r.Btw(ctx, "t1", "anything"); err != nil {
		t.Fatalf("Btw: %v", err)
	}
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"AT_TOOL=", "AT_AGENT_ID=", "AT_THREAD_ID=", "AT_DAEMON_URL="} {
		if strings.Contains(string(env), "\n"+k) || strings.HasPrefix(string(env), k) {
			t.Fatalf("side agent inherited %s", k)
		}
	}
}

func TestBtwSnapshotIsPartitionedPerThread(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	r, eng, ctx := newBtwHarness(t, btwScript(promptFile))
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2"}); err != nil {
		t.Fatal(err)
	}
	appendUserMessage(t, r, "t1", "secret of thread one")
	appendUserMessage(t, r, "t2", "thread two work")

	if _, err := r.Btw(ctx, "t2", "status?"); err != nil {
		t.Fatalf("Btw: %v", err)
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "thread two work") {
		t.Fatalf("prompt lacks its own thread's message:\n%s", prompt)
	}
	if strings.Contains(string(prompt), "secret of thread one") {
		t.Fatalf("prompt leaked another thread's message:\n%s", prompt)
	}
}

func TestBtwReportsEmptyAnswer(t *testing.T) {
	r, _, ctx := newBtwHarness(t, `true`)
	if _, err := r.Btw(ctx, "t1", "anything"); err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("err = %v, want no answer", err)
	}
}

func TestBtwRejectsBlankQuestionAndUnknownThread(t *testing.T) {
	r, _, ctx := newBtwHarness(t, `true`)
	if _, err := r.Btw(ctx, "t1", "   "); err == nil || !strings.Contains(err.Error(), "question is required") {
		t.Fatalf("blank question err = %v", err)
	}
	if _, err := r.Btw(ctx, "missing", "hi"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unknown thread err = %v", err)
	}
}

func TestBtwFollowUpCarriesPreviousExchanges(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	r, _, ctx := newBtwHarness(t, btwScript(promptFile))
	appendUserMessage(t, r, "t1", "build the parser")

	if _, err := r.Btw(ctx, "t1", "what is the parser doing?"); err != nil {
		t.Fatalf("first Btw: %v", err)
	}
	if _, err := r.Btw(ctx, "t1", "and the tests?"); err != nil {
		t.Fatalf("second Btw: %v", err)
	}
	// The script truncates the file per call, so it holds the second prompt.
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Previous side conversation", "Q: what is the parser doing?", "A: ANSWER", "and the tests?"} {
		if !strings.Contains(string(prompt), want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBtwTurnBoundaryDropsHistory(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	r, _, ctx := newBtwHarness(t, btwScript(promptFile))
	appendUserMessage(t, r, "t1", "build the parser")

	if _, err := r.Btw(ctx, "t1", "what is the parser doing?"); err != nil {
		t.Fatalf("first Btw: %v", err)
	}
	// A user-authored message opens a new turn, so the snapshot moved on and
	// the remembered exchanges belong to a stale context.
	appendUserMessage(t, r, "t1", "start the next turn")
	if _, err := r.Btw(ctx, "t1", "still there?"); err != nil {
		t.Fatalf("second Btw: %v", err)
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "start the next turn") {
		t.Fatalf("prompt lacks the new turn's message:\n%s", prompt)
	}
	if strings.Contains(string(prompt), "Previous side conversation") {
		t.Fatalf("prompt kept the previous turn's side conversation:\n%s", prompt)
	}
	if strings.Contains(string(prompt), "what is the parser doing?") {
		t.Fatalf("prompt kept the previous turn's question:\n%s", prompt)
	}
}

func TestBtwFollowUpsStayPerThread(t *testing.T) {
	dir := t.TempDir()
	r, eng, ctx := newBtwHarnessPerThread(t, dir)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2"}); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Btw(ctx, "t1", "thread one question"); err != nil {
		t.Fatalf("t1 Btw: %v", err)
	}
	if _, err := r.Btw(ctx, "t2", "thread two question"); err != nil {
		t.Fatalf("t2 Btw: %v", err)
	}
	if _, err := r.Btw(ctx, "t2", "thread two follow-up"); err != nil {
		t.Fatalf("t2 follow-up: %v", err)
	}

	// The per-thread file holds the latest prompt for that thread.
	p2, err := os.ReadFile(filepath.Join(dir, "t2"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Previous side conversation", "Q: thread two question", "A: ANSWER"} {
		if !strings.Contains(string(p2), want) {
			t.Fatalf("t2 prompt missing %q:\n%s", want, p2)
		}
	}
	if strings.Contains(string(p2), "thread one question") {
		t.Fatalf("t2 prompt leaked another thread's side conversation:\n%s", p2)
	}
}

func TestBtwSessionsSurviveConcurrentQuestions(t *testing.T) {
	dir := t.TempDir()
	r, eng, ctx := newBtwHarnessPerThread(t, dir)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2"}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []contracts.ThreadID{"t1", "t2"} {
		wg.Add(1)
		go func(id contracts.ThreadID) {
			defer wg.Done()
			if _, err := r.Btw(ctx, id, "concurrent question about "+string(id)); err != nil {
				errs <- fmt.Errorf("%s: %w", id, err)
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	p1, err := os.ReadFile(filepath.Join(dir, "t1"))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := os.ReadFile(filepath.Join(dir, "t2"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p1), "concurrent question about t1") {
		t.Fatalf("t1 prompt missing its own question:\n%s", p1)
	}
	if strings.Contains(string(p2), "concurrent question about t1") {
		t.Fatalf("t2 prompt leaked t1's question:\n%s", p2)
	}
}

func TestBtwFailedAnswerLeavesHistoryUnchanged(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	script := `printf '%s' "$0" > ` + promptFile + `; case "$0" in *"go silent"*) exit 0;; esac; printf '{"type":"text","text":"ANSWER"}\n'`
	r, _, ctx := newBtwHarness(t, script)
	appendUserMessage(t, r, "t1", "build the parser")

	if _, err := r.Btw(ctx, "t1", "first question"); err != nil {
		t.Fatalf("first Btw: %v", err)
	}
	if _, err := r.Btw(ctx, "t1", "go silent please"); err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("failed Btw err = %v, want no answer", err)
	}
	if _, err := r.Btw(ctx, "t1", "third question"); err != nil {
		t.Fatalf("third Btw: %v", err)
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "Q: first question") {
		t.Fatalf("prompt dropped the successful exchange:\n%s", prompt)
	}
	if strings.Contains(string(prompt), "go silent please") {
		t.Fatalf("prompt recorded the failed exchange:\n%s", prompt)
	}
}
