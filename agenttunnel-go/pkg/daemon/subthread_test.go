package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// postToolKind sends one `tool` call to the daemon's tool endpoint, as the
// `atd tool` binary does, and returns the status and body.
func postToolKind(t *testing.T, baseURL string, body map[string]any) (int, []byte) {
	t.Helper()
	data, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/tool/call", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// waitForReport waits on the parent's committed events until a mailbox message
// from the named sender lands. The failure bound is generous; the wait itself
// is on typed events, not a sleep.
func waitForReport(t *testing.T, events <-chan contracts.Event, from string) contracts.MailboxPosted {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev := <-events:
			if m, ok := ev.Payload.(contracts.MailboxPosted); ok && string(m.From) == from {
				return m
			}
		case <-deadline:
			t.Fatalf("no mailbox report from %q arrived", from)
		}
	}
}

// TestSubthreadRunsItsOwnTurnAndReportsToParentLead covers a subthread end to
// end: the lead's tool call creates a thread of its own in the parent's project,
// the subthread's orchestrator runs the task, and its summary lands in the
// parent's mailbox for the parent's lead. It also checks the two refusals an
// agent can hit: a subagent asking, and a subthread asking for another level.
func TestSubthreadRunsItsOwnTurnAndReportsToParentLead(t *testing.T) {
	d := newTestDaemon(t)
	c, base := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{
		"title": "parent", "projectId": "proj-sub",
	}), &created)
	parent := created.ThreadID

	events, unsub := d.Engine.Subscribe(parent)
	defer unsub()

	status, body := postToolKind(t, base, map[string]any{
		"threadId": parent, "agentId": "agent-orchestrator", "kind": "subthread",
		"task": "write the release notes",
	})
	if status != http.StatusOK {
		t.Fatalf("subthread call: status %d: %s", status, body)
	}
	var started struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(body, &started)
	child := started.ThreadID
	if child == "" || child == parent {
		t.Fatalf("subthread id = %q, want a new thread", child)
	}

	report := waitForReport(t, events, "subthread")
	if report.To != "agent-orchestrator" {
		t.Fatalf("report to %q, want the parent lead", report.To)
	}
	if !strings.Contains(report.Body, "finished") || !strings.Contains(report.Body, "report-") {
		t.Fatalf("report body = %q, want the subthread's own summary", report.Body)
	}

	ctx := context.Background()
	sub, err := d.Engine.State(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	if sub.ParentThreadID != parent {
		t.Fatalf("subthread parent = %q, want %q", sub.ParentThreadID, parent)
	}
	if sub.ProjectID != "proj-sub" {
		t.Fatalf("subthread project = %q, want the parent's project", sub.ProjectID)
	}
	if sub.Turn != 1 || len(sub.Messages) == 0 || sub.Messages[0].Text != "write the release notes" {
		t.Fatalf("subthread should run its task as its first turn, got turn %d messages %+v", sub.Turn, sub.Messages)
	}
	top, _ := d.Engine.State(ctx, parent)
	if len(top.Subthreads) != 1 || top.Subthreads[0] != child {
		t.Fatalf("parent subthreads = %v, want [%s]", top.Subthreads, child)
	}

	// A subthread cannot start its own subthread.
	if status, body := postToolKind(t, base, map[string]any{
		"threadId": child, "agentId": "agent-orchestrator", "kind": "subthread", "task": "nested",
	}); status != http.StatusBadRequest {
		t.Fatalf("nested subthread: status %d (%s), want 400", status, body)
	}
	// Only the lead starts subthreads; a subagent is refused outright.
	if status, body := postToolKind(t, base, map[string]any{
		"threadId": parent, "agentId": "agent-impl-1", "kind": "subthread", "task": "sneaky",
	}); status != http.StatusForbidden {
		t.Fatalf("subagent subthread: status %d (%s), want 403", status, body)
	}
}
