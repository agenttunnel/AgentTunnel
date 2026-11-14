package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type entry struct {
	name string
	body string
	dir  bool
	mode int64
}

func makeArchive(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.dir {
			hdr = &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeDir}
		} else if hdr.Mode == 0 {
			hdr.Mode = 0o644
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if !e.dir {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fakeGitHub serves a release list and the given download files.
type fakeGitHub struct {
	srv      *httptest.Server
	releases []Release
}

func newFakeGitHub(t *testing.T, files map[string][]byte) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+DefaultRepo+"/releases", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(g.releases)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) asset(name string, digest string, body []byte) Asset {
	return Asset{Name: name, URL: g.srv.URL + "/dl/" + name, Digest: digest, Size: int64(len(body))}
}

func (g *fakeGitHub) updater(version string, in Install, goos, goarch string) *Updater {
	return &Updater{
		Version: version,
		Repo:    DefaultRepo,
		APIBase: g.srv.URL,
		HTTP:    g.srv.Client(),
		Install: in,
		GOOS:    goos,
		GOARCH:  goarch,
	}
}

func release(tag string, draft bool, assets ...Asset) Release {
	return Release{Tag: tag, Draft: draft, Assets: assets}
}

func TestCheckChoosesNewestInChannel(t *testing.T) {
	linuxAsset := Asset{Name: "agenttunnel-go-linux-amd64.tar.gz"}
	darwinAsset := Asset{Name: "agenttunnel-go-darwin-arm64.tar.gz"}
	appAsset := Asset{Name: "AgentTunnel-darwin-arm64.app.tar.gz"}

	tests := []struct {
		name          string
		version       string
		releases      []Release
		wantEnabled   bool
		wantAvailable bool
		wantLatest    string
	}{
		{
			name:        "dev build never updates",
			version:     "dev",
			releases:    []Release{release("main-fff0000", false, linuxAsset)},
			wantEnabled: false,
		},
		{
			name:          "main build offered a newer main build",
			version:       "abc1234",
			releases:      []Release{release("main-fff0000", false, linuxAsset)},
			wantEnabled:   true,
			wantAvailable: true,
			wantLatest:    "fff0000",
		},
		{
			name:        "main build already on the newest main build",
			version:     "abc1234",
			releases:    []Release{release("main-abc1234", false, linuxAsset)},
			wantEnabled: true,
			wantLatest:  "abc1234",
		},
		{
			name:        "main build ignores stable tags",
			version:     "abc1234",
			releases:    []Release{release("v0.15.0", false, linuxAsset)},
			wantEnabled: true,
		},
		{
			name:          "stable build offered the newest tag, skipping main builds",
			version:       "0.14.0",
			releases:      []Release{release("main-abc1234", false, linuxAsset), release("v0.15.0", false, linuxAsset)},
			wantEnabled:   true,
			wantAvailable: true,
			wantLatest:    "0.15.0",
		},
		{
			name:        "drafts are skipped",
			version:     "abc1234",
			releases:    []Release{release("main-fff0000", true, linuxAsset), release("main-abc1234", false, linuxAsset)},
			wantEnabled: true,
			wantLatest:  "abc1234",
		},
		{
			name:        "newest release without a build here is not replaced by an older one",
			version:     "0.14.0",
			releases:    []Release{release("v0.15.0", false, darwinAsset), release("v0.13.0", false, linuxAsset)},
			wantEnabled: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGitHub(t, nil)
			g.releases = tc.releases
			u := g.updater(tc.version, Install{Kind: KindBin, Path: t.TempDir()}, "linux", "amd64")
			st, err := u.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if st.Enabled != tc.wantEnabled || st.Available != tc.wantAvailable || st.Latest != tc.wantLatest {
				t.Fatalf("status = %+v, want enabled=%v available=%v latest=%q",
					st, tc.wantEnabled, tc.wantAvailable, tc.wantLatest)
			}
		})
	}

	// The macOS app bundle is a separate asset from the CLI archive.
	g := newFakeGitHub(t, nil)
	g.releases = []Release{release("main-fff0000", false, appAsset)}
	u := g.updater("abc1234", Install{Kind: KindApp, Path: t.TempDir()}, "darwin", "arm64")
	if st, _ := u.Check(context.Background()); !st.Available {
		t.Fatalf("app install not offered the app asset: %+v", st)
	}
	u.Install.Kind = KindBin
	if st, _ := u.Check(context.Background()); st.Available {
		t.Fatalf("bin install offered an app-only release: %+v", st)
	}
}

