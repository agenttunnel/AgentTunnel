package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// claudeLimits reads Claude subscription usage from the same OAuth endpoint
// Claude Code's /usage view uses. It only reads the access token the local
// Claude install already stored; it never refreshes it, because running
// `claude` does that.
type claudeLimits struct {
	client    *http.Client
	baseURL   string
	readCreds func(ctx context.Context) (claudeCreds, error)
	now       func() time.Time
}

type claudeCreds struct {
	accessToken string
	// expiresAt is zero when the source does not record an expiry (the env var).
	expiresAt time.Time
}

var errNoClaudeCreds = errors.New("no claude credentials")

// NewClaudeLimits returns the Claude subscription usage fetcher.
func NewClaudeLimits() LimitsFetcher {
	return &claudeLimits{
		client:    &http.Client{Timeout: 5 * time.Second},
		baseURL:   "https://api.anthropic.com",
		readCreds: readClaudeCreds,
		now:       time.Now,
	}
}

func (c *claudeLimits) Harness() contracts.Harness { return contracts.HarnessClaude }

// Fetch returns the windows, or a generic reason. Errors never include the
// token, the URL's query, or response bodies.
func (c *claudeLimits) Fetch(ctx context.Context) ([]contracts.LimitWindow, string) {
	creds, err := c.readCreds(ctx)
	if err != nil {
		return nil, "not signed in to Claude"
	}
	if !creds.expiresAt.IsZero() && !c.now().Before(creds.expiresAt) {
		return nil, "Claude sign-in expired; run claude to refresh it"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/oauth/usage", nil)
	if err != nil {
		return nil, "Claude usage unavailable"
	}
	req.Header.Set("Authorization", "Bearer "+creds.accessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-01")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "Claude usage unavailable"
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, "Claude rejected the sign-in; run claude to sign in again"
	case http.StatusTooManyRequests:
		return nil, "Claude usage is rate limited"
	default:
		return nil, "Claude usage unavailable"
	}

	var body claudeUsageBody
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, "Claude usage response was not recognized"
	}
	return body.windows(), ""
}

// readClaudeCreds looks for the token in the order Claude Code itself uses:
// the env override, then the macOS keychain, then the credentials file.
func readClaudeCreds(ctx context.Context) (claudeCreds, error) {
	if tok := strings.TrimSpace(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")); tok != "" {
		return claudeCreds{accessToken: tok}, nil
	}
	if runtime.GOOS == "darwin" {
		if raw, err := claudeKeychainCreds(ctx); err == nil {
			if creds, err := parseClaudeCreds(raw); err == nil {
				return creds, nil
			}
		}
	}
	raw, err := os.ReadFile(claudeCredentialsPath())
	if err != nil {
		return claudeCreds{}, errNoClaudeCreds
	}
	return parseClaudeCreds(raw)
}

// claudeKeychainCreds reads the credentials JSON from the macOS keychain. The
// read runs only when usage is asked for, never at daemon boot.
func claudeKeychainCreds(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
}

func claudeCredentialsPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

// parseClaudeCreds keeps only the access token and its expiry. The decode
// error is dropped on purpose: it could quote part of the input.
func parseClaudeCreds(raw []byte) (claudeCreds, error) {
	var f struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return claudeCreds{}, errNoClaudeCreds
	}
	tok := strings.TrimSpace(f.ClaudeAiOauth.AccessToken)
	if tok == "" {
		return claudeCreds{}, errNoClaudeCreds
	}
	creds := claudeCreds{accessToken: tok}
	if f.ClaudeAiOauth.ExpiresAt > 0 {
		creds.expiresAt = time.UnixMilli(f.ClaudeAiOauth.ExpiresAt)
	}
	return creds, nil
}

// claudeUsageBody is the subset of the usage response the desktop shows. Each
// window is null when the account does not have it.
type claudeUsageBody struct {
	FiveHour     *claudeUsageWindow `json:"five_hour"`
	SevenDay     *claudeUsageWindow `json:"seven_day"`
	SevenDayOpus *claudeUsageWindow `json:"seven_day_opus"`
}

type claudeUsageWindow struct {
	Utilization float64    `json:"utilization"`
	ResetsAt    *time.Time `json:"resets_at"`
}

func (b claudeUsageBody) windows() []contracts.LimitWindow {
	var out []contracts.LimitWindow
	add := func(id, label string, w *claudeUsageWindow) {
		if w == nil {
			return
		}
		lw := contracts.LimitWindow{ID: id, Label: label, UsedPercent: min(max(w.Utilization, 0), 100)}
		if w.ResetsAt != nil {
			lw.ResetsAt = *w.ResetsAt
		}
		out = append(out, lw)
	}
	add("five_hour", "5-hour", b.FiveHour)
	add("weekly", "Weekly", b.SevenDay)
	add("weekly_opus", "Weekly Opus", b.SevenDayOpus)
	return out
}
