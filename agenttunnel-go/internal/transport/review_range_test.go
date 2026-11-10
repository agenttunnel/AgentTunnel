package transport

import (
	"context"
	"encoding/json"
	"testing"
)

func TestReviewCommentRPCKeepsRangeAndSide(t *testing.T) {
	eng, disp := newCommandFixture(t)
	params, err := json.Marshal(map[string]any{
		"threadId": "t1", "file": "a.go", "line": 3, "endLine": 6, "side": "old", "body": "drop these",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disp.Handle(context.Background(), Request{Method: "review.comment", Params: params}); err != nil {
		t.Fatalf("review.comment: %v", err)
	}
	st, _ := eng.State(context.Background(), "t1")
	if len(st.ReviewComments) != 1 {
		t.Fatalf("stored %d comments, want 1", len(st.ReviewComments))
	}
	c := st.ReviewComments[0]
	if c.Line != 3 || c.EndLine != 6 || c.Side != "old" {
		t.Fatalf("stored range = %d-%d side=%q, want 3-6 old", c.Line, c.EndLine, c.Side)
	}
}
