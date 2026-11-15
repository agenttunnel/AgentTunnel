package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// A lead's steer is answered at once with its outcome and never reaches the
// tool inbox, so it cannot wait for the pass to end.
func TestHandleToolCallSteerReturnsQueuedOutcome(t *testing.T) {
	ctx := context.Background()
	eng, runner, srv := newMessageFixture(t)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c0", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := runner.Dispatch(ctx, "t1", []contracts.DispatchTask{{
		AgentID: "agent-hub", Name: "hub", Role: contracts.RoleExecutor, Task: "hub work",
	}}); err != nil {
		t.Fatalf("dispatch hub: %v", err)
	}

	rec := postToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"steer","to":"hub","text":"use the smaller fixture"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("steer: status=%d body=%q", rec.Code, rec.Body.String())
	}
	var reply struct {
		OK      bool   `json:"ok"`
		Outcome string `json:"outcome"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if !reply.OK || reply.Outcome != "queued" || !strings.Contains(reply.Message, "queued for hub's next run") || !strings.Contains(reply.Message, "not interrupted mid-run") {
		t.Fatalf("reply = %+v", reply)
	}
	if calls := srv.box.Drain("t1"); len(calls) != 0 {
		t.Fatalf("steer went through the tool inbox: %+v", calls)
	}
}

func TestHandleToolCallSteerRefusals(t *testing.T) {
	ctx := context.Background()
	eng, runner, srv := newMessageFixture(t)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c0", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := runner.Dispatch(ctx, "t1", []contracts.DispatchTask{{
		AgentID: "agent-hub", Name: "hub", Role: contracts.RoleExecutor, Task: "hub work",
	}}); err != nil {
		t.Fatalf("dispatch hub: %v", err)
	}

	cases := []struct {
		name string
		body string
		code int
	}{
		{"subagent cannot steer", `{"threadId":"t1","agentId":"agent-hub","kind":"steer","to":"hub","text":"x"}`, http.StatusForbidden},
		{"to and text required", `{"threadId":"t1","agentId":"agent-orchestrator","kind":"steer","text":"x"}`, http.StatusBadRequest},
		{"unknown agent", `{"threadId":"t1","agentId":"agent-orchestrator","kind":"steer","to":"ghost","text":"x"}`, http.StatusBadRequest},
		{"lead is not steerable", `{"threadId":"t1","agentId":"agent-orchestrator","kind":"steer","to":"orchestrator","text":"x"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if rec := postToolCall(t, srv, tc.body); rec.Code != tc.code {
			t.Fatalf("%s: status=%d body=%q, want %d", tc.name, rec.Code, rec.Body.String(), tc.code)
		}
	}
}
