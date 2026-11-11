package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// HarnessUpdate is one installed harness's version state. Command is for
// display only; upgrades always run the argv resolved below, never text from
// an RPC caller.
type HarnessUpdate struct {
	Harness         contracts.Harness `json:"harness"`
	Binary          string            `json:"binary,omitempty"`
	Installed       string            `json:"installed,omitempty"`
	Latest          string            `json:"latest,omitempty"`
	UpdateAvailable bool              `json:"updateAvailable"`
	Updatable       bool              `json:"updatable"`
	Reason          string            `json:"reason,omitempty"`
	Command         string            `json:"command,omitempty"`
}

// UpdateReport is every installed harness, in contracts.AllHarnesses order.
type UpdateReport struct {
	CheckedAt time.Time       `json:"checkedAt"`
	Harnesses []HarnessUpdate `json:"harnesses"`
}

// UpdateFailure is one harness whose upgrade command failed.
type UpdateFailure struct {
	Harness contracts.Harness `json:"harness"`
	Error   string            `json:"error"`
}

// UpdateResult is the outcome of one upgrade pass plus the refreshed report.
type UpdateResult struct {
	Updated []contracts.Harness `json:"updated"`
	Failed  []UpdateFailure     `json:"failed,omitempty"`
	Report  UpdateReport        `json:"report"`
}

const (
	harnessCheckTTL      = time.Hour
	harnessCheckFailTTL  = 10 * time.Minute
	harnessProbeTimeout  = 5 * time.Second
	harnessLatestTimeout = 5 * time.Second
	harnessUpdateTimeout = 10 * time.Minute
	harnessErrorTail     = 2048
	harnessBodyLimit     = 4 << 20
)

var (
	// Harness CLIs print versions as `opencode v2.0.29` or `2.1.322`; the
	// optional v keeps the leading word boundary from hiding `v2`.
	defaultVersionRe = regexp.MustCompile(`\bv?(\d+\.\d+\.\d+)`)
	cursorVersionRe  = regexp.MustCompile(`(\d{4}\.\d{2}\.\d{2}-[0-9a-f]+)`)
	cursorDownloadRe = regexp.MustCompile(`downloads\.cursor\.com/lab/(\d{4}\.\d{2}\.\d{2}-[0-9a-f]+)/`)
	semverRe         = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)
	cursorDateRe     = regexp.MustCompile(`^(\d{4})\.(\d{2})\.(\d{2})-[0-9a-f]+$`)
)

var pnpmGlobalPaths = []string{
	"/.local/share/pnpm/",
	"/library/pnpm/",
	"/local/share/pnpm/",
	"/appdata/local/pnpm/",
	"/pnpm/global/",
}

// vitePlusPaths covers both the legacy ~/.vite-plus directory and the current
// ~/.local/share/vite-plus global root.
var vitePlusPaths = []string{"/vite-plus/", "/.vite-plus/"}

var brewPrefixes = []string{"/opt/homebrew/", "/usr/local/"}

// latestSource names where a harness's published version comes from.
type latestSource int

const (
	latestNone latestSource = iota
	latestNPM
	latestCursorInstall
)

// updateTarget is one row of the harness upgrade table.
type updateTarget struct {
	harness     contracts.Harness
	binaries    []string
	npmPackage  string
	brewFormula string
	nativeArgs  []string
	nativePaths []string
	versionRe   *regexp.Regexp
	latest      latestSource
}

