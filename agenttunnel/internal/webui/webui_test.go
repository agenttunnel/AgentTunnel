package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const indexHTML = "<!doctype html><html><head></head><body>spa index</body></html>"

// mapFS is a minimal built-SPA shape: the index plus one content-hashed
// asset, the way Vite emits dist/.
func mapFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              {Data: []byte(indexHTML)},
		"assets/app-DGz3kW9L.js":  {Data: []byte("console.log('app')")},
		"assets/app-Ck1fPq2E.css": {Data: []byte("body{margin:0}")},
	}
}

func TestHandlerServesIndexAtRoot(t *testing.T) {
	rec := httptest.NewRecorder()
	handler(mapFS(), nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != indexHTML {
		t.Fatalf("body = %q, want the index contents", body)
	}
}

func TestHandlerFallbacksAndRealFiles(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		// Client-side routes have no backing file: the index must come back
		// so the frontend router can resolve them.
		{name: "client-side route", path: "/threads/xyz", want: indexHTML},
		{name: "nested client-side route", path: "/threads/xyz/review", want: indexHTML},
		{name: "index by name", path: "/index.html", want: indexHTML},
		// A real file is served byte-identical, never shadowed by the index.
		{name: "hashed js asset", path: "/assets/app-DGz3kW9L.js", want: "console.log('app')"},
		{name: "hashed css asset", path: "/assets/app-Ck1fPq2E.css", want: "body{margin:0}"},
		// A path that looks like an asset but is not in the tree is still a
		// client-side route from the server's point of view.
		{name: "hashed asset miss", path: "/assets/stale-HASH.js", want: indexHTML},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler(mapFS(), nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if body := rec.Body.String(); body != tc.want {
				t.Fatalf("body = %q, want %q", body, tc.want)
			}
		})
	}
}

func TestHandlerIndexNotServedAsRedirectTarget(t *testing.T) {
	// The index fallback must be a real 200 response, not FileServer's
	// 301-style redirect to ./index.html, or deep links break on reload.
	rec := httptest.NewRecorder()
	handler(mapFS(), nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/threads/xyz", nil))

	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("Location = %q, want none", loc)
	}
}

func TestHandlerCacheHeaders(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{name: "index at root", path: "/", want: "no-cache"},
		{name: "index via fallback", path: "/threads/xyz", want: "no-cache"},
		{name: "hashed asset", path: "/assets/app-DGz3kW9L.js", want: assetCache},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler(mapFS(), nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.want {
				t.Fatalf("Cache-Control = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHandlerSeedsPrefsIntoHead(t *testing.T) {
	for _, path := range []string{"/", "/index.html", "/threads/xyz"} {
		rec := httptest.NewRecorder()
		handler(mapFS(), func() map[string]string {
			return map[string]string{"at.theme": "nord", "at.layout": `{"sidebarW":300}`}
		}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want %d", path, rec.Code, http.StatusOK)
		}
		if !strings.HasPrefix(body, "<!doctype html><html><head><script>") {
			t.Fatalf("%s: prefs script is not first in <head>: %q", path, body)
		}
		for _, want := range []string{`"at.theme":"nord"`, `"at.layout":"{\"sidebarW\":300}"`, "localStorage.setItem"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s: body does not contain %q: %q", path, want, body)
			}
		}
		if !strings.Contains(body, "</script></head><body>spa index</body></html>") {
			t.Fatalf("%s: index body was altered: %q", path, body)
		}
	}
}

func TestSeedPrefsRunsBeforeTheThemeScript(t *testing.T) {
	index := []byte(`<html><head><meta charset="utf-8"><script>theme()</script></head><body></body></html>`)
	out := string(seedPrefs(index, map[string]string{"at.theme": "nord"}))

	meta := strings.Index(out, `<meta charset="utf-8">`)
	seed := strings.Index(out, "localStorage.setItem")
	theme := strings.Index(out, "<script>theme()")
	if meta < 0 || seed < 0 || theme < 0 {
		t.Fatalf("missing part of the document: %q", out)
	}
	if !(meta < seed && seed < theme) {
		t.Fatalf("seed script must follow the charset meta and precede the theme script: %q", out)
	}
}

func TestHandlerSeedsPrefsCannotCloseScriptTag(t *testing.T) {
	rec := httptest.NewRecorder()
	handler(mapFS(), func() map[string]string {
		return map[string]string{"at.drafts": `</script><img src=x onerror=alert(1)>`}
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	body := rec.Body.String()
	if strings.Contains(body, "</script><img") {
		t.Fatalf("pref value escaped its script tag: %q", body)
	}
	if !strings.Contains(body, `\u003c/script\u003e`) {
		t.Fatalf("pref value was not JSON-escaped: %q", body)
	}
}

func TestHandlerServesPlaceholderIndex(t *testing.T) {
	// The committed dist/index.html is what a fresh checkout serves until the
	// frontend is built; it must carry the "needs building" message.
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	if want := "AgentTunnel web UI needs building"; !strings.Contains(string(body), want) {
		t.Fatalf("placeholder index does not mention %q", want)
	}
}