func TestLocate(t *testing.T) {
	tests := []struct {
		goos, exe string
		want      Install
	}{
		{"darwin", "/Applications/AgentTunnel.app/Contents/MacOS/agenttunnel-desktop",
			Install{Kind: KindApp, Path: "/Applications/AgentTunnel.app"}},
		{"darwin", "/Users/me/.local/bin/atd", Install{Kind: KindBin, Path: "/Users/me/.local/bin"}},
		{"linux", "/opt/at/atd", Install{Kind: KindBin, Path: "/opt/at"}},
		{"linux", "/opt/x.app/Contents/MacOS/agenttunnel-desktop", Install{Kind: KindBin, Path: "/opt/x.app/Contents/MacOS"}},
	}
	for _, tc := range tests {
		if got := Locate(tc.goos, tc.exe); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Locate(%q, %q) = %+v, want %+v", tc.goos, tc.exe, got, tc.want)
		}
	}
}

func TestApplyReplacesBinariesAndKeepsSymlinks(t *testing.T) {
	archive := makeArchive(t,
		entry{name: "./", dir: true},
		entry{name: "./atd", body: "atd-new", mode: 0o755},
		entry{name: "./agenttunnel", body: "desktop-new", mode: 0o755},
		entry{name: "./VERSION", body: "abc1234\n"},
	)
	name := "agenttunnel-go-linux-amd64.tar.gz"
	g := newFakeGitHub(t, map[string][]byte{"/dl/" + name: archive})
	g.releases = []Release{release("main-fff0000", false, g.asset(name, digestOf(archive), archive))}

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "atd"), "atd-old")
	outside := filepath.Join(t.TempDir(), "agenttunnel")
	writeTestFile(t, outside, "linked-elsewhere")
	if err := os.Symlink(outside, filepath.Join(dir, "agenttunnel")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	u := g.updater("abc1234", Install{Kind: KindBin, Path: dir}, "linux", "amd64")
	st, err := u.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Installed != "fff0000" {
		t.Fatalf("Installed = %q, want fff0000", st.Installed)
	}
	if got := readFile(t, filepath.Join(dir, "atd")); got != "atd-new" {
		t.Fatalf("atd = %q, want atd-new", got)
	}
	if got := readFile(t, outside); got != "linked-elsewhere" {
		t.Fatalf("symlink target changed: %q", got)
	}
	info, err := os.Lstat(filepath.Join(dir, "agenttunnel"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("agenttunnel is no longer a symlink: %v %v", info, err)
	}
	if names := dirNames(t, dir); !reflect.DeepEqual(names, []string{"agenttunnel", "atd"}) {
		t.Fatalf("install dir = %v, want only the two binaries (no staging leftovers)", names)
	}
}

func TestApplyRejectsChecksumMismatch(t *testing.T) {
	archive := makeArchive(t, entry{name: "./atd", body: "atd-new", mode: 0o755})
	name := "agenttunnel-go-linux-amd64.tar.gz"
	g := newFakeGitHub(t, map[string][]byte{"/dl/" + name: archive})
	wrong := digestOf([]byte("something else"))
	g.releases = []Release{release("main-fff0000", false, g.asset(name, wrong, archive))}

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "atd"), "atd-old")
	u := g.updater("abc1234", Install{Kind: KindBin, Path: dir}, "linux", "amd64")
	if _, err := u.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Apply error = %v, want checksum mismatch", err)
	}
	if got := readFile(t, filepath.Join(dir, "atd")); got != "atd-old" {
		t.Fatalf("atd changed despite failed verification: %q", got)
	}
	if names := dirNames(t, dir); !reflect.DeepEqual(names, []string{"atd"}) {
		t.Fatalf("install dir = %v, want only atd", names)
	}
}

func TestApplyRefusesAssetWithoutDigest(t *testing.T) {
	archive := makeArchive(t, entry{name: "./atd", body: "atd-new", mode: 0o755})
	name := "agenttunnel-go-linux-amd64.tar.gz"
	g := newFakeGitHub(t, map[string][]byte{"/dl/" + name: archive})
	g.releases = []Release{release("main-fff0000", false, g.asset(name, "", archive))}

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "atd"), "atd-old")
	u := g.updater("abc1234", Install{Kind: KindBin, Path: dir}, "linux", "amd64")
	if _, err := u.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "no sha256 digest") {
		t.Fatalf("Apply error = %v, want refusal for missing digest", err)
	}
	if got := readFile(t, filepath.Join(dir, "atd")); got != "atd-old" {
		t.Fatalf("atd changed: %q", got)
	}
}

