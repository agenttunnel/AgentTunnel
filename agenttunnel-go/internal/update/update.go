// Package update finds a newer AgentTunnel release on GitHub and installs it
// over the running build. Each release asset's sha256 digest, as reported by
// the GitHub API, is the checksum a download must match before anything is
// replaced.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DefaultRepo is the GitHub repository whose releases are installed.
const DefaultRepo = "agenttunnel/AgentTunnel"

// Install kinds. A macOS desktop build replaces its whole app bundle; every
// other build replaces the AgentTunnel binaries beside the running one.
const (
	KindApp = "app"
	KindBin = "bin"
)

// Install is where the running build lives and therefore what an update replaces.
type Install struct {
	Kind string
	// Path is the app bundle for KindApp and the directory holding the
	// binaries for KindBin.
	Path string
}

// Locate maps the running executable to its install. goos is passed in so the
// mapping stays testable on any platform.
func Locate(goos, exe string) Install {
	if goos == "darwin" {
		if i := strings.Index(exe, ".app/Contents/MacOS/"); i >= 0 {
			return Install{Kind: KindApp, Path: exe[:i+len(".app")]}
		}
	}
	return Install{Kind: KindBin, Path: filepath.Dir(exe)}
}

// Release is the subset of the GitHub release object the updater reads.
type Release struct {
	Tag    string  `json:"tag_name"`
	Draft  bool    `json:"draft"`
	Body   string  `json:"body"`
	Assets []Asset `json:"assets"`
}

// ReleaseNotes describe the build an Apply installed. They are kept on disk
// until a build of Version starts, then handed out once by TakeReleaseNotes.
type ReleaseNotes struct {
	Version string `json:"version"`
	Body    string `json:"body"`
}

// Asset is a downloadable release file. Digest is "sha256:<hex>".
type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// Status is what a client shows: the running version and the newest release
// that can be installed on this platform.
type Status struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available"`
	// Installed is set by Apply to the version now on disk. The running
	// process keeps its old build until it restarts.
	Installed string `json:"installed,omitempty"`
}

// Updater checks GitHub releases and installs them. Fields other than
// Version are exported so tests can point them at a fake server.
type Updater struct {
	Version string
	Repo    string
	APIBase string
	HTTP    *http.Client
	Install Install
	GOOS    string
	GOARCH  string
	// NotesPath is the file Apply writes release notes to. Empty disables
	// the notes feature.
	NotesPath string

	notesMu sync.Mutex
}

// New describes the running executable as an update target. version is the
// release version embedded at link time; "dev" or "" disables updates.
func New(version string) (*Updater, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return &Updater{
		Version: version,
		Repo:    DefaultRepo,
		APIBase: "https://api.github.com",
		HTTP:    &http.Client{Timeout: 15 * time.Minute},
		Install: Locate(runtime.GOOS, exe),
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
	}, nil
}

// Check reports whether a newer build is available. It makes no changes.
func (u *Updater) Check(ctx context.Context) (Status, error) {
	st, _, err := u.plan(ctx)
	return st, err
}

// Apply installs the newest available build if there is one. Errors leave
// the installed files untouched, except a failure partway through replacing
// several binaries, which is reported as an error.
func (u *Updater) Apply(ctx context.Context) (Status, error) {
	st, rel, err := u.plan(ctx)
	if err != nil || !st.Available {
		return st, err
	}
	// plan reports Available only when this asset exists.
	asset, _ := findAsset(rel, u.assetName())
	if err := u.install(ctx, asset); err != nil {
		return st, err
	}
	st.Installed = st.Latest
	if err := u.saveNotes(ReleaseNotes{Version: st.Latest, Body: rel.Body}); err != nil {
		return st, fmt.Errorf("update installed, but saving release notes failed: %w", err)
	}
	return st, nil
}

