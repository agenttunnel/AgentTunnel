package transport

import (
	"encoding/json"
	"net/http"

	"github.com/agenttunnel/agenttunnel-go/internal/prefs"
)

// SetPrefs attaches the client preference store served at /prefs. Without one
// the route answers 503, so a daemon built without preferences still boots.
func (s *Server) SetPrefs(store *prefs.Store) { s.prefs = store }

// handlePrefs reads and writes the client preferences shared by every client
// of this daemon. GET returns the whole map; POST merges a JSON object of
// key → value, where null deletes the key. The bearer token is the boundary,
// as it is for /attachments: a browser page can only send it after /conn
// handed it over.
func (s *Server) handlePrefs(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.prefs == nil {
		http.Error(w, "preferences are unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(s.prefs.Snapshot())
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
		var changes map[string]*string
		if err := json.NewDecoder(r.Body).Decode(&changes); err != nil {
			http.Error(w, "preferences must be a JSON object", http.StatusBadRequest)
			return
		}
		for key, value := range changes {
			if !prefs.ValidKey(key) || (value != nil && !prefs.ValidValue(*value)) {
				http.Error(w, "invalid preference", http.StatusBadRequest)
				return
			}
		}
		if err := s.prefs.Apply(changes); err != nil {
			http.Error(w, "could not save preferences", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
