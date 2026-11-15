package providers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

const testCommandCodeToken = "cc-test-token"

func readCommandCodeFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/commandcode_credits.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// newTestCommandCodeLimits points the fetcher at handler and feeds it a fixed token.
func newTestCommandCodeLimits(t *testing.T, handler http.HandlerFunc, token func(context.Context) (string, error)) *commandCodeLimits {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &commandCodeLimits{
		client:    srv.Client(),
		baseURL:   srv.URL,
		readToken: token,
	}
}

func validCommandCodeToken(context.Context) (string, error) { return testCommandCodeToken, nil }

func TestCommandCodeLimitsParsesWindowLimits(t *testing.T) {
	fixture := readCommandCodeFixture(t)
	var gotAuth string
	c := newTestCommandCodeLimits(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/alpha/billing/credits" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write(fixture)
	}, validCommandCodeToken)

	windows, reason := c.Fetch(context.Background())
	if reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
	if gotAuth != "Bearer "+testCommandCodeToken {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if len(windows) != 2 {
		t.Fatalf("windows = %#v, want 5-hour and weekly", windows)
	}
	if windows[0].ID != "five_hour" || windows[0].Label != "5-hour" || math.Abs(windows[0].UsedPercent-24.1) > 1e-9 {
		t.Fatalf("five_hour = %#v", windows[0])
	}
	if want := time.UnixMilli(1762794000000); !windows[0].ResetsAt.Equal(want) {
		t.Fatalf("five_hour resetsAt = %v, want %v", windows[0].ResetsAt, want)
	}
	if windows[1].ID != "weekly" || windows[1].UsedPercent != 100 {
		t.Fatalf("weekly = %#v, want clamped to 100", windows[1])
	}

	// The token authenticates the request but must never reach the reply.
	raw, err := json.Marshal(contracts.HarnessLimits{Harness: contracts.HarnessCommandCode, Available: true, Windows: windows})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testCommandCodeToken) {
		t.Fatalf("limits JSON leaked the token: %s", raw)
	}
}

func TestCommandCodeLimitsSkipsUnlimitedWindows(t *testing.T) {
	var body commandCodeCreditsBody
	if err := json.Unmarshal([]byte(`{
		"windowLimits": {
			"limited": false,
			"fiveHour": {"used": 10, "cap": 100, "resetAt": 1762794000000}
		}
	}`), &body); err != nil {
		t.Fatal(err)
	}
	if got := body.windows(); got != nil {
		t.Fatalf("windows() = %#v, want none when limits are not metered", got)
	}

	if err := json.Unmarshal([]byte(`{"windowLimits":{"limited":true,"fiveHour":{"used":1,"cap":0}}}`), &body); err != nil {
		t.Fatal(err)
	}
	if got := body.windows(); got != nil {
		t.Fatalf("windows() = %#v, want none for a zero cap", got)
	}
}

func TestCommandCodeLimitsWithoutCredentialsNeverCallsOut(t *testing.T) {
	var hits atomic.Int64
	c := newTestCommandCodeLimits(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) },
		func(context.Context) (string, error) { return "", errNoCommandCodeToken })

	windows, reason := c.Fetch(context.Background())
	if windows != nil || reason != "not signed in to Command Code" {
		t.Fatalf("windows=%v reason=%q", windows, reason)
	}
	if hits.Load() != 0 {
		t.Fatalf("made %d HTTP calls without credentials", hits.Load())
	}
}

func TestCommandCodeLimitsStatusReasons(t *testing.T) {
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
			c := newTestCommandCodeLimits(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}, validCommandCodeToken)
			windows, reason := c.Fetch(context.Background())
			if windows != nil || !strings.Contains(reason, tt.want) {
				t.Fatalf("windows=%v reason=%q, want reason containing %q", windows, reason, tt.want)
			}
			if strings.Contains(reason, testCommandCodeToken) {
				t.Fatalf("reason leaked the token: %q", reason)
			}
		})
	}
}

func TestParseCommandCodeTokenKeepsOnlyAPIKey(t *testing.T) {
	tok, err := parseCommandCodeToken([]byte(`{"apiKey":" key ","userId":"u"}`))
	if err != nil {
		t.Fatal(err)
	}
	if tok != "key" {
		t.Fatalf("token = %q, want trimmed key", tok)
	}

	for _, raw := range []string{`{}`, `{"apiKey":"  "}`, `not json`} {
		if _, err := parseCommandCodeToken([]byte(raw)); !errors.Is(err, errNoCommandCodeToken) {
			t.Fatalf("parseCommandCodeToken(%q) err = %v, want errNoCommandCodeToken", raw, err)
		}
	}
}

func TestReadCommandCodeTokenPrefersEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("COMMAND_CODE_API_KEY", " env-token ")

	tok, err := readCommandCodeToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "env-token" {
		t.Fatalf("token = %q, want trimmed env token", tok)
	}
}

func TestReadCommandCodeTokenFromAuthFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("COMMAND_CODE_API_KEY", "")

	dir := filepath.Join(home, ".commandcode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"apiKey":"file-token","userId":"u"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tok, err := readCommandCodeToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "file-token" {
		t.Fatalf("token = %q, want the stored API key", tok)
	}
}
