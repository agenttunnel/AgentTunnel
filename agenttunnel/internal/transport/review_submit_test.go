package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func submitReviewRPC(t *testing.T, disp *Dispatcher, threadID string) error {
	t.Helper()
	params, err := json.Marshal(map[string]string{"threadId": threadID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = disp.Handle(context.Background(), Request{Method: "review.submit", Params: params})
	return err
}

func TestReviewSubmitRefusedWhileTurnRunning(t *testing.T) {
	eng, disp := newCommandFixture(t)
	ctx := context.Background()
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "r1", Type: contracts.CmdReviewCommentAdd, ThreadID: "t1",
		ReviewComment: &contracts.ReviewCommentAdded{ID: "r1", File: "a.go", Line: 1, Body: "fix"},
	}); err != nil {
		t.Fatal(err)
	}

	_, release, err := disp.beginTurn(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if err := submitReviewRPC(t, disp, "t1"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("err = %v, want a turn-already-running refusal", err)
	}
	// The refused submit must leave the comment pending and write no turn.
	st, _ := eng.State(ctx, "t1")
	if len(st.Messages) != 0 || st.ReviewComments[0].Submitted {
		t.Fatalf("refused submit recorded state: messages=%d submitted=%v", len(st.Messages), st.ReviewComments[0].Submitted)
	}
	release()
}

func TestReviewSubmitNeedsPendingComments(t *testing.T) {
	_, disp := newCommandFixture(t)
	if err := submitReviewRPC(t, disp, "t1"); err == nil || !strings.Contains(err.Error(), "no pending review comments") {
		t.Fatalf("err = %v, want no pending review comments", err)
	}
}
