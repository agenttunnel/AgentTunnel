package relay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	relaywire "github.com/agenttunnel/agenttunnel-go/pkg/relay"
)

// identity is what the daemon keeps in <stateDir>/relay.json. The secret is the
// daemon's credential to the relay, so the file is written 0600 and never logged.
type identity struct {
	Instance string             `json:"instance"`
	Secret   string             `json:"secret"`
	Devices  []relaywire.Device `json:"devices"`
}

// newIdentity mints the instance id (128 bits) and daemon secret (256 bits).
func newIdentity() (*identity, error) {
	var inst [16]byte
	var secret [32]byte
	if _, err := rand.Read(inst[:]); err != nil {
		return nil, err
	}
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	return &identity{
		Instance: hex.EncodeToString(inst[:]),
		Secret:   hex.EncodeToString(secret[:]),
	}, nil
}

// loadIdentity reads the identity file. A missing file is not an error: relay
// use has never been enabled, so nothing is minted yet.
func loadIdentity(path string) (*identity, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var id identity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, err
	}
	return &id, nil
}

// saveIdentity writes the identity atomically with owner-only permissions.
func saveIdentity(path string, id *identity) error {
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "relay-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
