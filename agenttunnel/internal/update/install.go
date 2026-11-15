package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// maxAsset bounds one download. Release archives are tens of megabytes.
const maxAsset = 1 << 30

// install downloads and verifies asset, unpacks it beside the target, and
// swaps the new files in. Staging lives in the target's parent directory so
// every rename stays on one filesystem.
func (u *Updater) install(ctx context.Context, asset Asset) error {
	parent := u.Install.Path
	if u.Install.Kind == KindApp {
		parent = filepath.Dir(u.Install.Path)
	}

	work, err := os.MkdirTemp("", "agenttunnel-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	archive := filepath.Join(work, asset.Name)
	if err := u.download(ctx, asset, archive); err != nil {
		return err
	}

	staging, err := os.MkdirTemp(parent, ".agenttunnel-update-")
	if err != nil {
		return fmt.Errorf("staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := unpack(archive, staging); err != nil {
		return err
	}

	if u.Install.Kind == KindApp {
		return swapBundle(staging, u.Install.Path)
	}
	return u.swapBinaries(staging)
}

// download fetches asset into dst and fails unless its sha256 matches the
// digest GitHub reports. A release file without a digest is never installed.
func (u *Updater) download(ctx context.Context, a Asset, dst string) error {
	want, ok := strings.CutPrefix(a.Digest, "sha256:")
	if !ok || want == "" || a.URL == "" {
		return fmt.Errorf("release file %s has no sha256 digest; refusing to install it", a.Name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", a.Name, resp.Status)
	}

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxAsset+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", a.Name, err)
	}
	if n > maxAsset {
		return fmt.Errorf("download %s: larger than %d bytes", a.Name, int64(maxAsset))
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch for %s: got sha256:%s, want sha256:%s", a.Name, got, want)
	}
	return nil
}

// unpack extracts a .tar.gz into dst. Only directories and regular files are
// accepted; links and paths that escape dst are rejected.
func unpack(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(archive), err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", filepath.Base(archive), err)
		}
		clean := path.Clean(hdr.Name)
		if clean == "." {
			continue
		}
		local := filepath.FromSlash(clean)
		if !filepath.IsLocal(local) {
			return fmt.Errorf("unsafe archive path %q", hdr.Name)
		}
		target := filepath.Join(dst, local)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeFile(target, tr, hdr.FileInfo().Mode().Perm()|0o600); err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive entry %q: unsupported type %q", hdr.Name, hdr.Typeflag)
		}
	}
}

func writeFile(name string, r io.Reader, perm os.FileMode) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// swapBundle replaces the installed app bundle with the staged one. The old
// bundle is moved aside first and restored if the second rename fails.
func swapBundle(staging, bundle string) error {
	name := filepath.Base(bundle)
	fresh := filepath.Join(staging, name)
	if info, err := os.Stat(fresh); err != nil || !info.IsDir() {
		return fmt.Errorf("archive has no %s bundle", name)
	}
	previous := filepath.Join(staging, "previous")
	if err := os.Rename(bundle, previous); err != nil {
		return err
	}
	if err := os.Rename(fresh, bundle); err != nil {
		_ = os.Rename(previous, bundle)
		return err
	}
	return nil
}

// swapBinaries replaces each AgentTunnel binary that already exists as a
// regular file in the install directory. Symlinks are left alone: on macOS the
// CLI link points into the app bundle, which is updated separately.
func (u *Updater) swapBinaries(staging string) error {
	var replaced []string
	for _, name := range u.binaryNames() {
		dst := filepath.Join(u.Install.Path, name)
		if info, err := os.Lstat(dst); err != nil || !info.Mode().IsRegular() {
			continue
		}
		src := filepath.Join(staging, name)
		if info, err := os.Lstat(src); err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := os.Chmod(src, 0o755); err != nil {
			return err
		}
		if err := replaceFile(dst, src); err != nil {
			return fmt.Errorf("replace %s (after replacing %v): %w", name, replaced, err)
		}
		replaced = append(replaced, name)
	}
	if len(replaced) == 0 {
		return fmt.Errorf("no AgentTunnel binary in %s to replace", u.Install.Path)
	}
	return nil
}

// replaceFile moves src over dst. On Windows a running executable cannot be
// overwritten, so the old file is renamed aside first; the leftover is removed
// on the next update.
func replaceFile(dst, src string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(src, dst)
	}
	aside := dst + ".old"
	_ = os.Remove(aside)
	if err := os.Rename(dst, aside); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Rename(aside, dst)
		return err
	}
	_ = os.Remove(aside)
	return nil
}