// updateTargets is the table Check and Update walk. Every contracts.Harness
// must have exactly one row.
func updateTargets() []updateTarget {
	return []updateTarget{
		{
			harness:  contracts.HarnessOpenCode,
			binaries: []string{"opencode"},
			// opencode-ai is the v1 channel (npm latest is 1.x). The v2 binary
			// that opencode.ai/v2/install drops in ~/.opencode/bin is published
			// as @opencode/cli, and its npm latest matches the version in the
			// native updater's own channel manifest.
			npmPackage:  "@opencode/cli",
			brewFormula: "anomalyco/tap/opencode",
			nativeArgs:  []string{"upgrade"},
			nativePaths: []string{"/.opencode/bin/", "/.local/bin/opencode"},
			latest:      latestNPM,
		},
		{
			harness:     contracts.HarnessCodex,
			binaries:    []string{"codex"},
			npmPackage:  "@openai/codex",
			brewFormula: "codex",
			latest:      latestNPM,
		},
		{
			harness:     contracts.HarnessClaude,
			binaries:    []string{"claude"},
			npmPackage:  "@anthropic-ai/claude-code",
			brewFormula: "claude-code",
			nativeArgs:  []string{"update"},
			nativePaths: []string{"/.local/share/claude/", "/.local/bin/claude"},
			latest:      latestNPM,
		},
		{
			harness:     contracts.HarnessCursor,
			binaries:    []string{"cursor-agent", "agent"},
			nativeArgs:  []string{"update"},
			nativePaths: []string{"/.local/share/cursor-agent/", "/.local/bin/cursor-agent", "/.local/bin/agent"},
			versionRe:   cursorVersionRe,
			latest:      latestCursorInstall,
		},
		{
			harness:    contracts.HarnessPi,
			binaries:   []string{"pi"},
			npmPackage: "@earendil-works/pi-coding-agent",
			nativeArgs: []string{"update"},
			// Homebrew's bin/pi is a symlink into the formula's node_modules
			// tree; the native verb upgrades it in place.
			nativePaths: []string{
				"/opt/homebrew/lib/node_modules/@earendil-works/pi-coding-agent/",
				"/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/",
			},
			latest: latestNPM,
		},
		{
			harness:    contracts.HarnessCommandCode,
			binaries:   []string{"commandcode", "cmd"},
			npmPackage: "command-code",
			latest:     latestNPM,
		},
	}
}

// HarnessUpdaterOptions injects the updater's boundaries so tests stay
// hermetic. Zero values fall back to real system and network access.
type HarnessUpdaterOptions struct {
	HTTP             *http.Client
	NpmBase          string
	CursorInstallURL string
	LookPath         func(string) (string, error)
	RealPath         func(string) (string, error)
	Probe            func(ctx context.Context, bin string, args ...string) ([]byte, error)
	Run              func(ctx context.Context, bin string, args ...string) ([]byte, error)
	Now              func() time.Time
}

// HarnessUpdater checks installed CLI versions against their published
// releases and upgrades them through the package manager that installed them.
type HarnessUpdater struct {
	targets []updateTarget
	entries []*updateEntry

	http             *http.Client
	npmBase          string
	cursorInstallURL string
	lookPath         func(string) (string, error)
	realPath         func(string) (string, error)
	probe            func(ctx context.Context, bin string, args ...string) ([]byte, error)
	run              func(ctx context.Context, bin string, args ...string) ([]byte, error)
	now              func() time.Time

	updateMu sync.Mutex
}

// upgradeCommand is the resolved argv for one harness. exe is the full path
// for native installs and the manager's name for package-manager installs.
type upgradeCommand struct {
	exe  string
	args []string
}

// harnessState is one harness's cached row plus the argv Update runs.
type harnessState struct {
	update  HarnessUpdate
	command upgradeCommand
	ok      bool
	failed  bool
}

type updateEntry struct {
	mu      sync.Mutex
	loaded  bool
	expires time.Time
	state   harnessState
}

// NewHarnessUpdater builds the updater with production defaults for anything
// the caller left unset.
func NewHarnessUpdater(opts HarnessUpdaterOptions) *HarnessUpdater {
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	if opts.NpmBase == "" {
		opts.NpmBase = "https://registry.npmjs.org"
	}
	if opts.CursorInstallURL == "" {
		opts.CursorInstallURL = "https://cursor.com/install"
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.RealPath == nil {
		opts.RealPath = filepath.EvalSymlinks
	}
	if opts.Probe == nil {
		opts.Probe = runCombined
	}
	if opts.Run == nil {
		opts.Run = runCombined
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	targets := updateTargets()
	entries := make([]*updateEntry, len(targets))
	for i := range entries {
		entries[i] = &updateEntry{}
	}
	return &HarnessUpdater{
		targets:          targets,
		entries:          entries,
		http:             opts.HTTP,
		npmBase:          opts.NpmBase,
		cursorInstallURL: opts.CursorInstallURL,
		lookPath:         opts.LookPath,
		realPath:         opts.RealPath,
		probe:            opts.Probe,
		run:              opts.Run,
		now:              opts.Now,
	}
}

// Check reports every installed harness, in table order. refresh bypasses the
// cache. A harness with no binary on PATH is omitted, not reported as broken.
func (u *HarnessUpdater) Check(ctx context.Context, refresh bool) UpdateReport {
	states := make([]harnessState, len(u.targets))
	var wg sync.WaitGroup
	for i := range u.targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			states[i] = u.entries[i].resolve(ctx, &u.targets[i], u, refresh)
		}()
	}
	wg.Wait()

	out := make([]HarnessUpdate, 0, len(states))
	for _, st := range states {
		if st.ok {
			out = append(out, st.update)
		}
	}
	return UpdateReport{CheckedAt: u.now(), Harnesses: out}
}

