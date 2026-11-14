package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// tokenFileName is the default token file inside the state directory. A
// remote daemon reuses it across restarts so clients keep working.
const tokenFileName = "token"

// tokenSource records where the daemon's auth token came from. Only Path is
// printed at boot; the token value never reaches logs or argv.
type tokenSource struct {
	Value  string
	Path   string // empty unless the token lives in a file
	Origin string // "--token", "--token-file", "AT_DAEMON_TOKEN", or "state dir"
}

// resolveToken picks the auth token. Precedence: --token, --token-file,
// AT_DAEMON_TOKEN, then the token file in the state directory. The file path
// forms create the file (0600) on first use, so the token is stable across
// restarts.
func resolveToken(flagToken, flagFile, envToken, stateDir string) (tokenSource, error) {
	if flagToken != "" {
		return tokenSource{Value: flagToken, Origin: "--token"}, nil
	}
	if flagFile != "" {
		return loadOrCreateToken(flagFile, "--token-file")
	}
	if t := strings.TrimSpace(envToken); t != "" {
		return tokenSource{Value: t, Origin: "AT_DAEMON_TOKEN"}, nil
	}
	return loadOrCreateToken(filepath.Join(stateDir, tokenFileName), "state dir")
}

// loadOrCreateToken reads the token at path, trimming surrounding whitespace.
// A missing file gets a freshly generated token written with O_EXCL and 0600,
// so two racing starts cannot both persist different tokens.
func loadOrCreateToken(path, origin string) (tokenSource, error) {
	src := tokenSource{Path: path, Origin: origin}
	tok, err := readToken(path)
	if err == nil {
		src.Value = tok
		return src, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return tokenSource{}, err
	}
	gen, err := generateToken()
	if err != nil {
		return tokenSource{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return tokenSource{}, fmt.Errorf("token file: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		// Another start created it first; use its token.
		tok, err := readToken(path)
		if err != nil {
			return tokenSource{}, err
		}
		src.Value = tok
		return src, nil
	}
	if err != nil {
		return tokenSource{}, fmt.Errorf("token file: %w", err)
	}
	if _, err := f.WriteString(gen + "\n"); err != nil {
		f.Close()
		return tokenSource{}, fmt.Errorf("token file: %w", err)
	}
	if err := f.Close(); err != nil {
		return tokenSource{}, fmt.Errorf("token file: %w", err)
	}
	src.Value = gen
	return src, nil
}

func readToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return tok, nil
}

func generateToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
