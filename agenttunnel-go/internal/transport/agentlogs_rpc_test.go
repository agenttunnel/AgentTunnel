package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/files"
)

func TestAgentLogsGates(t *testing.T) {
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

	// A subagent's call is refused, as is an anonymous one.
	for _, agentID := range []string{"agent-hub", ""} {
		body := `{"threadId":"t1","agentId":"` + agentID + `","kind":"agent-logs","to":"hub"}`
		if rec := postLocalToolCall(t, srv, body); rec.Code != http.StatusForbidden {
			t.Fatalf("subagent call (%q) = %d, want 403", agentID, rec.Code)
		}
	}
	// The agent id is self-declared, so the peer decides: a remote lead is
	// refused too.
	if rec := postToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"agent-logs","to":"hub"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("remote call = %d, want 403", rec.Code)
	}

	cases := []struct {
		name string
		body string
		want string
	}{
		{"blank to", `{"threadId":"t1","agentId":"agent-orchestrator","kind":"agent-logs","to":"  "}`, "to is required"},
		{"unknown agent", `{"threadId":"t1","agentId":"agent-orchestrator","kind":"agent-logs","to":"ghost"}`, "unknown agent ghost"},
		{"unknown thread", `{"threadId":"t9","agentId":"agent-orchestrator","kind":"agent-logs","to":"hub"}`, "unknown thread t9"},
	}
	for _, tc := range cases {
		rec := postLocalToolCall(t, srv, tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s: status=%d body=%q, want 400 mentioning %q", tc.name, rec.Code, rec.Body.String(), tc.want)
		}
	}
}

