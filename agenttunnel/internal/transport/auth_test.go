package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthorizedTokenPlacement(t *testing.T) {
	const token = "s3cret"
	s := &Server{token: token}

	cases := []struct {
		name   string
		remote string
		target string
		header string
		want   bool
	}{
		{"query token from loopback v4", "127.0.0.1:5000", "/ws?token=s3cret", "", true},
		{"query token from loopback v6", "[::1]:5000", "/ws?token=s3cret", "", true},
		{"query token from LAN refused", "192.168.1.20:5000", "/ws?token=s3cret", "", false},
		{"query token wrong on loopback", "127.0.0.1:5000", "/ws?token=nope", "", false},
		{"query token empty on loopback", "127.0.0.1:5000", "/ws?token=", "", false},
		{"bearer from LAN accepted", "192.168.1.20:5000", "/ws", "Bearer s3cret", true},
		{"bearer wrong from LAN refused", "192.168.1.20:5000", "/ws", "Bearer nope", false},
		{"bearer without scheme refused", "192.168.1.20:5000", "/ws", "s3cret", false},
		{"bearer with empty value refused", "192.168.1.20:5000", "/ws", "Bearer ", false},
		{"unparseable peer refuses query token", "not-an-addr", "/ws?token=s3cret", "", false},
		{"unparseable peer still accepts bearer", "not-an-addr", "/ws", "Bearer s3cret", true},
		{"no credentials refused", "127.0.0.1:5000", "/ws", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			req.RemoteAddr = tc.remote
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			if got := s.authorized(req); got != tc.want {
				t.Fatalf("authorized = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAuthorizedAllowsAnyPeerWhenNoTokenConfigured(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.RemoteAddr = "192.168.1.20:5000"
	if !s.authorized(req) {
		t.Fatal("a server without a token must not refuse requests")
	}
}

// A query-token request from a LAN peer must be refused before the WebSocket
// upgrade, so the 401 is the handler's answer rather than a side effect.
func TestHandleWSRefusesQueryTokenFromLAN(t *testing.T) {
	s := &Server{token: "s3cret"}
	req := httptest.NewRequest(http.MethodGet, "/ws?token=s3cret", nil)
	req.RemoteAddr = "192.168.1.20:5000"
	rec := httptest.NewRecorder()
	s.handleWS(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestTokenMatchesIsExact(t *testing.T) {
	cases := []struct {
		got, want string
		match     bool
	}{
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"abc", "ab", false},
		{"ab", "abc", false},
		{"", "abc", false},
	}
	for _, tc := range cases {
		if got := tokenMatches(tc.got, tc.want); got != tc.match {
			t.Fatalf("tokenMatches(%q, %q) = %v, want %v", tc.got, tc.want, got, tc.match)
		}
	}
}
