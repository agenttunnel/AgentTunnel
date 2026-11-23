package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestHandlerServesWebManifest pins the manifest a browser install reads: it
// must come back as a JSON manifest, not text/plain or an index fallback.
func TestHandlerServesWebManifest(t *testing.T) {
	const manifest = `{"name":"AgentTunnel","display":"standalone"}`
	fsys := fstest.MapFS{
		"index.html":           {Data: []byte(indexHTML)},
		"manifest.webmanifest": {Data: []byte(manifest)},
	}

	rec := httptest.NewRecorder()
	handler(fsys, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/manifest+json") {
		t.Fatalf("Content-Type = %q, want application/manifest+json", ct)
	}
	if body := rec.Body.String(); body != manifest {
		t.Fatalf("body = %q, want the manifest file, not the index", body)
	}
	var got struct {
		Name    string `json:"name"`
		Display string `json:"display"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("served manifest is not JSON: %v", err)
	}
	if got.Name != "AgentTunnel" || got.Display != "standalone" {
		t.Fatalf("manifest = %+v", got)
	}
}
