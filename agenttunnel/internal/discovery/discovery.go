// Package discovery reads and writes the daemon's discovery file: how a CLI
// process finds a running atd, its authenticated WebSocket endpoint, and the
// pid it may wait on. The daemon writes it after its listener is up and removes
// it on shutdown; a stale file is harmless because every reader pings the
// endpoint before trusting it.
package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileName is the discovery file inside the daemon's state directory.
const FileName = "daemon.json"

// Info is the running daemon's address and identity.
type Info struct {
	WSURL     string    `json:"wsUrl"`
	HTTPURL   string    `json:"httpUrl"`
	Token     string    `json:"token"`
	PID       int       `json:"pid"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"startedAt"`
}

// Path returns the discovery file's path inside stateDir.
func Path(stateDir string) string { return filepath.Join(stateDir, FileName) }

// Write replaces the discovery file atomically: a reader sees either the old
// file or the complete new one. The file holds the daemon's bearer token, so it
// is written 0600.
func Write(stateDir string, in Info) error {
	if strings.TrimSpace(in.WSURL) == "" {
		return errors.New("discovery: wsUrl is required")
	}
	if strings.TrimSpace(in.Token) == "" {
		return errors.New("discovery: token is required")
	}
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(stateDir, ".daemon-*.json")
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
	return os.Rename(tmp, Path(stateDir))
}

// Read returns the discovery file's contents. A missing file reports the
// underlying os error (so callers can use errors.Is(err, os.ErrNotExist)); an
// empty or malformed file is an error, never a zero-value daemon.
func Read(stateDir string) (Info, error) {
	var in Info
	data, err := os.ReadFile(Path(stateDir))
	if err != nil {
		return in, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return in, fmt.Errorf("discovery: %s is empty", Path(stateDir))
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return in, fmt.Errorf("discovery: %s: %w", Path(stateDir), err)
	}
	if strings.TrimSpace(in.WSURL) == "" || strings.TrimSpace(in.Token) == "" {
		return in, fmt.Errorf("discovery: %s is missing wsUrl or token", Path(stateDir))
	}
	return in, nil
}

// Remove deletes the discovery file. A missing file is not an error.
func Remove(stateDir string) error {
	err := os.Remove(Path(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
