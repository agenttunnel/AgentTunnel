package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingGitHub is a fake release API that counts release-list requests. A
// non-nil gate holds each list request until the test closes it.
type countingGitHub struct {
	srv      *httptest.Server
	releases []Release
	hits     atomic.Int32
	fail     atomic.Bool
	gate     chan struct{}
	entered  chan struct{}
}

func newCountingGitHub(t *testing.T, files map[string][]byte) *countingGitHub {
	t.Helper()
	g := &countingGitHub{entered: make(chan struct{}, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+DefaultRepo+"/releases", func(w http.ResponseWriter, r *http.Request) {
		g.hits.Add(1)
		if g.gate != nil {
			select {
			case g.entered <- struct{}{}:
			default:
			}
			<-g.gate
		}
		if g.fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
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

func (g *countingGitHub) updater(version string, in Install, goos, goarch string) *Updater {
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

// clockAt returns a fixed clock the test advances by reassigning the pointer.
func clockAt(t time.Time) *time.Time { return &t }

func TestCheckReusesLookupWithinTTL(t *testing.T) {
	linux := Asset{Name: "agenttunnel-go-linux-amd64.tar.gz"}
	g := newCountingGitHub(t, nil)
	g.releases = []Release{release("main-fff0000", false, linux)}
	u := g.updater("abc1234", Install{Kind: KindBin, Path: t.TempDir()}, "linux", "amd64")
	u.probeOnDisk = func(context.Context) string { return "" }

	now := clockAt(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	u.now = func() time.Time { return *now }

	for range 3 {
		st, err := u.Check(context.Background())
		if err != nil || st.Latest != "fff0000" {
			t.Fatalf("Check = %+v, %v; want Latest fff0000", st, err)
		}
	}
	if got := g.hits.Load(); got != 1 {
		t.Fatalf("release lookups = %d for three checks, want 1", got)
	}

	*now = now.Add(planTTL - time.Second)
	if _, err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := g.hits.Load(); got != 1 {
		t.Fatalf("lookup repeated before the TTL elapsed: %d", got)
	}

	*now = now.Add(2 * time.Second)
	if _, err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := g.hits.Load(); got != 2 {
		t.Fatalf("release lookups after the TTL = %d, want 2", got)
	}
}

func TestConcurrentChecksShareOneLookup(t *testing.T) {
	linux := Asset{Name: "agenttunnel-go-linux-amd64.tar.gz"}
	g := newCountingGitHub(t, nil)
	g.releases = []Release{release("main-fff0000", false, linux)}
	g.gate = make(chan struct{})
	u := g.updater("abc1234", Install{Kind: KindBin, Path: t.TempDir()}, "linux", "amd64")
	u.probeOnDisk = func(context.Context) string { return "" }

	const callers = 5
	errs := make(chan error, callers)
	check := func() {
		_, err := u.Check(context.Background())
		errs <- err
	}

	// The first caller is held inside the release list request, so every
	// later caller arrives while that lookup is still in flight.
	go check()
	<-g.entered

	started := make(chan struct{}, callers)
	for range callers - 1 {
		go func() {
			started <- struct{}{}
			check()
		}()
	}
	for range callers - 1 {
		<-started
	}
	close(g.gate)

	for range callers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := g.hits.Load(); got != 1 {
		t.Fatalf("concurrent checks made %d lookups, want 1", got)
	}
}

func TestCheckDoesNotCacheFailures(t *testing.T) {
	linux := Asset{Name: "agenttunnel-go-linux-amd64.tar.gz"}
	g := newCountingGitHub(t, nil)
	g.releases = []Release{release("main-fff0000", false, linux)}
	g.fail.Store(true)
	u := g.updater("abc1234", Install{Kind: KindBin, Path: t.TempDir()}, "linux", "amd64")
	u.probeOnDisk = func(context.Context) string { return "" }
	now := clockAt(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	u.now = func() time.Time { return *now }

	if _, err := u.Check(context.Background()); err == nil {
		t.Fatal("Check against a failing API returned no error")
	}
	g.fail.Store(false)
	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check after recovery: %v", err)
	}
	if st.Latest != "fff0000" {
		t.Fatalf("Latest = %q after recovery, want fff0000", st.Latest)
	}
	if got := g.hits.Load(); got != 2 {
		t.Fatalf("lookups = %d, want 2 (the failure must not be cached)", got)
	}
}

func TestApplyBypassesCheckCacheAndInvalidatesIt(t *testing.T) {
	archive := makeArchive(t, entry{name: "./atd", body: "atd-new", mode: 0o755})
	name := "agenttunnel-go-linux-amd64.tar.gz"
	g := newCountingGitHub(t, map[string][]byte{"/dl/" + name: archive})
	g.releases = []Release{release("main-fff0000", false, Asset{
		Name:   name,
		URL:    g.srv.URL + "/dl/" + name,
		Digest: digestOf(archive),
		Size:   int64(len(archive)),
	})}

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "atd"), "atd-old")
	u := g.updater("abc1234", Install{Kind: KindBin, Path: dir}, "linux", "amd64")
	onDisk := "abc1234"
	u.probeOnDisk = func(context.Context) string { return onDisk }
	now := clockAt(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	u.now = func() time.Time { return *now }

	if st, err := u.Check(context.Background()); err != nil || !st.Available {
		t.Fatalf("Check = %+v, %v; want an update offered", st, err)
	}
	if got := g.hits.Load(); got != 1 {
		t.Fatalf("lookups after Check = %d, want 1", got)
	}

	st, err := u.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Installed != "fff0000" {
		t.Fatalf("Apply Installed = %q, want fff0000", st.Installed)
	}
	if got := g.hits.Load(); got != 2 {
		t.Fatalf("lookups after Apply = %d, want 2 (Apply must ask GitHub, not the cache)", got)
	}

	// The install replaced the binary. A cached pre-install answer would still
	// offer the update; the next Check must read the new on-disk state.
	onDisk = "fff0000"
	st, err = u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := g.hits.Load(); got != 3 {
		t.Fatalf("lookups after Apply and Check = %d, want 3 (cache must be invalidated)", got)
	}
	if st.Available || st.Installed != "fff0000" {
		t.Fatalf("Check after Apply = %+v, want restart-needed with Installed fff0000", st)
	}
}

// waitForJoiner blocks until one caller has joined the in-flight lookup. The
// condition is observed under planMu, so no timing is involved.
func waitForJoiner(u *Updater) {
	for {
		u.planMu.Lock()
		joined := u.planWait != nil && u.planWait.waiting > 0
		u.planMu.Unlock()
		if joined {
			return
		}
		runtime.Gosched()
	}
}

// A cancelled starter of a shared lookup must not hand context.Canceled to a
// caller that joined the same lookup with a live context.
func TestCancelledLeaderDoesNotFailJoiner(t *testing.T) {
	linux := Asset{Name: "agenttunnel-go-linux-amd64.tar.gz"}
	g := newCountingGitHub(t, nil)
	g.releases = []Release{release("main-fff0000", false, linux)}
	g.gate = make(chan struct{})
	closeGate := sync.OnceFunc(func() { close(g.gate) })
	t.Cleanup(closeGate)
	u := g.updater("abc1234", Install{Kind: KindBin, Path: t.TempDir()}, "linux", "amd64")
	u.probeOnDisk = func(context.Context) string { return "" }

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leaderErr := make(chan error, 1)
	go func() {
		_, err := u.Check(leaderCtx)
		leaderErr <- err
	}()
	// The lookup is now held inside the release list request.
	<-g.entered

	type result struct {
		st  Status
		err error
	}
	joiner := make(chan result, 1)
	go func() {
		st, err := u.Check(context.Background())
		joiner <- result{st, err}
	}()
	waitForJoiner(u)

	cancelLeader()
	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled leader returned %v, want context.Canceled", err)
	}
	closeGate()

	got := <-joiner
	if got.err != nil {
		t.Fatalf("joiner with a live context got error %v, want the shared result", got.err)
	}
	if got.st.Latest != "fff0000" {
		t.Fatalf("joiner Latest = %q, want fff0000", got.st.Latest)
	}
	if hits := g.hits.Load(); hits != 1 {
		t.Fatalf("release lookups = %d, want 1 (the joiner must share the lookup)", hits)
	}
}