func TestApplyReplacesAppBundle(t *testing.T) {
	archive := makeArchive(t,
		entry{name: "AgentTunnel.app/", dir: true},
		entry{name: "AgentTunnel.app/Contents/MacOS/agenttunnel-desktop", body: "app-new", mode: 0o755},
		entry{name: "AgentTunnel.app/Contents/Info.plist", body: "plist-new"},
	)
	name := "AgentTunnel-darwin-arm64.app.tar.gz"
	g := newFakeGitHub(t, map[string][]byte{"/dl/" + name: archive})
	g.releases = []Release{release("main-fff0000", false, g.asset(name, digestOf(archive), archive))}

	root := t.TempDir()
	bundle := filepath.Join(root, "AgentTunnel.app")
	writeTestFile(t, filepath.Join(bundle, "Contents", "MacOS", "agenttunnel-desktop"), "app-old")
	writeTestFile(t, filepath.Join(bundle, "Contents", "Info.plist"), "plist-old")
	writeTestFile(t, filepath.Join(bundle, "Contents", "Resources", "stale.txt"), "gone")

	u := g.updater("abc1234", Install{Kind: KindApp, Path: bundle}, "darwin", "arm64")
	if _, err := u.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(bundle, "Contents", "MacOS", "agenttunnel-desktop")); got != "app-new" {
		t.Fatalf("binary = %q, want app-new", got)
	}
	if _, err := os.Stat(filepath.Join(bundle, "Contents", "Resources", "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale bundle file survived the swap: %v", err)
	}
	if names := dirNames(t, root); !reflect.DeepEqual(names, []string{"AgentTunnel.app"}) {
		t.Fatalf("root = %v, want only the bundle", names)
	}
}

func TestApplyRecordsReleaseNotesUntilNewBuildRuns(t *testing.T) {
	archive := makeArchive(t, entry{name: "./atd", body: "atd-new", mode: 0o755})
	name := "agenttunnel-go-linux-amd64.tar.gz"
	g := newFakeGitHub(t, map[string][]byte{"/dl/" + name: archive})
	g.releases = []Release{release("main-fff0000", false, g.asset(name, digestOf(archive), archive))}
	g.releases[0].Body = "## Changes\n- faster startup"

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "atd"), "atd-old")
	notes := filepath.Join(t.TempDir(), "update-notes.json")

	old := g.updater("abc1234", Install{Kind: KindBin, Path: dir}, "linux", "amd64")
	old.NotesPath = notes
	if _, err := old.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(notes); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Check wrote notes: %v", err)
	}
	if _, err := old.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The running process is still the old build, so the notes stay hidden.
	if _, ok, err := old.TakeReleaseNotes(); err != nil || ok {
		t.Fatalf("old build took notes: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(notes); err != nil {
		t.Fatalf("notes gone before the new build ran: %v", err)
	}

	// The restarted build reads them once.
	next := g.updater("fff0000", Install{Kind: KindBin, Path: dir}, "linux", "amd64")
	next.NotesPath = notes
	got, ok, err := next.TakeReleaseNotes()
	if err != nil || !ok {
		t.Fatalf("TakeReleaseNotes = ok %v, err %v; want notes", ok, err)
	}
	if got.Version != "fff0000" || got.Body != "## Changes\n- faster startup" {
		t.Fatalf("notes = %+v", got)
	}
	if _, ok, err := next.TakeReleaseNotes(); err != nil || ok {
		t.Fatalf("second take returned notes again: ok=%v err=%v", ok, err)
	}
}

func TestUnpackRejectsEscapingPaths(t *testing.T) {
	for _, name := range []string{"../evil", "/abs/evil"} {
		archive := makeArchive(t, entry{name: name, body: "x"})
		file := filepath.Join(t.TempDir(), "a.tar.gz")
		writeTestFile(t, file, string(archive))
		if err := unpack(file, t.TempDir()); err == nil {
			t.Fatalf("unpack accepted %q", name)
		}
	}
}

func writeTestFile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
