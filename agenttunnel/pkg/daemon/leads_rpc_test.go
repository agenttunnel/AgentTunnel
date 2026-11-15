package daemon

import (
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// TestSetLeadsRPCPersistsTeamAndEnablesMailbox covers thread.setLeads end to
// end over the daemon's WebSocket surface, the same path the desktop team
// picker and headless clients use. A team needs peer messaging to agree, so
// enabling a team turns the mailbox on even after the user opted out.
func TestSetLeadsRPCPersistsTeamAndEnablesMailbox(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "team"}), &created)

	var ok struct {
		OK bool `json:"ok"`
	}
	json.Unmarshal(call(t, c, "2", "thread.setPeerMailbox", map[string]any{
		"threadId": created.ThreadID, "enabled": false,
	}), &ok)

	json.Unmarshal(call(t, c, "3", "thread.setLeads", map[string]any{
		"threadId": created.ThreadID, "count": 2,
	}), &ok)
	if !ok.OK {
		t.Fatal("setLeads should acknowledge")
	}

	var view struct {
		Leads       int  `json:"leads"`
		PeerMailbox bool `json:"peerMailbox"`
	}
	json.Unmarshal(call(t, c, "4", "thread.get", map[string]any{"threadId": created.ThreadID}), &view)
	if view.Leads != 2 {
		t.Fatalf("leads = %d, want 2", view.Leads)
	}
	if !view.PeerMailbox {
		t.Fatal("a team of leads must turn the peer mailbox on")
	}
}
