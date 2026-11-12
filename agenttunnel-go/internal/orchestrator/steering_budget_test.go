package orchestrator

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// passOf reads the AT_PASS marker the runner sets on every orchestrator spawn.
func passOf(spec providers.SessionSpec) int {
	for _, kv := range spec.Env {
		if v, ok := strings.CutPrefix(kv, "AT_PASS="); ok {
			n, _ := strconv.Atoi(v)
			return n
		}
	}
	return -1
}

// A steer that lands while the last pass budgeted for a turn is running must
// still be weighed by that turn, not left queued for the next one.
func TestSteerPostedDuringLastPassIsWeighedBeforeTurnEnds(t *testing.T) {
	const script = `if [ "$AT_PASS" = "0" ]; then
  printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","taskId":"impl","task":"implement the feature","role":"executor"}'
fi
printf '{"type":"text","text":"report-%s"}\n' "$AT_PASS"
printf '{"usage":{"input_tokens":1,"output_tokens":1}}\n'`

	const steer = "drop the parser work"
	prompts := map[int]string{}
	var r *Runner
	var eng *domain.Engine
	var ctx context.Context
	r, eng, ctx = newHarnessInvoking(t, func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		if spec.Role == contracts.RoleOrchestrator {
			pass := passOf(spec)
			prompts[pass] = spec.Prompt
			if pass == 1 {
				// The user steers while the team's second pass is running.
				_ = eng.Dispatch(ctx, contracts.Command{
					ID: "late", Type: contracts.CmdMessageAppend, ThreadID: "t1",
					Message: &contracts.MessageSent{MessageID: "steer-late", Author: "steering", Text: steer},
				})
				r.steering.Post("t1", "steer-late", steer)
			}
		}
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script}, PromptVia: "stdin"}, nil
	})
	if err := New(r).RunTurn(ctx, "t1", "please build the feature"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	if r.HasSteering("t1") {
		t.Fatal("steering was left queued after the turn ended; it would surface in an unrelated later turn")
	}
	if !strings.Contains(prompts[2], steer) {
		t.Fatalf("the steer posted during the last pass was never weighed in this turn; pass 2 prompt:\n%s", prompts[2])
	}
}
