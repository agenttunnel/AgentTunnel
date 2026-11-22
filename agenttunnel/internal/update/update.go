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
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DefaultRepo is the GitHub repository whose releases are installed.
const DefaultRepo = "agenttunnel/AgentTunnel"

// planTTL bounds how long a release lookup is reused by Check. Every connected
// client polls update.check, and GitHub's unauthenticated API allows 60 calls
// an hour per IP, so the lookup is shared across clients rather than repeated.
const planTTL = 10 * time.Minute

// planLookupTimeout bounds one shared lookup. It outlives the caller that
// started it, so it needs its own limit; it covers the release list (30s) and
// the on-disk version probe (5s).
const planLookupTimeout = 45 * time.Second

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

	// probeOnDisk returns the version of the binary at the executable path,
	// or "" if it cannot be determined. nil runs the on-disk executable with
	// --version. A running process keeps its old build even after its file is
	// replaced, so this reads the version the next start would run — letting
	// Check detect an update the install script applied out of band.
	probeOnDisk func(context.Context) string

	// now is the clock the plan cache reads. nil means time.Now; tests set it.
	now func() time.Time

	notesMu sync.Mutex

	// planMu guards the Check cache: the last successful lookup, the lookup in
	// flight that concurrent callers join, and gen, which forgetPlan bumps so a
	// lookup started before an install cannot repopulate the cache afterwards.
	planMu   sync.Mutex
	planHit  *planEntry
	planWait *planCall
	gen      uint64
}

type planResult struct {
	st  Status
	rel Release
}

type planEntry struct {
	key string
	res planResult
	at  time.Time
}

// planCall is one release lookup that concurrent Check calls wait on. waiting
// counts the callers that joined it after the one that started it.
type planCall struct {
	key     string
	done    chan struct{}
	res     planResult
	err     error
	waiting int
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

// Check reports whether a newer build is available. It makes no changes. The
// release lookup is reused for planTTL, and concurrent callers share one lookup.
func (u *Updater) Check(ctx context.Context) (Status, error) {
	res, err := u.cachedPlan(ctx)
	return res.st, err
}

// cachedPlan returns the memoized lookup while it is fresh, joins a lookup
// already in flight, and otherwise starts one. Every caller, the one that
// started the lookup included, waits on its own ctx: the lookup itself is
// detached from any caller so a cancelled caller cannot fail the others.
// Errors are never cached.
func (u *Updater) cachedPlan(ctx context.Context) (planResult, error) {
	// The lookup depends on the channel and platform fields, so a changed
	// configuration never reuses another configuration's result.
	key := u.planKey()
	u.planMu.Lock()
	if e := u.planHit; e != nil && e.key == key && u.clock().Sub(e.at) < planTTL {
		u.planMu.Unlock()
		return e.res, nil
	}
	c := u.planWait
	if c != nil && c.key == key {
		c.waiting++
	} else {
		c = &planCall{key: key, done: make(chan struct{})}
		u.planWait = c
		go u.runPlan(ctx, c, u.gen)
	}
	u.planMu.Unlock()

	select {
	case <-c.done:
		return c.res, c.err
	case <-ctx.Done():
		return planResult{}, ctx.Err()
	}
}

// runPlan performs the lookup for c and publishes its result. It runs on a
// context detached from the caller that started it (values kept, cancellation
// dropped) under its own bound, so the result reaches every joined caller even
// if the starter has gone away.
func (u *Updater) runPlan(ctx context.Context, c *planCall, gen uint64) {
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), planLookupTimeout)
	defer cancel()
	st, rel, err := u.plan(lookupCtx)
	c.res, c.err = planResult{st: st, rel: rel}, err

	u.planMu.Lock()
	if u.planWait == c {
		u.planWait = nil
	}
	if err == nil && u.gen == gen {
		u.planHit = &planEntry{key: c.key, res: c.res, at: u.clock()}
	}
	u.planMu.Unlock()
	close(c.done)
}

// forgetPlan drops the memoized lookup after an install, so the next Check
// reads the new on-disk state instead of the pre-install result.
func (u *Updater) forgetPlan() {
	u.planMu.Lock()
	defer u.planMu.Unlock()
	u.planHit = nil
	u.planWait = nil
	u.gen++
}

func (u *Updater) planKey() string {
	return u.Repo + "|" + u.APIBase + "|" + u.Version + "|" + u.assetName()
}

func (u *Updater) clock() time.Time {
	if u.now != nil {
		return u.now()
	}
	return time.Now()
}

// Apply installs the newest available build if there is one. It always asks
// GitHub afresh rather than reusing the Check cache. Errors leave the installed
// files untouched, except a failure partway through replacing several binaries,
// which is reported as an error.
func (u *Updater) Apply(ctx context.Context) (Status, error) {
	st, rel, err := u.plan(ctx)
	if err != nil || !st.Available {
		return st, err
	}
	// plan reports Available only when this asset exists.
	asset, _ := findAsset(rel, u.assetName())
	err = u.install(ctx, asset)
	// A failed install may still have replaced some binaries, so the cached
	// lookup is stale either way.
	u.forgetPlan()
	if err != nil {
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
	// An external updater (the install script, or `atd update` in another
	// terminal) may have already replaced the on-disk binary. If the on-disk
	// version matches Latest, the only remaining step is a restart, so report
	// it as installed rather than available.
	if st.Available {
		if v := u.onDiskVersion(ctx); v != "" && v == st.Latest {
			st.Available = false
			st.Installed = st.Latest
		}
	}
	return st, rel, nil
}

// onDiskVersion reports the version of the binary at the executable path. A
// running process keeps its old build even after the file on disk is replaced
// (the kernel holds the inode), so the next execution of that path runs the
// new build. We exploit this by spawning the on-disk binary with --version.
func (u *Updater) onDiskVersion(ctx context.Context) string {
	if u.probeOnDisk != nil {
		return u.probeOnDisk(ctx)
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, exe, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return parseExeVersion(string(out))
}

// parseExeVersion extracts the version token from "--version" output, which is
// "atd <version>" or "agenttunnel-desktop <version>".
func parseExeVersion(out string) string {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
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
	return []string{"atd" + ext, "agenttunnel" + ext, "agenttunnel-desktop" + ext}
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
