package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestThreadListSortsByLastUpdated(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	now := time.Unix(1_700_000_000, 0).UTC()
	d.Engine.SetClock(func() time.Time { return now })

	var older, newer struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "older"}), &older)
	now = now.Add(time.Minute)
	json.Unmarshal(call(t, c, "2", "thread.create", map[string]any{"title": "newer"}), &newer)

	// Touching the older thread makes it the most recently updated one.
	now = now.Add(time.Minute)
	call(t, c, "3", "thread.rename", map[string]any{
		"threadId": older.ThreadID, "title": "older updated",
	})

	var list []struct {
		ID        contracts.ThreadID `json:"id"`
		Title     string             `json:"title"`
		UpdatedAt time.Time          `json:"updatedAt"`
	}
	json.Unmarshal(call(t, c, "4", "thread.list", nil), &list)
	if len(list) != 2 {
		t.Fatalf("threads = %+v, want 2", list)
	}
	if list[0].ID != older.ThreadID || list[1].ID != newer.ThreadID {
		t.Fatalf("thread order = [%s, %s], want older updated first", list[0].Title, list[1].Title)
	}
	if !list[0].UpdatedAt.Equal(now) {
		t.Fatalf("older updatedAt = %s, want %s", list[0].UpdatedAt, now)
	}
	if want := now.Add(-time.Minute); !list[1].UpdatedAt.Equal(want) {
		t.Fatalf("newer updatedAt = %s, want %s", list[1].UpdatedAt, want)
	}
}