func (e *updateEntry) resolve(ctx context.Context, t *updateTarget, u *HarnessUpdater, refresh bool) harnessState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !refresh && e.loaded && u.now().Before(e.expires) {
		return e.state
	}
	at := u.now()
	st := u.checkOne(ctx, t)
	if !st.ok {
		return st
	}
	// A failure caused by the caller walking away says nothing about the
	// harness, so it must not be cached for everyone else.
	if st.failed && ctx.Err() != nil {
		return st
	}
	ttl := harnessCheckTTL
	if st.failed {
		ttl = harnessCheckFailTTL
	}
	e.state, e.loaded, e.expires = st, true, at.Add(ttl)
	return st
}

func (e *updateEntry) invalidate() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.loaded = false
}

func (u *HarnessUpdater) checkOne(ctx context.Context, t *updateTarget) harnessState {
	bin, err := u.findBinary(t)
	if err != nil {
		return harnessState{}
	}
	st := harnessState{ok: true, update: HarnessUpdate{Harness: t.harness, Binary: bin}}

	probeCtx, cancel := context.WithTimeout(ctx, harnessProbeTimeout)
	raw, err := u.probe(probeCtx, bin, "--version")
	cancel()
	if err == nil {
		st.update.Installed = parseVersion(t.versionRe, raw)
	}
	if st.update.Installed == "" {
		st.failed = true
		st.update.Reason = "could not read installed version"
	}

	if t.latest != latestNone {
		probeCtx, cancel = context.WithTimeout(ctx, harnessLatestTimeout)
		latest, latestErr := u.fetchLatest(probeCtx, t)
		cancel()
		st.update.Latest = latest
		if latestErr != nil || latest == "" {
			st.failed = true
			st.update.Reason = "could not check latest version"
		}
	}

	if st.update.Installed != "" && st.update.Latest != "" {
		switch compareVersions(st.update.Installed, st.update.Latest) {
		case versionNewer:
			st.update.UpdateAvailable = true
		case versionUnknown:
			st.failed = true
			if st.update.Reason == "" {
				st.update.Reason = "could not compare installed and latest versions"
			}
		}
	}

	// A harness binary is usually a symlink whose name hides its package
	// manager (pi -> homebrew node_modules, commandcode -> vite-plus), so
	// classification sees the symlink target too. The target is never run:
	// claude's realpath is a versioned binary that does not accept `update`.
	realBin, err := u.realPath(bin)
	if err != nil {
		realBin = ""
	}

	if cmd, ok := resolveUpgradeCommand(*t, bin, realBin); ok {
		st.command = cmd
		st.update.Updatable = true
		st.update.Command = cmd.display()
	} else if st.update.Reason == "" {
		st.update.Reason = "installed outside a known package manager"
	}
	return st
}