func TestAgentLogsReturnsTargetAgentsLines(t *testing.T) {
	ctx := context.Background()
	eng, runner, srv := newMessageFixture(t)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c0", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := runner.Dispatch(ctx, "t1", []contracts.DispatchTask{
		{AgentID: "agent-hub", Name: "hub", Role: contracts.RoleExecutor, Task: "hub work"},
		{AgentID: "agent-spoke", Name: "spoke", Role: contracts.RoleExecutor, Task: "spoke work"},
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	logs := runner.Logs()
	now := time.Unix(0, 0).UTC()
	for _, e := range []contracts.LogEntry{
		{AgentID: "agent-hub", Kind: contracts.LogText, Text: "hub line one", At: now},
		{AgentID: "agent-spoke", Kind: contracts.LogText, Text: "spoke line one", At: now},
		{AgentID: "agent-hub", Kind: contracts.LogTool, Text: "$ go test ./...", At: now},
		{AgentID: "agent-hub", Kind: contracts.LogError, Text: "boom", At: now},
		{AgentID: "agent-spoke", Kind: contracts.LogText, Text: "spoke line two", At: now},
	} {
		if err := logs.Append("t1", e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"agent-logs","to":"hub"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent-logs = %d: %s", rec.Code, rec.Body.String())
	}
	var reply struct {
		OK        bool   `json:"ok"`
		Agent     string `json:"agent"`
		Name      string `json:"name"`
		Lines     int    `json:"lines"`
		Truncated bool   `json:"truncated"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v\n%s", err, rec.Body.String())
	}
	if !reply.OK || reply.Agent != "agent-hub" || reply.Name != "hub" || reply.Lines != 3 || reply.Truncated {
		t.Fatalf("reply = %+v, want hub agent, 3 lines, not truncated", reply)
	}
	for _, want := range []string{"hub line one", "[tool] $ go test ./...", "[error] boom"} {
		if !strings.Contains(reply.Text, want) {
			t.Fatalf("reply text lacks %q:\n%s", want, reply.Text)
		}
	}
	if strings.Contains(reply.Text, "spoke") {
		t.Fatalf("reply text carries another agent's lines:\n%s", reply.Text)
	}

	// An agent id selects that agent, and the entries keep file order.
	rec = postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"agent-logs","to":"agent-spoke"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent-logs by id = %d: %s", rec.Code, rec.Body.String())
	}
	reply = struct {
		OK        bool   `json:"ok"`
		Agent     string `json:"agent"`
		Name      string `json:"name"`
		Lines     int    `json:"lines"`
		Truncated bool   `json:"truncated"`
		Text      string `json:"text"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.Agent != "agent-spoke" || reply.Name != "spoke" || reply.Lines != 2 {
		t.Fatalf("reply = %+v, want spoke agent, 2 lines", reply)
	}
	if !strings.Contains(reply.Text, "spoke line one\n") || strings.Index(reply.Text, "spoke line one") > strings.Index(reply.Text, "spoke line two") {
		t.Fatalf("spoke text is not oldest first:\n%s", reply.Text)
	}
}

// A repeated roster name resolves to the newest agent carrying it, running or
// terminal: the latest run is the one whose log the lead wants.
func TestAgentLogsResolvesRepeatedNameToNewestRun(t *testing.T) {
	ctx := context.Background()
	eng, runner, srv := newMessageFixture(t)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c0", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := runner.Dispatch(ctx, "t1", []contracts.DispatchTask{
		{AgentID: "agent-hub", Name: "hub", Role: contracts.RoleExecutor, Task: "hub work"},
	}); err != nil {
		t.Fatalf("dispatch hub: %v", err)
	}
	if err := runner.Dispatch(ctx, "t1", []contracts.DispatchTask{
		{AgentID: "agent-hub2", Name: "hub", Role: contracts.RoleExecutor, Task: "hub work again"},
	}); err != nil {
		t.Fatalf("dispatch hub again: %v", err)
	}
	logs := runner.Logs()
	now := time.Unix(0, 0).UTC()
	_ = logs.Append("t1", contracts.LogEntry{AgentID: "agent-hub", Kind: contracts.LogText, Text: "first run", At: now})
	_ = logs.Append("t1", contracts.LogEntry{AgentID: "agent-hub2", Kind: contracts.LogText, Text: "second run", At: now})

	rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"agent-logs","to":"hub"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent-logs = %d: %s", rec.Code, rec.Body.String())
	}
	var reply struct {
		Agent string `json:"agent"`
		Lines int    `json:"lines"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.Agent != "agent-hub2" || reply.Lines != 1 || reply.Text != "second run\n" {
		t.Fatalf("reply = %+v, want the newest hub run only", reply)
	}
}

// Over the read cap the rendering keeps the newest lines and drops whole
// leading ones, saying so on the first line.
func TestRenderAgentLogKeepsNewestLinesOverCap(t *testing.T) {
	var entries []contracts.LogEntry
	now := time.Unix(0, 0).UTC()
	for i := 0; i < 300; i++ {
		entries = append(entries, contracts.LogEntry{
			AgentID: "agent-a", Kind: contracts.LogText,
			Text: fmt.Sprintf("%06d %s", i, strings.Repeat("x", 1000)), At: now,
		})
	}
	text, truncated := renderAgentLog(entries)
	if !truncated {
		t.Fatal("a log over the cap was not marked truncated")
	}
	if !strings.HasPrefix(text, "(older lines truncated)\n") {
		t.Fatalf("truncated log lacks its note:\n%.80s", text)
	}
	if !strings.Contains(text, "000299") {
		t.Fatal("the newest line was dropped")
	}
	if strings.Contains(text, "000000") {
		t.Fatal("an old line survived the cap")
	}
	if len(text) > files.MaxReadBytes {
		t.Fatalf("truncated log is %d bytes, over the %d cap", len(text), files.MaxReadBytes)
	}

	// Under the cap the log is whole, oldest first, one entry per line.
	small := renderLogWithKinds()
	text, truncated = renderAgentLog(small)
	if truncated || text != "plain\n[tool] $ go test ./...\n[error] boom\n[reasoning] hmm\n[raw] noise\n[raw] usage line\n" {
		t.Fatalf("render = %q truncated=%v", text, truncated)
	}
}

func renderLogWithKinds() []contracts.LogEntry {
	now := time.Unix(0, 0).UTC()
	return []contracts.LogEntry{
		{AgentID: "agent-a", Kind: contracts.LogText, Text: "plain", At: now},
		{AgentID: "agent-a", Kind: contracts.LogTool, Text: "$ go test ./...", At: now},
		{AgentID: "agent-a", Kind: contracts.LogError, Text: "boom", At: now},
		{AgentID: "agent-a", Kind: contracts.LogReasoning, Text: "hmm", At: now},
		{AgentID: "agent-a", Kind: contracts.LogRaw, Text: "noise", At: now},
		{AgentID: "agent-a", Kind: contracts.LogUsage, Text: "usage line", At: now},
	}
}
