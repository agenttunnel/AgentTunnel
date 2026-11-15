package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// CloudConfig configures the WhatsApp Cloud API adapter: a Business phone
// number, its access token, and the WhatsApp number that receives updates.
type CloudConfig struct {
	// BaseURL overrides the Graph API host (tests, or a proxy). Empty uses the
	// public API.
	BaseURL string
	// PhoneNumberID is the Cloud API phone number id, not the number itself.
	PhoneNumberID string
	AccessToken   string
	// Recipient is the WhatsApp number updates go to, in international format
	// without punctuation (e.g. "15551234567").
	Recipient string
	// PairingCode, when set, is the registration PIN for a number that has not
	// been registered yet. Registration is also available in the Meta dashboard.
	PairingCode string
}

// CloudAdapter sends workflow updates through the WhatsApp Cloud API. It is a
// fallback for deployments that already run a Business number; a normal install
// links the daemon as a companion device instead (see linked.go).
type CloudAdapter struct {
	cfg    CloudConfig
	client *http.Client
}

// cloudAPIVersion is the Graph API version the adapter speaks.
const cloudAPIVersion = "v23.0"

// defaultGraphURL is the public Graph API host.
const defaultGraphURL = "https://graph.facebook.com"

// NewCloudAdapter builds an adapter for cfg.
func NewCloudAdapter(cfg CloudConfig) *CloudAdapter {
	return &CloudAdapter{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// NewDefaultAdapter returns the adapter this build ships. With the
// AT_WHATSAPP_* Cloud API credentials set it uses that fallback; otherwise it
// links the daemon as a WhatsApp companion device, which needs no configuration
// beyond the state directory the session lives in.
func NewDefaultAdapter(stateDir string) Adapter {
	cfg := CloudConfig{
		BaseURL:       os.Getenv("AT_WHATSAPP_GRAPH_URL"),
		PhoneNumberID: os.Getenv("AT_WHATSAPP_PHONE_NUMBER_ID"),
		AccessToken:   os.Getenv("AT_WHATSAPP_TOKEN"),
		Recipient:     os.Getenv("AT_WHATSAPP_TO"),
		PairingCode:   os.Getenv("AT_WHATSAPP_PAIRING_CODE"),
	}
	if cfg.AccessToken != "" && cfg.PhoneNumberID != "" && cfg.Recipient != "" {
		return NewCloudAdapter(cfg)
	}
	return NewLinkedAdapter(stateDir)
}

// Connect verifies the configured number with the Graph API and registers its
// PIN when one is configured. The Cloud API has no QR: the phone number is the
// device, and onLinked names it once the credentials check out. It has no
// asynchronous failures, so onError is unused.
func (a *CloudAdapter) Connect(ctx context.Context, _ func(qr string), onLinked func(detail string), _ func(error)) error {
	if a.cfg.AccessToken == "" || a.cfg.PhoneNumberID == "" || a.cfg.Recipient == "" {
		return errors.New("WhatsApp is not configured: set AT_WHATSAPP_TOKEN, AT_WHATSAPP_PHONE_NUMBER_ID, and AT_WHATSAPP_TO")
	}
	display, err := a.verify(ctx)
	if err != nil {
		return err
	}
	if a.cfg.PairingCode != "" {
		// Registration is idempotent from the user's point of view: a number
		// already registered in the dashboard refuses a second PIN, and that is
		// not a reason to stop delivering updates.
		_ = a.register(ctx, a.cfg.PairingCode)
	}
	if display != "" {
		onLinked("Cloud API · " + display)
	} else {
		onLinked("WhatsApp Cloud API")
	}
	return nil
}

// Disconnect is a no-op: Cloud API credentials stay valid until revoked in the
// Meta dashboard. The bridge persists the user's choice to stop using them.
func (a *CloudAdapter) Disconnect() error { return nil }

// Send delivers one text message to the configured recipient.
func (a *CloudAdapter) Send(ctx context.Context, text string) error {
	if a.cfg.Recipient == "" {
		return errors.New("WhatsApp has no recipient: set AT_WHATSAPP_TO")
	}
	body := map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                a.cfg.Recipient,
		"type":              "text",
		"text":              map[string]any{"preview_url": false, "body": text},
	}
	return a.do(ctx, http.MethodPost, a.path("messages"), body, nil)
}

// verify reads the number's display details from the Graph API. A 2xx proves
// the token can act on this phone number id.
func (a *CloudAdapter) verify(ctx context.Context) (string, error) {
	var out struct {
		DisplayPhoneNumber string `json:"display_phone_number"`
		VerifiedName       string `json:"verified_name"`
	}
	q := url.Values{"fields": {"display_phone_number,verified_name"}}
	if err := a.do(ctx, http.MethodGet, a.path("")+"?"+q.Encode(), nil, &out); err != nil {
		return "", err
	}
	if out.DisplayPhoneNumber == "" {
		return out.VerifiedName, nil
	}
	if out.VerifiedName != "" {
		return out.DisplayPhoneNumber + " (" + out.VerifiedName + ")", nil
	}
	return out.DisplayPhoneNumber, nil
}

// register sets the number's two-step verification PIN.
func (a *CloudAdapter) register(ctx context.Context, pin string) error {
	body := map[string]any{"messaging_product": "whatsapp", "pin": pin}
	return a.do(ctx, http.MethodPost, a.path("register"), body, nil)
}

// path builds a Graph API URL below the configured phone number id.
func (a *CloudAdapter) path(suffix string) string {
	base := strings.TrimRight(a.cfg.BaseURL, "/")
	if base == "" {
		base = defaultGraphURL
	}
	p := base + "/" + cloudAPIVersion + "/" + url.PathEscape(a.cfg.PhoneNumberID)
	if suffix == "" {
		return p
	}
	if strings.HasPrefix(suffix, "?") {
		return p + suffix
	}
	return p + "/" + suffix
}

// do runs one Graph API call and decodes a successful body into out, when set.
// A non-2xx answer is turned into the API's own error message.
func (a *CloudAdapter) do(ctx context.Context, method, target string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.AccessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("WhatsApp API: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("WhatsApp API: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var envelope struct {
			Error struct {
				Message string `json:"message"`
				Code    int    `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Error.Message != "" {
			return fmt.Errorf("WhatsApp API: %s (code %d)", envelope.Error.Message, envelope.Error.Code)
		}
		return fmt.Errorf("WhatsApp API: HTTP %d", res.StatusCode)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("WhatsApp API: %w", err)
		}
	}
	return nil
}
