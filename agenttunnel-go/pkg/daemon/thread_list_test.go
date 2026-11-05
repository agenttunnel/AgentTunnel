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

func TestThreadListReportsStaleness(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	now := time.Unix(1_700_000_000, 0).UTC()
	d.Engine.SetClock(func() time.Time { return now })

	var fresh, idle struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "fresh"}), &fresh)
	call(t, c, "2", "settings.set", map[string]any{"staleAfterMinutes": 30})

	var list []struct {
		Title             string    `json:"title"`
		RunningAgents     int       `json:"runningAgents"`
		StaleAfterSeconds int       `json:"staleAfterSeconds"`
		Stale             bool      `json:"stale"`
		UpdatedAt         time.Time `json:"updatedAt"`
	}
	json.Unmarshal(call(t, c, "3", "thread.list", nil), &list)
	if len(list) != 1 {
		t.Fatalf("threads = %+v", list)
	}
	if list[0].StaleAfterSeconds != 1800 {
		t.Fatalf("staleAfterSeconds = %d, want 1800", list[0].StaleAfterSeconds)
	}

	// The only thread is fresh until the clock passes the threshold.
	now = now.Add(29 * time.Minute)
	json.Unmarshal(call(t, c, "4", "thread.list", nil), &list)
	if list[0].Stale {
		t.Fatalf("thread marked stale at 29 minutes")
	}

	// Mark a second thread, then advance past the threshold and check both:
	// the touched one stays fresh, the untouched one goes stale.
	json.Unmarshal(call(t, c, "5", "thread.create", map[string]any{"title": "touched"}), &idle)
	now = now.Add(2 * time.Minute)
	call(t, c, "6", "thread.rename", map[string]any{
		"threadId": idle.ThreadID, "title": "touched renamed",
	})
	json.Unmarshal(call(t, c, "7", "thread.list", nil), &list)
	stale := map[string]bool{}
	for _, row := range list {
		stale[row.Title] = row.Stale
	}
	if !stale["fresh"] {
		t.Fatalf("31-minute-old thread should be stale: %+v", list)
	}
	if stale["touched renamed"] {
		t.Fatalf("just-touched thread should not be stale: %+v", list)
	}
}
