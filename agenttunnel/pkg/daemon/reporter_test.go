package daemon

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// Reporter mode is wired end to end: the RPC persists the flag, and once
// WhatsApp connectivity is established the master's status report of the
// threads reaches the linked device through the bridge's ordered worker.
func TestDaemonReporterSendsMasterReportOnConnect(t *testing.T) {
	adapter := &fakeRemoteAdapter{sentCh: make(chan string, 16)}
	d := newTestDaemonWith(t, Options{RemoteAdapter: adapter})
	c, _ := dialWS(t, d)

	// A project with a thread, so the report has something to say.
	dir := t.TempDir()
	var opened map[string]any
	if err := json.Unmarshal(call(t, c, "1", "thread.open", map[string]any{"dir": dir}), &opened); err != nil || opened["threadId"] == nil {
		t.Fatalf("thread.open reply: %v %v", err, opened)
	}

	// Turn reporter on while the bridge is disconnected: no connect event has
	// happened, so nothing is sent. Connect's callbacks run before the RPC
	// replies, so the reply is the barrier after which a report could only
	// exist if the wiring had fired on the wrong event.
	params := map[string]any{"enabled": true}
	var state transport.ControlState
	if err := json.Unmarshal(call(t, c, "2", "master.reporter.set", params), &state); err != nil {
		t.Fatalf("decode master.reporter.set: %v", err)
	}
	if !state.Enabled {
		t.Fatal("reporter did not turn on")
	}
	select {
	case text := <-adapter.sentCh:
		t.Fatalf("report fired before any connect event: %q", text)
	default:
	}

	// The connect event is the trigger: the master's report follows through
	// the bridge (the enqueue happens inside the connect callbacks, before the
	// RPC reply, so awaiting the send is race-free).
	call(t, c, "3", "remote.whatsapp.connect", nil)

	select {
	case report := <-adapter.sentCh:
		if !strings.Contains(report, "AgentTunnel status") {
			t.Fatalf("report = %q, want the master status report", report)
		}
		// The report surfaces the thread that was opened, not the no-threads
		// fallback, so the master snapshot really fed the bridge.
		if strings.Contains(report, "no threads") || !strings.Contains(report, filepath.Base(dir)) {
			t.Fatalf("report = %q, want it to list the opened project", report)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the reporter report never arrived")
	}
}
