// Package prefs stores client preferences beside daemon state, so every client
// of one environment (the desktop app, the web UI in any browser) shows the
// same theme, layout, and drafts.
//
// The daemon owns the file and serves it at /prefs. Each client keeps
// localStorage as its synchronous read cache, seeded from the daemon before
// the app starts, and mirrors later writes back.
package prefs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MaxValueBytes bounds one preference value. Drafts can be large; this matches
// what browsers allow localStorage to hold while keeping one runaway write
// from bloating the file.
const MaxValueBytes = 4 << 20

// Store is safe for concurrent use.
type Store struct {
	mu     sync.Mutex
	path   string
	values map[string]string
}

// Open loads preferences from path. A missing or malformed file reads as
// empty: every client must still boot when its preference file is unreadable.
func Open(path string) *Store {
	s := &Store{path: path, values: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var values map[string]string
	if json.Unmarshal(data, &values) != nil {
		return s
	}
	for k, v := range values {
		if ValidKey(k) && ValidValue(v) {
			s.values[k] = v
		}
	}
	return s
}

// Snapshot copies the saved pairs for seeding a client before it starts.
func (s *Store) Snapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out
}

// Apply merges changes into the store and rewrites the file. A nil value
// deletes the key. Changes stay in memory when the write fails so the current
// clients still see them; the next successful write persists the whole map.
func (s *Store) Apply(changes map[string]*string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range changes {
		if v == nil {
			delete(s.values, k)
			continue
		}
		s.values[k] = *v
	}
	return s.persistLocked()
}

// FetchFunc returns a snapshot source for a host that does not own the store,
// such as the web server or desktop attached to another daemon. A failed
// fetch seeds nothing rather than blocking the app's boot.
func FetchFunc(httpURL, token string) func() map[string]string {
	return func() map[string]string {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		values, err := Fetch(ctx, httpURL, token)
		if err != nil {
			return nil
		}
		return values
	}
}

// Fetch reads the daemon's saved preferences over its HTTP surface.
func Fetch(ctx context.Context, httpURL, token string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpURL+"/prefs", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /prefs: %s", res.Status)
	}
	var values map[string]string
	if err := json.NewDecoder(io.LimitReader(res.Body, MaxValueBytes*4)).Decode(&values); err != nil {
		return nil, err
	}
	return values, nil
}

// ValidKey reports whether a preference key is safe to store: short, non-empty,
// and free of control characters.
func ValidKey(key string) bool {
	if key == "" || len(key) > 200 {
		return false
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// ValidValue reports whether a preference value fits the store's bound.
func ValidValue(value string) bool { return len(value) <= MaxValueBytes }

// persistLocked writes the whole map atomically as 0600. A reader sees the old
// file or the complete new one, never a half-written mix.
func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	data, err := json.Marshal(s.values)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".prefs-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	// No fsync: writes arrive every few hundred milliseconds while a panel is
	// dragged, and a crash that loses the last one costs a single change.
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
