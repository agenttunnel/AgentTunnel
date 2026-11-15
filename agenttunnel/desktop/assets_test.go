package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The desktop window must open with the daemon's saved preferences already in
// the page, the same as the web UI, so it never flashes the default theme.
func TestClientAssetsSeedTheIndexWithSavedPrefs(t *testing.T) {
	h := clientAssets(func() map[string]string { return map[string]string{"at.theme": "nord"} })
	for _, path := range []string{"/", "/index.html"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"at.theme":"nord"`) {
			t.Fatalf("%s: status %d, body does not seed the saved theme", path, rec.Code)
		}
	}
}
