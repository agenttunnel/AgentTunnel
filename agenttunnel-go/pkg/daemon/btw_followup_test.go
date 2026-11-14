package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// readBtwPromptLog returns the side-question prompts captured by the harness
// script below, in the order the harnesses ran: each section follows a
// <<<btw-prompt>>> marker line. The two thread.btw calls are strictly
// sequential (the RPC reply arrives only after the harness process exits), so
// the append order is the call order and no two writers overlap.
func readBtwPromptLog(t *testing.T, path string) []string {
	t.Helper()
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, section := range strings.Split(string(blob), "<<<btw-prompt>>>\n") {
		if section != "" {
			out = append(out, strings.TrimSuffix(section, "\n"))
		}
	}
	return out
}

// The /btw follow-up memory over the real daemon surface: consecutive
// thread.btw calls on one thread each spawn a fresh agent-btw-<id> harness with
// the thread snapshot as its prompt, so the tests below go through the WS
// JSON-RPC wire, not Runner.Btw directly. The scripted harness logs the exact
// prompt it was handed and answers from the side question the prompt ends
// with, so each call returns a distinct scripted answer.
func TestBtwFollowUpCarriesPreviousExchangeThroughDaemon(t *testing.T) {
	promptLog := filepath.Join(t.TempDir(), "btw-prompts.log")
	t.Setenv("QA_BTW_PROMPT_LOG", promptLog)

	// The prompt rides as $1 so the script logs exactly what the harness
	// process saw. The side-question agent carries no AT_ tool environment, so
	// the script keys on the prompt's shape instead of any $AT_* variable.
	script := `case "$1" in
*"Side question:"*)
  printf '<<<btw-prompt>>>\n%s\n' "$1" >> "$QA_BTW_PROMPT_LOG"
  q=$(printf '%s\n' "$1" | awk '/^Side question:$/{getline; print; exit}')
  case "$q" in
  *ZUNIQ1*) printf '{"type":"text","text":"side-answer-one"}\n' ;;
  *ZUNIQ2*) printf '{"type":"text","text":"side-answer-two"}\n' ;;
  *)        printf '{"type":"text","text":"unexpected question: %s"}\n' "$q" ;;
  esac
  ;;
*)
  printf '{"type":"text","text":"not a side question"}\n'
  ;;
esac`
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script, "sh", spec.Prompt}, PromptVia: "arg"}, nil
	}, providers.JSONLineTranslator{})
	d, err := New(context.Background(), Options{
		StateDir: t.TempDir(),
		Token:    "secret",
		Registry: providers.NewRegistry(driver),
		Roles:    contracts.NewRoleRegistry(contracts.BuiltinRoles()...),
		Config:   orchestrator.Config{MaxPasses: 2, MaxConcurrency: 2},
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	t.Cleanup(d.Close)

	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "btw follow-ups"}), &created)
	if created.ThreadID == "" {
		t.Fatal("no thread id returned")
	}

	askBtw := func(id, question string) string {
		t.Helper()
		var reply struct {
			Answer string `json:"answer"`
		}
		json.Unmarshal(call(t, c, id, "thread.btw", map[string]any{
			"threadId": created.ThreadID,
			"question": question,
		}), &reply)
		return reply.Answer
	}

	const (
		q1 = "What does the ZUNIQ1 helper do?"
		q2 = "Where do the ZUNIQ2 tests live?"
	)
	if a := askBtw("2", q1); a != "side-answer-one" {
		t.Fatalf("first thread.btw answer = %q, want side-answer-one", a)
	}
	if a := askBtw("3", q2); a != "side-answer-two" {
		t.Fatalf("second thread.btw answer = %q, want side-answer-two", a)
	}

	prompts := readBtwPromptLog(t, promptLog)
	if len(prompts) != 2 {
		t.Fatalf("captured %d side-question prompts, want 2:\n%s", len(prompts), strings.Join(prompts, "\n---\n"))
	}
	first, second := prompts[0], prompts[1]

	// The first prompt is a fresh snapshot: its own question, no memory of an
	// exchange that had not happened yet.
	for _, want := range []string{"Side question:", q1} {
		if !strings.Contains(first, want) {
			t.Fatalf("first prompt missing %q:\n%s", want, first)
		}
	}
	if strings.Contains(first, "Previous side conversation:") {
		t.Fatalf("first prompt already carried a previous side conversation:\n%s", first)
	}
	if strings.Contains(first, "side-answer-one") {
		t.Fatalf("first prompt carried its own answer before the model answered:\n%s", first)
	}

	// The second prompt delivers the first exchange to the harness.
	for _, want := range []string{
		"Previous side conversation:",
		"Q: " + q1,
		"A: side-answer-one",
		"Side question:",
		q2,
	} {
		if !strings.Contains(second, want) {
			t.Fatalf("second prompt missing %q:\n%s", want, second)
		}
	}
}
