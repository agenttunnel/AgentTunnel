package cli

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestStartWebAttachedProxySiteGate pins the Sec-Fetch-Site gate on the
// token-injecting /prefs and /attachments proxy: a cross-site page must not
// write through the daemon's identity, while same-origin and non-browser
// clients still reach the daemon.
func TestStartWebAttachedProxySiteGate(t *testing.T) {
	stateDir := t.TempDir()
	newWebTestDaemon(t, stateDir)

	env := &Env{Out: io.Discard, Err: io.Discard, HomeDir: stateDir, Version: "webtest"}
	w, err := startWeb(context.Background(), env, t.TempDir())
	if err != nil {
		t.Fatalf("startWeb: %v", err)
	}
	t.Cleanup(w.Close)

	do := func(path, site, body string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, w.URL()+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s (Sec-Fetch-Site %q): %v", path, site, err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	// A valid empty merge: the daemon answers 204 when it accepts the request.
	const prefsBody = `{}`
	if got := do("/prefs", "cross-site", prefsBody); got != http.StatusForbidden {
		t.Fatalf("cross-site POST /prefs = %d, want 403", got)
	}
	for _, site := range []string{"same-origin", "none", ""} {
		if got := do("/prefs", site, prefsBody); got != http.StatusNoContent {
			t.Fatalf("POST /prefs with Sec-Fetch-Site %q = %d, want 204", site, got)
		}
	}

	// The attachment body is junk, so the daemon refuses it; the point is only
	// whether the gate or the daemon answered.
	if got := do("/attachments?threadId=x&name=y", "cross-site", "junk"); got != http.StatusForbidden {
		t.Fatalf("cross-site POST /attachments = %d, want 403", got)
	}
	for _, site := range []string{"same-origin", ""} {
		got := do("/attachments?threadId=x&name=y", site, "junk")
		if got == http.StatusForbidden || got == http.StatusUnauthorized {
			t.Fatalf("POST /attachments with Sec-Fetch-Site %q = %d, want the daemon's answer", site, got)
		}
	}
}
