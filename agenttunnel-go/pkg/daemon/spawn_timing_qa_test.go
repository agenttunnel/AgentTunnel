package daemon

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// readCapturedPrompts reads a prompt log written by the script below: each
// section starts with a <<<thread|pass>>> marker line and holds the full lead
// pass prompt that followed it.
func readCapturedPrompts(t *testing.T, path string) map[string]string {
	t.Helper()
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	key := ""
	buf := &strings.Builder{}
	flush := func() {
		if key != "" {
			out[key] = buf.String()
		}
	}
	for _, line := range strings.Split(string(blob), "\n") {
		if strings.HasPrefix(line, "<<<") && strings.HasSuffix(line, ">>>") {
			flush()
			key = strings.TrimSuffix(strings.TrimPrefix(line, "<<<"), ">>>")
			buf = &strings.Builder{}
			continue
		}
		if key != "" {
			buf.WriteString(line)
			buf.WriteString("\n")
		}
	}
	flush()
	return out
}

// The full approval round trip over the real agent-facing path, pinning the
// pass-by-pass timing with the shipped default budget of 3 passes: the
// executor proposes `tool spawn` mid-wave over HTTP /tool/call, the request is
// invisible in pass 0's prompt, appears in pass 1's prompt (with the requester
// and the approve command), the lead approves during pass 1, the child runs in
// wave 1 parented on the executor, and pass 2 still happens to summarize.
func TestSpawnProposalSurfacesInNextPromptAndRunsInNextWave(t *testing.T) {
	promptLog := filepath.Join(t.TempDir(), "prompts.log")
	script := `case "$AT_ROLE" in
orchestrator)
  printf '<<<%s|%s>>>\n%s\n' "$AT_THREAD_ID" "$AT_PASS" "$1" >> "$QA_PROMPT_LOG"
  case "$AT_PASS" in
  0) "$AT_TOOL" tool dispatch --name outer --task "outer work" >/dev/null ;;
  1) "$AT_TOOL" tool approve --name inner >/dev/null ;;
  esac
  printf '{"type":"text","text":"report-%s"}\n' "$AT_PASS"
  ;;
*)
  if [ "$AT_TASK" = "outer work" ]; then
    "$AT_TOOL" tool spawn --name inner --task "inner work" >/dev/null
  fi
  printf '{"type":"text","text":"%s findings"}\n' "$AT_AGENT_ID"
  ;;
esac`

	// Agents need a real binary that understands `tool …`; build one.
	toolBin := filepath.Join(t.TempDir(), "atd")
	build := exec.Command("go", "build", "-o", toolBin, "github.com/agenttunnel/agenttunnel-go/cmd/atd")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build atd: %v\n%s", err, out)
	}

	// The prompt rides as $1 so the lead script can log exactly what it saw.
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script, "sh", spec.Prompt}, PromptVia: "arg"}, nil
	}, providers.JSONLineTranslator{})
	d, err := New(context.Background(), Options{
		StateDir: t.TempDir(),
		Token:    "secret",
		Registry: providers.NewRegistry(driver),
		ToolPath: toolBin,
		Config:   orchestrator.Config{MaxPasses: 3, MaxConcurrency: 2},
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	defer d.Close()

	srv := httptest.NewServer(d.Server.Handler())
	defer srv.Close()
	d.SetEndpoint(srv.URL)
	t.Setenv("QA_PROMPT_LOG", promptLog)

	ctx := context.Background()
	if err := d.Engine.Dispatch(ctx, contracts.Command{ID: "c", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Orch.RunTurn(ctx, "t1", "please build the feature"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	prompts := readCapturedPrompts(t, promptLog)
	if len(prompts) != 3 {
		t.Fatalf("expected prompts from passes 0-2, got %d: %+v", len(prompts), prompts)
	}
	// The request was posted mid-wave-0, after pass 0's prompt was already
	// built: it must not have been visible yet.
	if strings.Contains(prompts["t1|0"], "Pending spawn requests") {
		t.Fatalf("pass 0 prompt already carried the request:\n%s", prompts["t1|0"])
	}
	// Pass 1 is where the lead first sees it.
	pass1 := prompts["t1|1"]
	for _, want := range []string{
		"## Pending spawn requests",
		"inner", "inner work", // the request itself
		"agent-outer-1", // the executor that proposed it
		"tool approve --name",
	} {
		if !strings.Contains(pass1, want) {
			t.Fatalf("pass 1 prompt missing %q:\n%s", want, pass1)
		}
	}
	// The approval consumed the request before the summary pass.
	if strings.Contains(prompts["t1|2"], "Pending spawn requests") {
		t.Fatalf("pass 2 prompt still carried a consumed request:\n%s", prompts["t1|2"])
	}

	state, _ := d.Engine.State(ctx, "t1")
	inner := state.Agents["agent-inner-2"]
	if inner.Status != contracts.AgentSucceeded {
		t.Fatalf("inner status = %s, agents = %+v", inner.Status, state.Agents)
	}
	if inner.ParentAgentID != "agent-outer-1" {
		t.Fatalf("inner parent = %q, want the executor agent-outer-1", inner.ParentAgentID)
	}
	// The approve in pass 1 matched a pending request; a timing regression
	// would have dead-lettered it as a note instead.
	for _, note := range d.Runner.Notebook().Get("notes") {
		if strings.Contains(note.Value, "No pending spawn request") {
			t.Fatalf("unexpected decision note: %+v", note)
		}
	}
	// The summary pass (pass 2) produced the turn's final message, so the
	// whole round trip fit the default budget.
	found := false
	for _, m := range state.Messages {
		if m.Author == "orchestrator" && m.Text == "report-2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("orchestrator final message missing: %+v", state.Messages)
	}
}
