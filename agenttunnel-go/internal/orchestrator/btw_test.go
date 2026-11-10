package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
