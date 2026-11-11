package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

const testClaudeToken = "sk-ant-oat01-test-token"

// newTestClaudeLimits points the fetcher at handler and feeds it fixed creds.
func newTestClaudeLimits(t *testing.T, handler http.HandlerFunc, creds func(context.Context) (claudeCreds, error), now time.Time) *claudeLimits {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &claudeLimits{
		client:    srv.Client(),
		baseURL:   srv.URL,
		readCreds: creds,
		now:       func() time.Time { return now },
	}
}

func validCreds(context.Context) (claudeCreds, error) {
	return claudeCreds{accessToken: testClaudeToken}, nil
}

func TestClaudeLimitsParsesWindowsAndSkipsNulls(t *testing.T) {
	var gotAuth, gotBeta string
	c := newTestClaudeLimits(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth/usage" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		_, _ = w.Write([]byte(`{
			"five_hour": {"utilization": 42.5, "resets_at": "2026-11-10T14:00:00Z"},
			"seven_day": {"utilization": 130, "resets_at": null},
			"seven_day_opus": null
		}`))
	}, validCreds, time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))

	windows, reason := c.Fetch(context.Background())
	if reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
	if gotAuth != "Bearer "+testClaudeToken || gotBeta != "oauth-2025-04-01" {
		t.Fatalf("headers auth=%q beta=%q", gotAuth, gotBeta)
	}
	if len(windows) != 2 {
		t.Fatalf("windows = %#v, want the two non-null windows", windows)
	}
	if windows[0].ID != "five_hour" || windows[0].Label != "5-hour" || windows[0].UsedPercent != 42.5 {
		t.Fatalf("five_hour = %#v", windows[0])
	}
	if want := time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC); !windows[0].ResetsAt.Equal(want) {
		t.Fatalf("resetsAt = %v, want %v", windows[0].ResetsAt, want)
	}
	if windows[1].ID != "weekly" || windows[1].UsedPercent != 100 || !windows[1].ResetsAt.IsZero() {
		t.Fatalf("weekly = %#v, want clamped to 100 with no reset", windows[1])
	}

	// The token authenticates the request but must never reach the reply.
	raw, err := json.Marshal(contracts.HarnessLimits{Harness: contracts.HarnessClaude, Available: true, Windows: windows})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testClaudeToken) {
		t.Fatalf("limits JSON leaked the token: %s", raw)
	}
}

func TestClaudeLimitsExpiredTokenSkipsRequest(t *testing.T) {
	var hits atomic.Int64
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	c := newTestClaudeLimits(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) },
		func(context.Context) (claudeCreds, error) {
			return claudeCreds{accessToken: testClaudeToken, expiresAt: now.Add(-time.Minute)}, nil
		}, now)

	windows, reason := c.Fetch(context.Background())
	if reason == "" || windows != nil {
		t.Fatalf("expired token should be unavailable, got windows=%v reason=%q", windows, reason)
	}
	if hits.Load() != 0 {
		t.Fatalf("expired token made %d HTTP calls, want 0", hits.Load())
	}
}

func TestClaudeLimitsStatusReasons(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{"rejected", http.StatusUnauthorized, "rejected"},
		{"forbidden", http.StatusForbidden, "rejected"},
		{"rate limited", http.StatusTooManyRequests, "rate limited"},
		{"server error", http.StatusInternalServerError, "unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClaudeLimits(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}, validCreds, time.Now())
			windows, reason := c.Fetch(context.Background())
			if windows != nil || !strings.Contains(reason, tt.want) {
				t.Fatalf("windows=%v reason=%q, want reason containing %q", windows, reason, tt.want)
			}
			if strings.Contains(reason, testClaudeToken) {
				t.Fatalf("reason leaked the token: %q", reason)
			}
		})
	}
}

func TestClaudeLimitsWithoutCredentialsNeverCallsOut(t *testing.T) {
	var hits atomic.Int64
	c := newTestClaudeLimits(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) },
		func(context.Context) (claudeCreds, error) { return claudeCreds{}, errNoClaudeCreds }, time.Now())

	windows, reason := c.Fetch(context.Background())
	if windows != nil || reason != "not signed in to Claude" {
		t.Fatalf("windows=%v reason=%q", windows, reason)
	}
	if hits.Load() != 0 {
		t.Fatalf("made %d HTTP calls without credentials", hits.Load())
	}
}

func TestParseClaudeCredsKeepsOnlyTokenAndExpiry(t *testing.T) {
	creds, err := parseClaudeCreds([]byte(`{"claudeAiOauth":{"accessToken":" tok ","refreshToken":"r","expiresAt":1700000000000}}`))
	if err != nil {
		t.Fatal(err)
	}
	if creds.accessToken != "tok" {
		t.Fatalf("token = %q, want trimmed tok", creds.accessToken)
	}
	if !creds.expiresAt.Equal(time.UnixMilli(1700000000000)) {
		t.Fatalf("expiresAt = %v", creds.expiresAt)
	}

	for _, raw := range []string{`{}`, `{"claudeAiOauth":{"accessToken":"  "}}`, `not json`} {
		if _, err := parseClaudeCreds([]byte(raw)); !errors.Is(err, errNoClaudeCreds) {
			t.Fatalf("parseClaudeCreds(%q) err = %v, want errNoClaudeCreds", raw, err)
		}
	}
}
