package transport

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// kind=revive rides the generic queued fall-through rather than an at-once
// handler, so the posted Call must keep every field — To included. The
// toolcli payload already sends "to"; a field the fall-through forgets is
// lost on the real CLI path while orchestrator-level tests that post
// toolbox.Call directly stay green.
func TestToolCallReviveForwardsTargetIntact(t *testing.T) {
	ctx := context.Background()
	eng, _, srv := newMessageFixture(t)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c0", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"revive",`+
		`"name":"scratch","role":"executor","lane":"main","task":"ignored","harness":"opencode","model":"m1",`+
		`"dependsOn":["a"],"text":"which file did you touch?","to":"hub"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("revive call = %d: %s", rec.Code, rec.Body.String())
	}

	calls := srv.box.Drain("t1")
	if len(calls) != 1 {
		t.Fatalf("queued calls = %+v, want one revive", calls)
	}
	got := calls[0]
	want := toolbox.Call{
		Kind:      toolbox.KindRevive,
		AgentID:   "agent-orchestrator",
		Name:      "scratch",
		Lane:      "main",
		Role:      contracts.RoleExecutor,
		Task:      "ignored",
		Harness:   "opencode",
		Model:     "m1",
		DependsOn: []string{"a"},
		Text:      "which file did you touch?",
		To:        "hub",
	}
	// Field-for-field, not ==: a Call carries a slice, which structs holding
	// one may not be compared with.
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("queued call = %+v, want %+v", got, want)
	}
}
