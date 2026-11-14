package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
)

// remotesFileName is the saved-profile file under <user config dir>/agenttunnel.
// It holds bearer tokens, so it is written 0600 and never printed.
const remotesFileName = "remotes.json"

// remoteProfile is one saved remote daemon.
type remoteProfile struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

// remoteStore is the whole profile file: every saved remote and which one, if
// any, the CLI targets when no --daemon flag or AT_DAEMON_URL is set.
type remoteStore struct {
	Active  string          `json:"active,omitempty"`
	Remotes []remoteProfile `json:"remotes"`
}

func remotesPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config dir: %w", err)
	}
	return filepath.Join(dir, "agenttunnel", remotesFileName), nil
}

// loadRemotes reads the profile file. A missing file is an empty store; a file
// that exists but does not parse is an error, so a damaged config never silently
// sends commands to the local daemon.
func loadRemotes() (remoteStore, error) {
	path, err := remotesPath()
	if err != nil {
		return remoteStore{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return remoteStore{}, nil
	}
	if err != nil {
		return remoteStore{}, err
	}
	var s remoteStore
	if err := json.Unmarshal(data, &s); err != nil {
		return remoteStore{}, fmt.Errorf("%s is not valid: %w", path, err)
	}
	return s, nil
}

// saveRemotes replaces the profile file atomically, 0600.
func saveRemotes(s remoteStore) error {
	path, err := remotesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".remotes-*.json")
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
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s remoteStore) index(name string) int {
	for i, p := range s.Remotes {
		if p.Name == name {
			return i
		}
	}
	return -1
}

// byURL finds a saved profile by its WebSocket URL, so `--daemon <url>` can pick
// up that profile's token.
func (s remoteStore) byURL(wsURL string) (remoteProfile, bool) {
	for _, p := range s.Remotes {
		if p.URL == wsURL {
			return p, true
		}
	}
	return remoteProfile{}, false
}

// active returns the profile the CLI targets by default. An active name with no
// matching profile (a hand-edited file) counts as no active profile.
func (s remoteStore) active() (remoteProfile, bool) {
	if i := s.index(s.Active); s.Active != "" && i >= 0 {
		return s.Remotes[i], true
	}
	return remoteProfile{}, false
}

// validateRemoteURL requires wss:// for any host. Plain ws:// is accepted only
// for loopback, where no bearer token crosses the network.
func validateRemoteURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return usageError("remote URL must be a wss:// URL, got %q", raw)
	}
	switch u.Scheme {
	case "wss":
		return nil
	case "ws":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return usageError("plain ws:// is only allowed for loopback hosts; use wss:// for %s", u.Host)
	}
	return usageError("remote URL must be a wss:// URL, got %q", raw)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// envDaemonURL reads AT_DAEMON_URL. Agent sessions export it as the daemon's HTTP
// base (the /tool/call endpoint), so an http(s) base maps to its WebSocket route.
func envDaemonURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("AT_DAEMON_URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("AT_DAEMON_URL must be a ws, wss, http or https URL")
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/ws"
	}
	return u.String(), nil
}
