package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// commandCodeLimits reads Command Code subscription usage from the same
// billing endpoint the installed CLI's /usage view uses. It only reads the API
// key the CLI already stored; it never creates or refreshes credentials.
type commandCodeLimits struct {
	client    *http.Client
	baseURL   string
	readToken func(ctx context.Context) (string, error)
}

var errNoCommandCodeToken = errors.New("no command code credentials")

// NewCommandCodeLimits returns the Command Code subscription usage fetcher.
func NewCommandCodeLimits() LimitsFetcher {
	return &commandCodeLimits{
		client:    &http.Client{Timeout: 5 * time.Second},
		baseURL:   "https://api.commandcode.ai",
		readToken: readCommandCodeToken,
	}
}

func (c *commandCodeLimits) Harness() contracts.Harness { return contracts.HarnessCommandCode }

// Fetch returns the windows, or a generic reason. Errors never include the
// token, the URL, or response bodies.
func (c *commandCodeLimits) Fetch(ctx context.Context) ([]contracts.LimitWindow, string) {
	token, err := c.readToken(ctx)
	if err != nil {
		return nil, "not signed in to Command Code"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/alpha/billing/credits", nil)
	if err != nil {
		return nil, "Command Code usage unavailable"
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "Command Code usage unavailable"
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, "Command Code rejected the sign-in; run commandcode to sign in again"
	case http.StatusTooManyRequests:
		return nil, "Command Code usage is rate limited"
	default:
		return nil, "Command Code usage unavailable"
	}

	var body commandCodeCreditsBody
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, "Command Code usage response was not recognized"
	}
	return body.windows(), ""
}

// readCommandCodeToken looks for the key in the order the CLI itself uses: the
// env override, then the auth file written by `commandcode` login.
func readCommandCodeToken(context.Context) (string, error) {
	if tok := strings.TrimSpace(os.Getenv("COMMAND_CODE_API_KEY")); tok != "" {
		return tok, nil
	}
	raw, err := os.ReadFile(commandCodeAuthPath())
	if err != nil {
		return "", errNoCommandCodeToken
	}
	return parseCommandCodeToken(raw)
}

func commandCodeAuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".commandcode", "auth.json")
}

// parseCommandCodeToken keeps only the API key. The decode error is dropped on
// purpose: it could quote part of the input.
func parseCommandCodeToken(raw []byte) (string, error) {
	var f struct {
		APIKey string `json:"apiKey"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return "", errNoCommandCodeToken
	}
	tok := strings.TrimSpace(f.APIKey)
	if tok == "" {
		return "", errNoCommandCodeToken
	}
	return tok, nil
}

// commandCodeCreditsBody is the subset of the CLI's /alpha/billing/credits
// response the desktop shows. windowLimits is absent for accounts without
// metered plan windows.
type commandCodeCreditsBody struct {
	WindowLimits *struct {
		Limited  bool               `json:"limited"`
		FiveHour *commandCodeWindow `json:"fiveHour"`
		Weekly   *commandCodeWindow `json:"weekly"`
	} `json:"windowLimits"`
}

type commandCodeWindow struct {
	Used    float64 `json:"used"`
	Cap     float64 `json:"cap"`
	ResetAt int64   `json:"resetAt"`
}

// windows mirrors the CLI: a window is shown only when metered limits are
// enabled, and its bar is used/cap clamped to 100.
func (b commandCodeCreditsBody) windows() []contracts.LimitWindow {
	if b.WindowLimits == nil || !b.WindowLimits.Limited {
		return nil
	}
	var out []contracts.LimitWindow
	add := func(id, label string, w *commandCodeWindow) {
		if w == nil || w.Cap <= 0 {
			return
		}
		lw := contracts.LimitWindow{ID: id, Label: label, UsedPercent: min(max(w.Used/w.Cap*100, 0), 100)}
		if w.ResetAt > 0 {
			lw.ResetsAt = time.UnixMilli(w.ResetAt)
		}
		out = append(out, lw)
	}
	add("five_hour", "5-hour", b.WindowLimits.FiveHour)
	add("weekly", "Weekly", b.WindowLimits.Weekly)
	return out
}
