package daemon

import (
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestThreadDeleteRemovesItFromListings(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "doomed"}), &created)

	var threads []map[string]any
	json.Unmarshal(call(t, c, "2", "thread.list", nil), &threads)
	if len(threads) != 1 {
		t.Fatalf("threads = %+v, want 1", threads)
	}

	call(t, c, "3", "thread.delete", map[string]any{"threadId": created.ThreadID})

	json.Unmarshal(call(t, c, "4", "thread.list", nil), &threads)
	if len(threads) != 0 {
		t.Fatalf("threads after delete = %+v, want none", threads)
	}

	// Deleting a tombstoned thread fails; it is already gone.
	if _, errResp := rawCall(t, c, "5", "thread.delete", map[string]any{"threadId": created.ThreadID}); errResp == nil {
		t.Fatal("second delete should fail")
	}
}

func TestThreadRenameRoundTrip(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "old"}), &created)

	call(t, c, "2", "thread.rename", map[string]any{
		"threadId": created.ThreadID, "title": "new title",
	})

	var list []struct {
		ID    contracts.ThreadID `json:"id"`
		Title string             `json:"title"`
	}
	json.Unmarshal(call(t, c, "3", "thread.list", nil), &list)
	if len(list) != 1 || list[0].Title != "new title" {
		t.Fatalf("threads = %+v, want renamed title", list)
	}
}