func (u *HarnessUpdater) findBinary(t *updateTarget) (string, error) {
	for _, name := range t.binaries {
		if path, err := u.lookPath(name); err == nil && path != "" {
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func (u *HarnessUpdater) fetchLatest(ctx context.Context, t *updateTarget) (string, error) {
	switch t.latest {
	case latestNPM:
		return u.fetchNPMLatest(ctx, t.npmPackage)
	case latestCursorInstall:
		return u.fetchCursorLatest(ctx)
	default:
		return "", nil
	}
}

func (u *HarnessUpdater) fetchNPMLatest(ctx context.Context, pkg string) (string, error) {
	if pkg == "" {
		return "", errors.New("no npm package")
	}
	endpoint := strings.TrimRight(u.npmBase, "/") + "/" + url.PathEscape(pkg) + "/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("accept", "application/json")
	resp, err := u.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("npm registry: %s", resp.Status)
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, harnessBodyLimit)).Decode(&payload); err != nil {
		return "", err
	}
	version := strings.TrimSpace(payload.Version)
	if version == "" {
		return "", errors.New("npm registry: empty version")
	}
	return version, nil
}

func (u *HarnessUpdater) fetchCursorLatest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.cursorInstallURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := u.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("cursor install: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, harnessBodyLimit))
	if err != nil {
		return "", err
	}
	match := cursorDownloadRe.FindSubmatch(body)
	if len(match) < 2 {
		return "", errors.New("cursor install: version not found")
	}
	return string(match[1]), nil
}

// Update upgrades the requested harnesses, or every affected harness when only
// is empty. Failures are recorded per harness and never abort the pass.
func (u *HarnessUpdater) Update(ctx context.Context, only []contracts.Harness) (UpdateResult, error) {
	u.updateMu.Lock()
	defer u.updateMu.Unlock()

	selected, err := u.selectTargets(ctx, only)
	if err != nil {
		return UpdateResult{}, err
	}

	res := UpdateResult{Updated: []contracts.Harness{}}
	for _, index := range selected {
		t := &u.targets[index]
		st := u.entries[index].resolve(ctx, t, u, false)
		if !st.ok {
			res.Failed = append(res.Failed, UpdateFailure{Harness: t.harness, Error: "not installed"})
			continue
		}
		if !st.update.Updatable {
			reason := st.update.Reason
			if reason == "" {
				reason = "no upgrade command"
			}
			res.Failed = append(res.Failed, UpdateFailure{Harness: t.harness, Error: reason})
			continue
		}
		// An explicit request still skips a harness that is known to be current
		// so a client cannot trigger minutes-long brew/vp runs for no change.
		if st.update.Installed != "" && st.update.Latest != "" && !st.update.UpdateAvailable {
			continue
		}

		// A half-finished package install must survive a disconnected client.
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), harnessUpdateTimeout)
		out, runErr := u.run(runCtx, st.command.exe, st.command.args...)
		cancel()
		u.entries[index].invalidate()
		if runErr != nil {
			res.Failed = append(res.Failed, UpdateFailure{
				Harness: t.harness,
				Error:   commandError(st.command, out, runErr),
			})
			continue
		}
		res.Updated = append(res.Updated, t.harness)
	}

	res.Report = u.Check(ctx, false)
	return res, nil
}

func (u *HarnessUpdater) selectTargets(ctx context.Context, only []contracts.Harness) ([]int, error) {
	if len(only) > 0 {
		out := make([]int, 0, len(only))
		seen := make(map[contracts.Harness]bool, len(only))
		for _, h := range only {
			index, ok := u.indexOf(h)
			if !ok {
				return nil, fmt.Errorf("unknown harness %q", h)
			}
			if seen[h] {
				continue
			}
			seen[h] = true
			out = append(out, index)
		}
		return out, nil
	}

	var out []int
	for _, row := range u.Check(ctx, false).Harnesses {
		if !row.UpdateAvailable || !row.Updatable {
			continue
		}
		if index, ok := u.indexOf(row.Harness); ok {
			out = append(out, index)
		}
	}
	return out, nil
}

func (u *HarnessUpdater) indexOf(h contracts.Harness) (int, bool) {
	for i := range u.targets {
		if u.targets[i].harness == h {
			return i, true
		}
	}
	return 0, false
}

