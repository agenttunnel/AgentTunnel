package transport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/prefs"
)

func prefsServer(t *testing.T) *Server {
	t.Helper()
	return &Server{token: "tok", prefs: prefs.Open(filepath.Join(t.TempDir(), "prefs.json"))}
}

func prefsRequest(t *testing.T, s *Server, method, body string, authed bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/prefs", strings.NewReader(body))
	if authed {
		req.Header.Set("Authorization", "Bearer tok")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestPrefsRequireTheDaemonToken(t *testing.T) {
	s := prefsServer(t)
	if rec := prefsRequest(t, s, http.MethodGet, "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET without token = %d, want 401", rec.Code)
	}
	if rec := prefsRequest(t, s, http.MethodPost, `{"at.theme":"nord"}`, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST without token = %d, want 401", rec.Code)
	}
}

func TestPrefsMergeAndDeleteAcrossReads(t *testing.T) {
	s := prefsServer(t)
	if rec := prefsRequest(t, s, http.MethodPost, `{"at.theme":"nord","at.layout":"{}"}`, true); rec.Code != http.StatusNoContent {
		t.Fatalf("POST = %d, want 204", rec.Code)
	}
	if rec := prefsRequest(t, s, http.MethodPost, `{"at.layout":null}`, true); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE via null = %d, want 204", rec.Code)
	}

	rec := prefsRequest(t, s, http.MethodGet, "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200", rec.Code)
	}
	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["at.theme"] != "nord" {
		t.Fatalf("GET = %+v, want only at.theme=nord", got)
	}
}

func TestPrefsRejectBadWrites(t *testing.T) {
	s := prefsServer(t)
	cases := map[string]string{
		"malformed body":  `{"bad`,
		"oversized value": fmt.Sprintf(`{"at.theme":%q}`, strings.Repeat("x", prefs.MaxValueBytes+1)),
		"empty key":       `{"":"x"}`,
	}
	for name, body := range cases {
		if rec := prefsRequest(t, s, http.MethodPost, body, true); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", name, rec.Code)
		}
	}
	if rec := prefsRequest(t, s, http.MethodPut, `{}`, true); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d, want 405", rec.Code)
	}
}

func TestPrefsUnavailableWithoutAStore(t *testing.T) {
	s := &Server{token: "tok"}
	if rec := prefsRequest(t, s, http.MethodGet, "", true); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET without a store = %d, want 503", rec.Code)
	}
}
