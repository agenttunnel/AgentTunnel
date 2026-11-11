package remote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// graphCall is one request the adapter made, for asserting the wire shape.
type graphCall struct {
	method string
	path   string
	auth   string
	body   map[string]any
}

func graphServer(t *testing.T) (*httptest.Server, func() []graphCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []graphCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		call := graphCall{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &call.body)
		}
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"display_phone_number":"+1 555 0100","verified_name":"AgentTunnel"}`))
		case strings.HasSuffix(r.URL.Path, "/register"):
			_, _ = w.Write([]byte(`{"success":true}`))
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.1"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []graphCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]graphCall(nil), calls...)
	}
}

func TestCloudAdapterVerifiesThenSends(t *testing.T) {
	srv, calls := graphServer(t)
	adapter := NewCloudAdapter(CloudConfig{
		BaseURL:       srv.URL,
		PhoneNumberID: "123",
		AccessToken:   "tok",
		Recipient:     "15551234567",
		PairingCode:   "654321",
	})

	var detail string
	if err := adapter.Connect(context.Background(), func(string) {}, func(d string) { detail = d }, func(error) {}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !strings.Contains(detail, "+1 555 0100") || !strings.Contains(detail, "AgentTunnel") {
		t.Fatalf("linked detail = %q, want the verified number", detail)
	}

	if err := adapter.Send(context.Background(), "turn complete"); err != nil {
		t.Fatalf("send: %v", err)
	}

	got := calls()
	if len(got) != 3 {
		t.Fatalf("graph calls = %d (%+v), want verify, register, send", len(got), got)
	}
	if got[0].method != http.MethodGet || got[0].path != "/v23.0/123" {
		t.Fatalf("verify call = %+v", got[0])
	}
	if got[1].path != "/v23.0/123/register" || got[1].body["pin"] != "654321" {
		t.Fatalf("register call = %+v", got[1])
	}
	if got[2].path != "/v23.0/123/messages" {
		t.Fatalf("send path = %q", got[2].path)
	}
	if got[2].auth != "Bearer tok" {
		t.Fatalf("send auth = %q", got[2].auth)
	}
	if got[2].body["to"] != "15551234567" || got[2].body["type"] != "text" {
		t.Fatalf("send body = %+v", got[2].body)
	}
	text, _ := got[2].body["text"].(map[string]any)
	if text["body"] != "turn complete" {
		t.Fatalf("message text = %+v", text)
	}
}

func TestCloudAdapterSurfacesAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid OAuth access token","code":190}}`))
	}))
	t.Cleanup(srv.Close)

	adapter := NewCloudAdapter(CloudConfig{
		BaseURL: srv.URL, PhoneNumberID: "123", AccessToken: "bad", Recipient: "15551234567",
	})
	err := adapter.Connect(context.Background(), func(string) {}, func(string) {}, func(error) {})
	if err == nil || !strings.Contains(err.Error(), "Invalid OAuth access token") {
		t.Fatalf("connect error = %v, want the API message", err)
	}
}

func TestCloudAdapterRequiresConfiguration(t *testing.T) {
	adapter := NewCloudAdapter(CloudConfig{})
	if err := adapter.Connect(context.Background(), func(string) {}, func(string) {}, func(error) {}); err == nil {
		t.Fatal("connect without credentials must fail")
	}
	if err := adapter.Send(context.Background(), "hi"); err == nil {
		t.Fatal("send without a recipient must fail")
	}
}
