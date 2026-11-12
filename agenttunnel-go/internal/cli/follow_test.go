package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// scriptedServer is a minimal JSON-RPC daemon for the follower tests: it
// answers known methods and, when told to, pushes thread.event frames after a
// subscription. It never starts a real daemon and never touches disk.
type scriptedServer struct {
	mu         sync.Mutex
	results    map[string]any
	events     []threadFrame
	eventMu    sync.Mutex
	conns      map[*websocket.Conn]struct{}
	subscribed chan struct{}
	once       sync.Once
}

// threadFrame is one pushed frame: the event type and its payload.
type threadFrame struct {
	threadID string
	typ      contracts.EventType
	payload  any
}

func newScriptedServer() *scriptedServer {
	return &scriptedServer{
		results:    map[string]any{},
		conns:      map[*websocket.Conn]struct{}{},
		subscribed: make(chan struct{}),
	}
}

func (s *scriptedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	s.mu.Lock()
	s.conns[ws] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, ws)
		s.mu.Unlock()
	}()

	for {
		_, data, err := ws.Read(r.Context())
		if err != nil {
			return
		}
		var req struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(data, &req) != nil {
			continue
		}
		s.mu.Lock()
		result, ok := s.results[req.Method]
		s.mu.Unlock()
		if !ok {
			frame, _ := json.Marshal(map[string]any{
				"id": req.ID, "error": map[string]any{"code": 1, "message": "unsupported " + req.Method},
			})
			_ = ws.Write(r.Context(), websocket.MessageText, frame)
			continue
		}
		frame, _ := json.Marshal(map[string]any{"id": req.ID, "result": result})
		if err := ws.Write(r.Context(), websocket.MessageText, frame); err != nil {
			return
		}
		if req.Method == "thread.subscribe" {
			s.once.Do(func() { close(s.subscribed) })
		}
	}
}

// push writes every queued event to every connected client, then drains the
// queue. Callers wait for subscribed first so the frames are not sent before
// the client asked.
func (s *scriptedServer) push(t *testing.T) {
	t.Helper()
	select {
	case <-s.subscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("client never subscribed")
	}
	s.eventMu.Lock()
	frames := s.events
	s.events = nil
	s.eventMu.Unlock()

	s.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, frame := range frames {
		params, _ := json.Marshal(map[string]any{
			"threadId": frame.threadID, "type": frame.typ, "payload": frame.payload,
		})
		out, _ := json.Marshal(map[string]any{"method": "thread.event", "params": json.RawMessage(params)})
		for _, c := range conns {
			_ = c.Write(context.Background(), websocket.MessageText, out)
		}
	}
}

func (s *scriptedServer) queue(frame threadFrame) {
	s.eventMu.Lock()
	s.events = append(s.events, frame)
	s.eventMu.Unlock()
}

