package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestThreadContextViewEditAndDelete(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "context"}), &created)

	nb := d.Runner.Notebook(created.ThreadID)
	nb.Set("context", "summary v1", "agent-context-1")
	nb.Append("notes", "first note", "orchestrator")
	nb.Append("notes", "second note", "orchestrator")

	type view struct {
		Keys []struct {
			Key     string `json:"key"`
			Entries []struct {
				Index  int    `json:"index"`
				Author string `json:"author"`
				Value  string `json:"value"`
			} `json:"entries"`
		} `json:"keys"`
	}
	var got view
	json.Unmarshal(call(t, c, "2", "thread.context.get", map[string]any{"threadId": created.ThreadID}), &got)
	if len(got.Keys) != 2 || got.Keys[0].Key != "context" || got.Keys[1].Key != "notes" {
		t.Fatalf("keys = %+v, want context then notes", got.Keys)
	}
	if got.Keys[1].Entries[1].Index != 1 || got.Keys[1].Entries[1].Value != "second note" {
		t.Fatalf("notes = %+v", got.Keys[1].Entries)
	}

	call(t, c, "3", "thread.context.entry.save", map[string]any{
		"threadId": created.ThreadID, "key": "notes", "index": 1, "value": "second note, corrected",
	})
	call(t, c, "4", "thread.context.entry.delete", map[string]any{
		"threadId": created.ThreadID, "key": "context", "index": 0,
	})

	json.Unmarshal(call(t, c, "5", "thread.context.get", map[string]any{"threadId": created.ThreadID}), &got)
	if len(got.Keys) != 1 || got.Keys[0].Key != "notes" {
		t.Fatalf("after delete keys = %+v, want only notes", got.Keys)
	}
	edited := got.Keys[0].Entries[1]
	if edited.Value != "second note, corrected" || edited.Author != contextstore.UserAuthor {
		t.Fatalf("edited entry = %+v, want the corrected value by the user", edited)
	}
	// The orchestrator reads the same notebook on its next dispatch.
	if rendered := nb.Render(); !strings.Contains(rendered, "second note, corrected") || strings.Contains(rendered, "summary v1") {
		t.Fatalf("rendered context = %q", rendered)
	}
}
