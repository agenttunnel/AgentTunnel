package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The desktop window must open with the daemon's saved preferences already in
// the page, the same as the web UI, so it never flashes the default theme.
func TestClientAssetsSeedTheIndexWithSavedPrefs(t *testing.T) {
	h := clientAssets("http://127.0.0.1:1", "tok", func() map[string]string { return map[string]string{"at.theme": "nord"} })
	for _, path := range []string{"/", "/index.html"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"at.theme":"nord"`) {
			t.Fatalf("%s: status %d, body does not seed the saved theme", path, rec.Code)
		}
	}
}

// The page's /attachments and /prefs calls stay on the window's origin and
// reach the daemon with its bearer, so no CORS surface is needed.
func TestClientAssetsProxiesDaemonRoutesWithBearer(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Seen-Auth", r.Header.Get("Authorization"))
		w.Header().Set("X-Seen-Path", r.URL.RequestURI())
		_, _ = w.Write(body)
	}))
	defer daemon.Close()

	h := clientAssets(daemon.URL, "tok", func() map[string]string { return nil })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/attachments?threadId=th_x&name=a.png", strings.NewReader("bytes")))
	if rec.Body.String() != "bytes" {
		t.Fatalf("body = %q, want the uploaded bytes", rec.Body.String())
	}
	if got := rec.Header().Get("X-Seen-Auth"); got != "Bearer tok" {
		t.Fatalf("daemon saw Authorization %q, want the bearer", got)
	}
	if got := rec.Header().Get("X-Seen-Path"); got != "/attachments?threadId=th_x&name=a.png" {
		t.Fatalf("daemon saw path %q", got)
	}
}