func dialScripted(t *testing.T, s *scriptedServer) *Client {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestFollowOutputPrintsLiveEventsUntilTheTurnEnds(t *testing.T) {
	s := newScriptedServer()
	s.results["agents.list"] = []map[string]any{
		{"agent": map[string]any{"id": "agent-orchestrator", "status": "running"}},
	}
	s.results["thread.subscribe"] = map[string]any{"subscribed": "th_1"}
	s.results["thread.logs"] = []contracts.LogEntry{}
	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentOutput, payload: contracts.AgentOutput{
		AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "planning 3 jobs",
	}})
	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentOutput, payload: contracts.AgentOutput{
		AgentID: "agent-orchestrator", Kind: contracts.LogTool, Text: "$ go test ./...",
	}})
	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentStatusChanged, payload: contracts.AgentStatusChanged{
		AgentID: "agent-orchestrator", Status: contracts.AgentSucceeded,
	}})

	c := dialScripted(t, s)
	env, out := newUsageTestEnv()
	row := threadListRow{ID: "th_1"}

	// Push only once the follower is listening; the test then waits for the
	// stream to end on the terminal status, never on a sleep.
	done := make(chan error, 1)
	go func() {
		done <- streamLogs(context.Background(), env, c, row, followOptions{lines: 10, follow: true})
	}()
	select {
	case <-s.subscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("follower never subscribed")
	}
	s.pushNow(t)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("streamLogs: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("follow did not end on the terminal status")
	}
	got := out.String()
	for _, want := range []string{"planning 3 jobs", "$ go test ./...", "status succeeded"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

// pushNow sends queued frames without waiting: the caller already waited.
func (s *scriptedServer) pushNow(t *testing.T) {
	t.Helper()
	s.eventMu.Lock()
	frames := s.events
	s.events = nil
	s.eventMu.Unlock()

	s.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, frame := range frames {
		params, _ := json.Marshal(map[string]any{
			"threadId": frame.threadID, "type": frame.typ, "payload": frame.payload,
		})
		out, _ := json.Marshal(map[string]any{"method": "thread.event", "params": json.RawMessage(params)})
		for _, c := range conns {
			_ = c.Write(context.Background(), websocket.MessageText, out)
		}
	}
}

func TestLogsFollowHonorsTheAgentFilter(t *testing.T) {
	s := newScriptedServer()
	s.results["agents.list"] = []map[string]any{
		{"agent": map[string]any{"id": "agent-exec", "status": "running"}},
	}
	s.results["thread.subscribe"] = map[string]any{"subscribed": "th_1"}
	s.results["agents.logs"] = []contracts.LogEntry{}
	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentOutput, payload: contracts.AgentOutput{
		AgentID: "agent-exec", Kind: contracts.LogText, Text: "from the executor",
	}})
	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentOutput, payload: contracts.AgentOutput{
		AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "from the lead",
	}})
	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentStatusChanged, payload: contracts.AgentStatusChanged{
		AgentID: "agent-exec", Status: contracts.AgentSucceeded,
	}})

	c := dialScripted(t, s)
	env, out := newUsageTestEnv()
	done := make(chan error, 1)
	go func() {
		done <- streamLogs(context.Background(), env, c, threadListRow{ID: "th_1"}, followOptions{
			lines: 10, follow: true, agent: "agent-exec",
		})
	}()
	select {
	case <-s.subscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("follower never subscribed")
	}
	s.pushNow(t)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("streamLogs: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("filtered follow did not end")
	}
	got := out.String()
	if !strings.Contains(got, "from the executor") {
		t.Fatalf("the filtered agent's line is missing:\n%s", got)
	}
	if strings.Contains(got, "from the lead") {
		t.Fatalf("another agent's line leaked through the filter:\n%s", got)
	}
}

func TestLogsFollowStopsOnContextCancel(t *testing.T) {
	s := newScriptedServer()
	s.results["agents.list"] = []map[string]any{
		{"agent": map[string]any{"id": "agent-orchestrator", "status": "running"}},
	}
	s.results["thread.subscribe"] = map[string]any{"subscribed": "th_1"}
	s.results["thread.logs"] = []contracts.LogEntry{}

	c := dialScripted(t, s)
	env, _ := newUsageTestEnv()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- streamLogs(ctx, env, c, threadListRow{ID: "th_1"}, followOptions{lines: 10, follow: true})
	}()
	select {
	case <-s.subscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("follower never subscribed")
	}
	cancel()
	select {
	case err := <-done:
		// A cancelled follow is a clean exit, not an error: Ctrl-C must leave
		// the shell usable.
		if err != nil {
			t.Fatalf("cancelled follow returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled follow did not return")
	}
}

// eventSourceCaller is a fake caller that also exposes buffered pushed frames,
// as the real Client does.
type eventSourceCaller struct {
	fakeCLICaller
	events chan Event
}

func (e *eventSourceCaller) Events() <-chan Event { return e.events }

func outputFrame(t *testing.T, threadID string, out contracts.AgentOutput) Event {
	t.Helper()
	payload, _ := json.Marshal(out)
	raw, _ := json.Marshal(map[string]any{
		"threadId": threadID, "type": string(contracts.EvtAgentOutput), "payload": json.RawMessage(payload),
	})
	return Event{Method: "thread.event", Params: raw}
}

