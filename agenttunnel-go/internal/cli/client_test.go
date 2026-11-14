package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The client sends its token as a Bearer header (never in the URL) and its read
// loop correlates a reply by id even when a pushed event frame arrives first.
func TestClientBearerHeaderAndReplyCorrelation(t *testing.T) {
	gotAuth := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		if r.URL.RawQuery != "" {
			t.Errorf("token must not ride in the query string: %q", r.URL.RawQuery)
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		_, data, err := ws.Read(r.Context())
		if err != nil {
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(data, &req)
		// An event frame in front of the reply must not be mistaken for it.
		_ = ws.Write(r.Context(), websocket.MessageText, []byte(`{"method":"thread.event","params":{"type":"agent.output"}}`))
		_ = ws.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`{"id":%q,"result":{"ok":true}}`, req.ID)))
		_, _, _ = ws.Read(context.Background())
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), "tok-123")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if auth := <-gotAuth; auth != "Bearer tok-123" {
		t.Fatalf("Authorization = %q", auth)
	}

	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.Call(ctx, "project.list", nil, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if !out.OK {
		t.Fatal("reply result was not decoded")
	}
	select {
	case ev := <-c.Events():
		if ev.Method != "thread.event" {
			t.Fatalf("event = %+v", ev)
		}
		var params struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(ev.Params, &params)
		if params.Type != "agent.output" {
			t.Fatalf("event params = %s", ev.Params)
		}
	case <-ctx.Done():
		t.Fatal("pushed event was never published")
	}
}

// A daemon reply bigger than websocket's 32768-byte default (thread.list on a
// machine with real history) must still be read and decoded, so Dial raises
// the connection's read limit.
func TestClientReadsReplyOverDefaultLimit(t *testing.T) {
	const payload = 128 * 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		_, data, _ := ws.Read(r.Context())
		var req struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(data, &req)
		frame := fmt.Sprintf(`{"id":%q,"result":["%s"]}`, req.ID, strings.Repeat("x", payload))
		_ = ws.Write(r.Context(), websocket.MessageText, []byte(frame))
		_, _, _ = ws.Read(context.Background())
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	var out []string
	if err := c.Call(ctx, "thread.list", nil, &out); err != nil {
		t.Fatalf("call over the default read limit: %v", err)
	}
	if len(out) != 1 || len(out[0]) != payload {
		t.Fatalf("reply = %d elems (first %d bytes), want one %d-byte element", len(out), len(out[0]), payload)
	}
}

func TestClientCallReportsRPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		_, data, _ := ws.Read(r.Context())
		var req struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(data, &req)
		_ = ws.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`{"id":%q,"error":{"code":1,"message":"boom"}}`, req.ID)))
		_, _, _ = ws.Read(context.Background())
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := c.Call(ctx, "bad.method", nil, nil); err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
	}
}