// resolveUpgradeCommand picks the argv from the resolved install path, so a
// native install upgrades with its own verb and a package-managed one upgrades
// through the manager that owns it. bin stays the executable for a native
// upgrade; realBin is only used to classify managers hidden behind a symlink.
func resolveUpgradeCommand(t updateTarget, bin, realBin string) (upgradeCommand, bool) {
	paths := commandPaths(bin, realBin)
	if len(t.nativeArgs) > 0 && len(t.nativePaths) > 0 && matchesAnyPath(paths, t.nativePaths) {
		return upgradeCommand{exe: bin, args: append([]string(nil), t.nativeArgs...)}, true
	}
	if t.npmPackage != "" {
		// Order is load-bearing: a vite-plus realpath also contains
		// /node_modules/, so the vp check must win or vp installs get upgraded
		// into npm's prefix while vp keeps serving the stale binary.
		switch {
		case matchesAnyPath(paths, vitePlusPaths):
			return upgradeCommand{exe: "vp", args: []string{"i", "-g", t.npmPackage}}, true
		case matchesAnyPath(paths, []string{"/.bun/"}):
			return upgradeCommand{exe: "bun", args: []string{"i", "-g", t.npmPackage + "@latest"}}, true
		case matchesAnyPath(paths, pnpmGlobalPaths):
			return upgradeCommand{exe: "pnpm", args: []string{"add", "-g", t.npmPackage + "@latest"}}, true
		case matchesAnyPath(paths, []string{"/node_modules/"}):
			// npm 12 blocks lifecycle scripts by default and still exits 0, so
			// a package whose postinstall assembles the real binary (claude)
			// would be left half-installed while reporting success.
			return upgradeCommand{exe: "npm", args: []string{
				"install", "-g", "--allow-scripts=" + t.npmPackage, t.npmPackage + "@latest",
			}}, true
		}
	}
	if t.brewFormula != "" && matchesAnyPathPrefix(paths, brewPrefixes) {
		return upgradeCommand{exe: "brew", args: []string{"upgrade", t.brewFormula}}, true
	}
	return upgradeCommand{}, false
}

// commandPaths normalizes the LookPath path and its symlink target for
// classification, deduplicating when they are the same file.
func commandPaths(bin, realBin string) []string {
	paths := []string{normalizeCommandPath(bin)}
	if realBin != "" {
		real := normalizeCommandPath(realBin)
		if real != paths[0] {
			paths = append(paths, real)
		}
	}
	return paths
}

func (c upgradeCommand) display() string {
	return strings.Join(append([]string{filepath.Base(c.exe)}, c.args...), " ")
}

func normalizeCommandPath(path string) string {
	return strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
}

func matchesAny(path string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(path, needle) {
			return true
		}
	}
	return false
}

func matchesAnyPath(paths, needles []string) bool {
	for _, path := range paths {
		if matchesAny(path, needles) {
			return true
		}
	}
	return false
}

func matchesAnyPathPrefix(paths, prefixes []string) bool {
	for _, path := range paths {
		if matchesAnyPrefix(path, prefixes) {
			return true
		}
	}
	return false
}

func matchesAnyPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func parseVersion(re *regexp.Regexp, out []byte) string {
	if re == nil {
		re = defaultVersionRe
	}
	match := re.FindSubmatch(out)
	if len(match) < 2 {
		return ""
	}
	return string(match[1])
}

type versionComparison int

const (
	versionUnknown versionComparison = iota
	versionOlder
	versionEqual
	versionNewer
)

// compareVersions reports how latest sits relative to installed. Semver wins
// when both sides parse; Cursor's date-hash versions compare by date only.
func compareVersions(installed, latest string) versionComparison {
	if a, ok := cursorDateKey(installed); ok {
		b, ok := cursorDateKey(latest)
		if !ok {
			return versionUnknown
		}
		switch {
		case a < b:
			return versionNewer
		case a > b:
			return versionOlder
		default:
			return versionEqual
		}
	}
	a, ok := semverKey(installed)
	if !ok {
		return versionUnknown
	}
	b, ok := semverKey(latest)
	if !ok {
		return versionUnknown
	}
	for i := 0; i < 3; i++ {
		switch {
		case a[i] < b[i]:
			return versionNewer
		case a[i] > b[i]:
			return versionOlder
		}
	}
	return versionEqual
}

func semverKey(version string) ([3]int, bool) {
	match := semverRe.FindStringSubmatch(version)
	if match == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func cursorDateKey(version string) (int, bool) {
	match := cursorDateRe.FindStringSubmatch(version)
	if match == nil {
		return 0, false
	}
	key, err := strconv.Atoi(match[1] + match[2] + match[3])
	if err != nil {
		return 0, false
	}
	return key, true
}

func commandError(cmd upgradeCommand, out []byte, err error) string {
	msg := cmd.display() + ": "
	if text := strings.TrimSpace(string(out)); text != "" {
		msg += text + ": "
	}
	msg += err.Error()
	if len(msg) > harnessErrorTail {
		msg = "…" + msg[len(msg)-harnessErrorTail:]
	}
	return msg
}

func runCombined(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = nil
	return cmd.CombinedOutput()
}