// A repeated line from history is dropped only when it was already buffered at
// backlog time; the same text arriving later is new output and must print.
func TestBufferedFramesDropsOnlyBacklogRepeats(t *testing.T) {
	backlog := []contracts.LogEntry{{AgentID: "agent-exec", Kind: contracts.LogTool, Text: "$ go test ./..."}}
	source := &eventSourceCaller{events: make(chan Event, 8)}
	source.events <- outputFrame(t, "th_1", contracts.AgentOutput{AgentID: "agent-exec", Kind: contracts.LogTool, Text: "$ go test ./..."})
	source.events <- outputFrame(t, "th_1", contracts.AgentOutput{AgentID: "agent-exec", Kind: contracts.LogText, Text: "planning"})
	source.events <- outputFrame(t, "th_other", contracts.AgentOutput{AgentID: "agent-exec", Kind: contracts.LogTool, Text: "$ go test ./..."})

	early := bufferedFrames(source, "th_1", backlog)
	if len(early) != 2 {
		t.Fatalf("buffered frames kept = %d, want 2 (the repeat dropped, the others kept)", len(early))
	}

	// The same line arriving after the backlog is new output, not a repeat.
	env, out := newUsageTestEnv()
	tracker := newTurnTracker(map[string]contracts.AgentStatus{}, false)
	printFollowEvent(env, outputFrame(t, "th_1", contracts.AgentOutput{
		AgentID: "agent-exec", Kind: contracts.LogTool, Text: "$ go test ./...",
	}), "th_1", followOptions{}, tracker)
	if !strings.Contains(out.String(), "$ go test ./...") {
		t.Fatalf("a later repeat must print:\n%s", out.String())
	}
}

// attachTestServer is a scripted daemon with one running lead, so attach runs
// its full loop over a real Client.
func attachTestServer() *scriptedServer {
	s := newScriptedServer()
	s.results["agents.list"] = []map[string]any{
		{"agent": map[string]any{"id": "agent-orchestrator", "status": "running"}},
	}
	s.results["thread.subscribe"] = map[string]any{"subscribed": "th_1"}
	s.results["thread.logs"] = []contracts.LogEntry{}
	return s
}

func TestAttachEndsWhenTheTurnItWatchesEnds(t *testing.T) {
	s := attachTestServer()
	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentOutput, payload: contracts.AgentOutput{
		AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "reviewing the diff",
	}})
	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentStatusChanged, payload: contracts.AgentStatusChanged{
		AgentID: "agent-orchestrator", Status: contracts.AgentSucceeded,
	}})
	c := dialScripted(t, s)
	env, out := newUsageTestEnv()

	in, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	done := make(chan error, 1)
	go func() {
		done <- attachSession(context.Background(), env, c, threadListRow{ID: "th_1", Title: "job"}, in)
	}()
	select {
	case <-s.subscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("attach never subscribed")
	}
	s.pushNow(t)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("attachSession: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("attach did not leave when the turn ended")
	}
	got := out.String()
	for _, want := range []string{"reviewing the diff", "status succeeded", "-- turn finished --"} {
		if !strings.Contains(got, want) {
			t.Fatalf("attach output missing %q:\n%s", want, got)
		}
	}
}

func TestAttachCtrlCDetachesCleanly(t *testing.T) {
	s := attachTestServer()
	s.results["agents.list"] = []map[string]any{
		{"agent": map[string]any{"id": "agent-orchestrator", "status": "running"}},
	}
	c := dialScripted(t, s)
	env, out := newUsageTestEnv()

	// The input never closes: only the cancelled context ends the session.
	in, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- attachSession(ctx, env, c, threadListRow{ID: "th_1"}, in)
	}()
	select {
	case <-s.subscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("attach never subscribed")
	}
	cancel()

	select {
	case err := <-done:
		// Ctrl-C is a detach: the shell gets a clean exit and a note that the
		// turn is still running, never an error.
		if err != nil {
			t.Fatalf("Ctrl-C detach returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C did not detach")
	}
	if !strings.Contains(out.String(), "detached (the turn keeps running)") {
		t.Fatalf("detach note missing:\n%s", out.String())
	}
}
