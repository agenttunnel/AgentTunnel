package cli

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// An attached web server shares the preference store of the daemon it attaches
// to: its page seeds from that store, and its writes land in it through the
// token-injecting proxy.
func TestStartWebAttachedSharesPreferencesWithDaemon(t *testing.T) {
	stateDir := t.TempDir()
	_, httpURL, token := newWebTestDaemon(t, stateDir)

	req, _ := http.NewRequest(http.MethodPost, httpURL+"/prefs", strings.NewReader(`{"at.theme":"nord"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusNoContent {
		t.Fatalf("seed daemon prefs: %v", err)
	} else {
		res.Body.Close()
	}

	env := &Env{Out: io.Discard, Err: io.Discard, HomeDir: stateDir, Version: "webtest"}
	w, err := startWeb(context.Background(), env, t.TempDir())
	if err != nil {
		t.Fatalf("startWeb: %v", err)
	}
	t.Cleanup(w.Close)

	page, err := http.Get(w.URL() + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if !strings.Contains(string(body), `"at.theme":"nord"`) {
		t.Fatalf("attached page does not seed the daemon's theme: %s", body)
	}

	// The page writes through its own origin with no credentials of its own.
	write, err := http.Post(w.URL()+"/prefs", "application/json", strings.NewReader(`{"at.logsOpen":"false"}`))
	if err != nil {
		t.Fatalf("POST /prefs: %v", err)
	}
	write.Body.Close()
	if write.StatusCode != http.StatusNoContent {
		t.Fatalf("proxied POST /prefs = %d, want 204", write.StatusCode)
	}

	read, _ := http.NewRequest(http.MethodGet, httpURL+"/prefs", nil)
	read.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(read)
	if err != nil {
		t.Fatalf("GET daemon /prefs: %v", err)
	}
	stored, _ := io.ReadAll(res.Body)
	res.Body.Close()
	for _, want := range []string{`"at.theme":"nord"`, `"at.logsOpen":"false"`} {
		if !strings.Contains(string(stored), want) {
			t.Fatalf("daemon store = %s, want it to contain %s", stored, want)
		}
	}
}
