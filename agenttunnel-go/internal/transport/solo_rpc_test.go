package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// setSoloRPC calls thread.setSolo. An empty harness clears solo mode.
func setSoloRPC(t *testing.T, disp *Dispatcher, threadID, harness, model, variant string) error {
	t.Helper()
	params, err := json.Marshal(map[string]string{
		"threadId": threadID, "harness": harness, "model": model, "variant": variant,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = disp.Handle(context.Background(), Request{Method: "thread.setSolo", Params: params})
	return err
}

// threadGetRPC fetches one thread's state as marshaled wire bytes, so a test
// can assert on literal JSON keys a struct-level decode cannot see.
func threadGetRPC(t *testing.T, disp *Dispatcher, threadID string) []byte {
	t.Helper()
	res, err := disp.Handle(context.Background(), Request{Method: "thread.get", Params: mustJSON(t, map[string]string{"threadId": threadID})})
	if err != nil {
		t.Fatalf("thread.get: %v", err)
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Setting then clearing solo mode round-trips through thread.get, and the
// wire key appears exactly when the runtime is set: omitempty must never
// leave a null soloRuntime behind.
func TestSetSoloRoundTripsThroughThreadGet(t *testing.T) {
	_, disp := newCommandFixture(t)

	if err := setSoloRPC(t, disp, "t1", "claude", "sonnet", "code"); err != nil {
		t.Fatalf("setSolo: %v", err)
	}
	data := threadGetRPC(t, disp, "t1")
	if !strings.Contains(string(data), `"soloRuntime"`) {
		t.Fatal("thread.get wire bytes must carry soloRuntime when set")
	}
	var got struct {
		SoloRuntime *contracts.LeadRuntime `json:"soloRuntime"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.SoloRuntime == nil || got.SoloRuntime.Harness != contracts.HarnessClaude ||
		got.SoloRuntime.Model != "sonnet" || got.SoloRuntime.Variant != "code" {
		t.Fatalf("soloRuntime = %+v, want claude/sonnet/code", got.SoloRuntime)
	}

	// Clearing with an empty harness removes the key entirely.
	if err := setSoloRPC(t, disp, "t1", "", "", ""); err != nil {
		t.Fatalf("clearSolo: %v", err)
	}
	data = threadGetRPC(t, disp, "t1")
	if strings.Contains(string(data), `"soloRuntime"`) {
		t.Fatalf("cleared thread still carries soloRuntime: %s", data)
	}
	var cleared struct {
		SoloRuntime *contracts.LeadRuntime `json:"soloRuntime"`
	}
	if err := json.Unmarshal(data, &cleared); err != nil {
		t.Fatal(err)
	}
	if cleared.SoloRuntime != nil {
		t.Fatalf("soloRuntime after clear = %+v, want nil", cleared.SoloRuntime)
	}
}

// thread.list rows carry soloRuntime only on solo threads, and never as null.
func TestThreadListCarriesSoloRuntimeOnlyOnSoloThreads(t *testing.T) {
	eng, disp := newCommandFixture(t)
	ctx := context.Background()
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2", Title: "team"}); err != nil {
		t.Fatal(err)
	}
	if err := setSoloRPC(t, disp, "t1", "opencode", "glm", ""); err != nil {
		t.Fatalf("setSolo: %v", err)
	}

	res, err := disp.Handle(ctx, Request{ID: "1", Method: "thread.list"})
	if err != nil {
		t.Fatalf("thread.list: %v", err)
	}
	list, ok := res.([]ThreadSummary)
	if !ok {
		t.Fatalf("thread.list result = %T, want []ThreadSummary", res)
	}
	byID := make(map[contracts.ThreadID]ThreadSummary, len(list))
	for _, s := range list {
		byID[s.ID] = s
	}
	if rt := byID["t1"].SoloRuntime; rt == nil || rt.Harness != contracts.HarnessOpenCode || rt.Model != "glm" {
		t.Fatalf("t1 soloRuntime = %+v, want opencode/glm", byID["t1"].SoloRuntime)
	}
	if byID["t2"].SoloRuntime != nil {
		t.Fatalf("t2 soloRuntime = %+v, want nil", byID["t2"].SoloRuntime)
	}

	// Wire probe: the key rides the solo row and is absent from the other,
	// never serialized as null.
	data, _ := json.Marshal(res)
	if strings.Contains(string(data), `"soloRuntime":null`) {
		t.Fatalf("thread.list wire bytes carry a null soloRuntime: %s", data)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	rowByID := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		rowByID[row["id"].(string)] = row
	}
	rt, ok := rowByID["t1"]["soloRuntime"].(map[string]any)
	if !ok || rt["harness"] != "opencode" || rt["model"] != "glm" {
		t.Fatalf("t1 wire soloRuntime = %#v", rowByID["t1"]["soloRuntime"])
	}
	if _, present := rowByID["t2"]["soloRuntime"]; present {
		t.Fatalf("t2 wire row carries soloRuntime: %#v", rowByID["t2"])
	}
}

// The RPC layer does no pre-validation of its own: bad harnesses and unknown
// threads are rejected by the decider.
func TestSetSoloRejectsUnknownHarnessAndThread(t *testing.T) {
	_, disp := newCommandFixture(t)
	if err := setSoloRPC(t, disp, "t1", "not-a-harness", "", ""); err == nil || !strings.Contains(err.Error(), "unknown harness") {
		t.Fatalf("err = %v, want unknown harness", err)
	}
	if err := setSoloRPC(t, disp, "missing", "claude", "", ""); err == nil || !strings.Contains(err.Error(), "thread not found") {
		t.Fatalf("err = %v, want thread not found", err)
	}
}

func wsSend(ctx context.Context, t *testing.T, c *websocket.Conn, req Request) {
	t.Helper()
	data, _ := json.Marshal(req)
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write %s: %v", req.Method, err)
	}
}

// wsReply waits for the reply to id, discarding any event frames that arrive
// first, and fails the test on an error reply.
func wsReply(ctx context.Context, t *testing.T, c *websocket.Conn, id string) {
	t.Helper()
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read reply %s: %v", id, err)
		}
		var frame struct {
			ID     string          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
		}
		if json.Unmarshal(msg, &frame) != nil || frame.ID != id {
			continue
		}
		if frame.Error != nil {
			t.Fatalf("reply %s: %s", id, frame.Error.Message)
		}
		return
	}
}

// A subscriber that registered before the call sees thread.setSolo as a live
// thread.event frame carrying the solo runtime.
func TestSoloEventFrameArrivesOverWS(t *testing.T) {
	eng, disp := newCommandFixture(t)
	srv := httptest.NewServer(NewServer(disp, eng, disp.runner, "", toolbox.New(), nil).Handler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })

	// Subscribe, then one plain round-trip RPC: the reply is the barrier that
	// proves the connection is registered before the event fires.
	wsSend(ctx, t, c, Request{ID: "sub", Method: "thread.subscribe", Params: mustJSON(t, map[string]string{"threadId": "t1"})})
	wsReply(ctx, t, c, "sub")
	wsSend(ctx, t, c, Request{ID: "ping", Method: "thread.list"})
	wsReply(ctx, t, c, "ping")

	// Read frames until both the ok reply and the solo runtime event arrive.
	wsSend(ctx, t, c, Request{ID: "solo", Method: "thread.setSolo",
		Params: mustJSON(t, map[string]string{"threadId": "t1", "harness": "claude", "model": "sonnet"})})
	replyOK, sawEvent := false, false
	for !(replyOK && sawEvent) {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		var frame struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(msg, &frame) != nil {
			continue
		}
		switch {
		case frame.ID == "solo":
			if frame.Error != nil {
				t.Fatalf("thread.setSolo: %s", frame.Error.Message)
			}
			var out map[string]any
			if err := json.Unmarshal(frame.Result, &out); err != nil || out["ok"] != true {
				t.Fatalf("thread.setSolo reply = %s", frame.Result)
			}
			replyOK = true
		case frame.Method == "thread.event":
			var ev struct {
				ThreadID string `json:"threadId"`
				Type     string `json:"type"`
				Payload  struct {
					Runtime struct {
						Harness string `json:"harness"`
						Model   string `json:"model"`
					} `json:"runtime"`
				} `json:"payload"`
			}
			if json.Unmarshal(frame.Params, &ev) != nil {
				continue
			}
			if ev.ThreadID == "t1" && ev.Type == "thread.solo-runtime-set" {
				if ev.Payload.Runtime.Harness != "claude" || ev.Payload.Runtime.Model != "sonnet" {
					t.Fatalf("event runtime = %+v, want claude/sonnet", ev.Payload.Runtime)
				}
				sawEvent = true
			}
		}
	}
}

// review.submit runs a turn whose prompt never mentions the comments on a
// solo thread, so it is refused until solo mode is off.
func TestReviewSubmitRefusedOnSoloThread(t *testing.T) {
	eng, disp := newCommandFixture(t)
	ctx := context.Background()
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "r1", Type: contracts.CmdReviewCommentAdd, ThreadID: "t1",
		ReviewComment: &contracts.ReviewCommentAdded{ID: "r1", File: "a.go", Line: 1, Body: "fix"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := setSoloRPC(t, disp, "t1", "claude", "", ""); err != nil {
		t.Fatalf("setSolo: %v", err)
	}

	if err := submitReviewRPC(t, disp, "t1"); err == nil || !strings.Contains(err.Error(), "turn solo mode off") {
		t.Fatalf("err = %v, want the solo refusal", err)
	}
	// The refused submit leaves the comment pending and writes no turn.
	st, _ := eng.State(ctx, "t1")
	if len(st.Messages) != 0 || st.ReviewComments[0].Submitted {
		t.Fatalf("refused submit recorded state: messages=%d submitted=%v", len(st.Messages), st.ReviewComments[0].Submitted)
	}
}