func (u *Updater) plan(ctx context.Context) (Status, Release, error) {
	st := Status{Current: u.Version, Enabled: enabled(u.Version)}
	if !st.Enabled {
		return st, Release{}, nil
	}
	rels, err := u.releases(ctx)
	if err != nil {
		return st, Release{}, err
	}
	// Only the newest release in this build's channel is considered. If it
	// has no build for this platform we offer nothing, rather than falling
	// back to an older release and offering a downgrade.
	rel, ok := newest(rels, isMainVersion(u.Version))
	if !ok {
		return st, Release{}, nil
	}
	if _, ok := findAsset(rel, u.assetName()); !ok {
		return st, Release{}, nil
	}
	st.Latest = versionOf(rel.Tag)
	st.Available = st.Latest != u.Version
	return st, rel, nil
}

// TakeReleaseNotes returns the notes saved by Apply if they describe the
// running build, and clears them so they are handed out once. Notes for any
// other version stay on disk until the matching build starts.
func (u *Updater) TakeReleaseNotes() (ReleaseNotes, bool, error) {
	u.notesMu.Lock()
	defer u.notesMu.Unlock()
	if u.NotesPath == "" {
		return ReleaseNotes{}, false, nil
	}
	b, err := os.ReadFile(u.NotesPath)
	if errors.Is(err, fs.ErrNotExist) {
		return ReleaseNotes{}, false, nil
	}
	if err != nil {
		return ReleaseNotes{}, false, fmt.Errorf("read release notes: %w", err)
	}
	var n ReleaseNotes
	if err := json.Unmarshal(b, &n); err != nil {
		return ReleaseNotes{}, false, fmt.Errorf("decode release notes: %w", err)
	}
	if n.Version != u.Version {
		return ReleaseNotes{}, false, nil
	}
	if err := os.Remove(u.NotesPath); err != nil {
		return ReleaseNotes{}, false, fmt.Errorf("clear release notes: %w", err)
	}
	return n, true, nil
}

// saveNotes writes the notes via a temp file and rename, so a crash never
// leaves a half-written file for TakeReleaseNotes to choke on.
func (u *Updater) saveNotes(n ReleaseNotes) error {
	if u.NotesPath == "" {
		return nil
	}
	u.notesMu.Lock()
	defer u.notesMu.Unlock()
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(u.NotesPath), 0o755); err != nil {
		return err
	}
	tmp := u.NotesPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, u.NotesPath)
}

func (u *Updater) releases(ctx context.Context) ([]Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=100", u.APIBase, u.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "agenttunnel-update")
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list releases: %s", resp.Status)
	}
	var rels []Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rels); err != nil {
		return nil, fmt.Errorf("decode releases: %w", err)
	}
	return rels, nil
}

// assetName is the release file this install is updated from.
func (u *Updater) assetName() string {
	if u.Install.Kind == KindApp {
		return "AgentTunnel-darwin-" + u.GOARCH + ".app.tar.gz"
	}
	return "agenttunnel-go-" + u.GOOS + "-" + u.GOARCH + ".tar.gz"
}

// binaryNames are the archive entries a KindBin install may replace.
func (u *Updater) binaryNames() []string {
	ext := ""
	if u.GOOS == "windows" {
		ext = ".exe"
	}
	return []string{"atd" + ext, "agenttunnel" + ext}
}

// enabled reports whether version identifies a release build.
func enabled(version string) bool {
	return version != "" && version != "dev"
}

var mainVersion = regexp.MustCompile(`^[0-9a-f]{7}$`)

// isMainVersion reports whether version is a main-branch build, which the
// release workflow names with a 7-character commit hash.
func isMainVersion(version string) bool {
	return mainVersion.MatchString(version)
}

// versionOf strips the tag prefix the release workflow adds. Tags are
// "main-<hash>" for main builds and "v<semver>" for tagged releases.
func versionOf(tag string) string {
	if v, ok := strings.CutPrefix(tag, "main-"); ok {
		return v
	}
	return strings.TrimPrefix(tag, "v")
}

func isMainTag(tag string) bool {
	return strings.HasPrefix(tag, "main-")
}

func newest(rels []Release, mainChannel bool) (Release, bool) {
	for _, r := range rels {
		if r.Draft || isMainTag(r.Tag) != mainChannel {
			continue
		}
		return r, true
	}
	return Release{}, false
}

func findAsset(rel Release, name string) (Asset, bool) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}
