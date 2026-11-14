package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
	"github.com/coder/websocket"
)

// Remote profiles and the agent-session env live on the developer's machine. The
// whole package runs with a throwaway home and no inherited AT_DAEMON_URL or
// AT_TOKEN, so no test can read a real profile or dial a real remote.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "agenttunnel-cli-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	os.Setenv("APPDATA", filepath.Join(home, "AppData"))
	os.Unsetenv("AT_DAEMON_URL")
	os.Unsetenv("AT_TOKEN")
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// isolateConfig points the user config dir at a fresh temp directory for one test.
func isolateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	t.Setenv("APPDATA", filepath.Join(dir, "AppData"))
}

// fakeDaemon is an httptest WebSocket server that behaves like atd for the two
// RPCs the remote commands use: it requires the Bearer token, answers app.info
// with its version, and answers every other call with an empty result.
type fakeDaemon struct {
	WS   string // ws://.../ws
	HTTP string // http://... base, as AT_DAEMON_URL carries it in agent sessions

	mu   sync.Mutex
	auth []string
}

func newFakeDaemon(t *testing.T, token, version string) *fakeDaemon {
	t.Helper()
	fd := &fakeDaemon{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			http.NotFound(w, r)
			return
		}
		fd.mu.Lock()
		fd.auth = append(fd.auth, r.Header.Get("Authorization"))
		fd.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			_, data, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var req struct {
				ID     string `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(data, &req)
			result := `{}`
			if req.Method == "app.info" {
				result = fmt.Sprintf(`{"version":%q}`, version)
			}
			reply := fmt.Sprintf(`{"id":%q,"result":%s}`, req.ID, result)
			if err := ws.Write(r.Context(), websocket.MessageText, []byte(reply)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	fd.HTTP = srv.URL
	fd.WS = "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	return fd
}

// authHeaders returns the Authorization header of every upgrade request seen.
func (fd *fakeDaemon) authHeaders() []string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return append([]string(nil), fd.auth...)
}

// runRemote invokes one `agenttunnel remote <sub>` leaf with stdin and returns
// what it printed.
func runRemote(t *testing.T, stdin string, sub string, args ...string) (string, error) {
	t.Helper()
	group, ok := findCommand(registry, "remote")
	if !ok {
		t.Fatal("remote command is not registered")
	}
	cmd, ok := findCommand(group.Subcommands, sub)
	if !ok {
		t.Fatalf("remote %s is not registered", sub)
	}
	var out bytes.Buffer
	env := &Env{In: strings.NewReader(stdin), Out: &out, Err: &out, HomeDir: t.TempDir()}
	err := cmd.Run(context.Background(), env, args)
	return out.String(), err
}

func profilesPath(t *testing.T) string {
	t.Helper()
	path, err := remotesPath()
	if err != nil {
		t.Fatalf("remotesPath: %v", err)
	}
	return path
}

func TestRemoteAddStoresTokenPrivatelyAndNeverPrintsIt(t *testing.T) {
	isolateConfig(t)
	const secret = "tok-very-secret"

	out, err := runRemote(t, secret+"\n", "add", "box", "wss://box.example.com/ws", "--token-stdin")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("add printed the token: %q", out)
	}

	data, err := os.ReadFile(profilesPath(t))
	if err != nil {
		t.Fatalf("profile file: %v", err)
	}
	if !strings.Contains(string(data), secret) {
		t.Fatal("token was not stored in the profile file")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(profilesPath(t))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("profile file mode = %o, want 600", perm)
		}
	}

	// Listing shows that a token is saved, never the token, in both renderings.
	listOut, err := runRemote(t, "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(listOut, secret) || !strings.Contains(listOut, "yes") {
		t.Fatalf("list output = %q", listOut)
	}
	var jsonOut bytes.Buffer
	if code := Run([]string{"--json", "--home-dir", t.TempDir(), "remote", "list"}, "test", &jsonOut, &jsonOut); code != 0 {
		t.Fatalf("json list exit = %d: %s", code, jsonOut.String())
	}
	if strings.Contains(jsonOut.String(), secret) {
		t.Fatalf("json list leaked the token: %s", jsonOut.String())
	}
}

func TestRemoteAddReadsTokenFromStdinThroughRun(t *testing.T) {
	isolateConfig(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = saved; r.Close() })
	go func() {
		io.WriteString(w, "from-pipe\n")
		w.Close()
	}()

	var out bytes.Buffer
	if code := Run([]string{"remote", "add", "pipe", "wss://pipe.example.com/ws", "--token-stdin"}, "test", &out, &out); code != 0 {
		t.Fatalf("exit = %d: %s", code, out.String())
	}
	store, err := loadRemotes()
	if err != nil {
		t.Fatal(err)
	}
	if i := store.index("pipe"); i < 0 || store.Remotes[i].Token != "from-pipe" {
		t.Fatalf("stored profiles = %+v", store.Remotes)
	}
}

func TestRemoteAddRefusesPlaintextRemotesAndBadNames(t *testing.T) {
	isolateConfig(t)
	cases := []struct {
		name, url string
		ok        bool
	}{
		{"ok-remote", "wss://box.example.com/ws", true},
		{"loopback", "ws://127.0.0.1:7331/ws", true},
		{"plain-public", "ws://box.example.com/ws", false},
		{"http-scheme", "https://box.example.com/ws", false},
		{"no-host", "wss:///ws", false},
		{"-bad", "wss://box.example.com/ws", false},
		{"has space", "wss://box.example.com/ws", false},
	}
	for _, tc := range cases {
		_, err := runRemote(t, "", "add", tc.name, tc.url)
		if (err == nil) != tc.ok {
			t.Errorf("add %q %q: err = %v, want ok=%v", tc.name, tc.url, err, tc.ok)
		}
	}
}

func TestRemoteAddRefusesDuplicateAndEmptyStdin(t *testing.T) {
	isolateConfig(t)
	if _, err := runRemote(t, "tok\n", "add", "box", "wss://box.example.com/ws", "--token-stdin"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := runRemote(t, "tok\n", "add", "box", "wss://other.example.com/ws", "--token-stdin"); err == nil {
		t.Fatal("duplicate name was accepted")
	}
	if _, err := runRemote(t, "  \n", "add", "empty", "wss://e.example.com/ws", "--token-stdin"); err == nil {
		t.Fatal("empty --token-stdin was accepted")
	}
}

func TestRemoteUseListRmManageTheActiveProfile(t *testing.T) {
	isolateConfig(t)
	for _, name := range []string{"alpha", "beta"} {
		if _, err := runRemote(t, "", "add", name, "wss://"+name+".example.com/ws"); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}
	if _, err := runRemote(t, "", "use", "beta"); err != nil {
		t.Fatalf("use: %v", err)
	}
	if _, err := runRemote(t, "", "use", "missing"); err == nil {
		t.Fatal("use of an unknown remote succeeded")
	}
	list, err := runRemote(t, "", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list, "*") || !strings.Contains(list, "beta") {
		t.Fatalf("list does not mark beta active:\n%s", list)
	}

	// Removing the active profile clears the selection.
	if _, err := runRemote(t, "", "rm", "beta"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	store, err := loadRemotes()
	if err != nil {
		t.Fatal(err)
	}
	if store.Active != "" || len(store.Remotes) != 1 || store.Remotes[0].Name != "alpha" {
		t.Fatalf("after rm: %+v", store)
	}
	if _, err := runRemote(t, "", "rm", "beta"); err == nil {
		t.Fatal("removing an unknown remote succeeded")
	}
}

// With no flag, no AT_DAEMON_URL and an active profile, Connect dials that remote
// with the stored token as a Bearer header and never starts a local daemon.
func TestConnectUsesActiveRemoteWithoutFlags(t *testing.T) {
	isolateConfig(t)
	fd := newFakeDaemon(t, "tok-remote", "9.9.9-remote")
	if _, err := runRemote(t, "tok-remote\n", "add", "box", fd.WS, "--token-stdin"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runRemote(t, "", "use", "box"); err != nil {
		t.Fatalf("use: %v", err)
	}

	stateDir := t.TempDir()
	env := &Env{HomeDir: stateDir}
	c, info, err := env.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()
	if info.WSURL != fd.WS {
		t.Fatalf("connected to %s, want %s", info.WSURL, fd.WS)
	}
	var got struct {
		Version string `json:"version"`
	}
	if err := c.Call(context.Background(), "app.info", nil, &got); err != nil || got.Version != "9.9.9-remote" {
		t.Fatalf("app.info = %+v, %v", got, err)
	}
	if auth := fd.authHeaders(); len(auth) == 0 || auth[0] != "Bearer tok-remote" {
		t.Fatalf("Authorization headers = %q, want Bearer token", auth)
	}
	if _, err := os.Stat(discovery.Path(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("a local daemon was autostarted for a remote target (stat err %v)", err)
	}
}

// Resolution order: --daemon beats AT_DAEMON_URL, which beats the active profile.
// Each daemon reports a different version so the test can see which one answered.
func TestConnectResolutionOrder(t *testing.T) {
	isolateConfig(t)
	active := newFakeDaemon(t, "tok", "v-active")
	fromEnv := newFakeDaemon(t, "tok", "v-env")
	fromFlag := newFakeDaemon(t, "tok", "v-flag")
	t.Setenv("AT_TOKEN", "tok")

	if _, err := runRemote(t, "tok\n", "add", "active", active.WS, "--token-stdin"); err != nil {
		t.Fatal(err)
	}
	if _, err := runRemote(t, "", "use", "active"); err != nil {
		t.Fatal(err)
	}

	version := func(env *Env) string {
		t.Helper()
		c, _, err := env.Connect(context.Background())
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer c.Close()
		var got struct {
			Version string `json:"version"`
		}
		if err := c.Call(context.Background(), "app.info", nil, &got); err != nil {
			t.Fatalf("app.info: %v", err)
		}
		return got.Version
	}

	if v := version(&Env{HomeDir: t.TempDir()}); v != "v-active" {
		t.Fatalf("no flag, no env: answered by %q", v)
	}

	// AT_DAEMON_URL is the HTTP base agent sessions receive; the CLI maps it to /ws.
	t.Setenv("AT_DAEMON_URL", fromEnv.HTTP)
	if v := version(&Env{HomeDir: t.TempDir()}); v != "v-env" {
		t.Fatalf("AT_DAEMON_URL set: answered by %q", v)
	}

	if v := version(&Env{HomeDir: t.TempDir(), DaemonURL: fromFlag.WS}); v != "v-flag" {
		t.Fatalf("--daemon set: answered by %q", v)
	}
}

func TestConnectRejectsCorruptProfileFileInsteadOfGoingLocal(t *testing.T) {
	isolateConfig(t)
	if err := os.MkdirAll(filepath.Dir(profilesPath(t)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilesPath(t), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	env := &Env{HomeDir: stateDir}
	if _, _, err := env.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Fatalf("err = %v, want a profile-file error", err)
	}
	if _, err := os.Stat(discovery.Path(stateDir)); !os.IsNotExist(err) {
		t.Fatal("a local daemon was started over a broken profile file")
	}
}

func TestRemotePingReportsVersionAndLatency(t *testing.T) {
	isolateConfig(t)
	const token = "tok-ping-secret"
	fd := newFakeDaemon(t, token, "1.2.3-test")
	if _, err := runRemote(t, token+"\n", "add", "box", fd.WS, "--token-stdin"); err != nil {
		t.Fatal(err)
	}

	out, err := runRemote(t, "", "ping", "box")
	if err != nil {
		t.Fatalf("ping: %v (%s)", err, out)
	}
	if !strings.Contains(out, "1.2.3-test") || !strings.Contains(out, "round trip") {
		t.Fatalf("ping output = %q", out)
	}
	if strings.Contains(out, token) {
		t.Fatalf("ping printed the token: %q", out)
	}
	if auth := fd.authHeaders(); len(auth) == 0 || auth[len(auth)-1] != "Bearer "+token {
		t.Fatalf("ping Authorization = %q", auth)
	}

	// The same ping through the active profile, with no name, and JSON output.
	if _, err := runRemote(t, "", "use", "box"); err != nil {
		t.Fatal(err)
	}
	var jsonOut bytes.Buffer
	if code := Run([]string{"--json", "--home-dir", t.TempDir(), "remote", "ping"}, "test", &jsonOut, &jsonOut); code != 0 {
		t.Fatalf("json ping exit = %d: %s", code, jsonOut.String())
	}
	var report struct {
		Version     string `json:"version"`
		RoundTripUs int64  `json:"roundTripUs"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &report); err != nil {
		t.Fatalf("json ping: %v: %s", err, jsonOut.String())
	}
	if report.Version != "1.2.3-test" || report.RoundTripUs < 0 {
		t.Fatalf("json ping report = %+v", report)
	}
}

func TestRemotePingRejectsWrongTokenWithoutEchoingIt(t *testing.T) {
	isolateConfig(t)
	fd := newFakeDaemon(t, "right-token", "1.0.0")
	if _, err := runRemote(t, "wrong-token\n", "add", "box", fd.WS, "--token-stdin"); err != nil {
		t.Fatal(err)
	}
	out, err := runRemote(t, "", "ping", "box")
	if err == nil {
		t.Fatalf("ping with a wrong token succeeded: %s", out)
	}
	if strings.Contains(err.Error(), "wrong-token") || strings.Contains(out, "wrong-token") {
		t.Fatal("ping echoed the token")
	}
}

func TestRemotePingWithoutTargetIsUsageErrorAndNeverAutostarts(t *testing.T) {
	isolateConfig(t)
	stateDir := t.TempDir()
	var out bytes.Buffer
	env := &Env{Out: &out, Err: &out, HomeDir: stateDir}
	group, _ := findCommand(registry, "remote")
	ping, _ := findCommand(group.Subcommands, "ping")
	err := ping.Run(context.Background(), env, nil)
	if err == nil || !strings.Contains(err.Error(), "no remote daemon selected") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(discovery.Path(stateDir)); !os.IsNotExist(statErr) {
		t.Fatal("ping started a local daemon")
	}
}

func TestEnvDaemonURLMapsHTTPBaseToWebSocketRoute(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:7331":      "ws://127.0.0.1:7331/ws",
		"https://box.example.com":    "wss://box.example.com/ws",
		"ws://127.0.0.1:7331/ws":     "ws://127.0.0.1:7331/ws",
		"wss://box.example.com/ws":   "wss://box.example.com/ws",
		"http://127.0.0.1:7331/base": "ws://127.0.0.1:7331/base",
	}
	for in, want := range cases {
		got, err := envDaemonURL(in)
		if err != nil || got != want {
			t.Errorf("envDaemonURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := envDaemonURL("ftp://x"); err == nil {
		t.Error("ftp scheme was accepted")
	}
}

func TestValidateRemoteURLRequiresTLSOffLoopback(t *testing.T) {
	for raw, ok := range map[string]bool{
		"wss://box.example.com/ws": true,
		"ws://localhost:7331/ws":   true,
		"ws://[::1]:7331/ws":       true,
		"ws://10.0.0.5:7331/ws":    false,
		"http://box.example.com":   false,
	} {
		if err := validateRemoteURL(raw); (err == nil) != ok {
			t.Errorf("validateRemoteURL(%q) = %v, want ok=%v", raw, err, ok)
		}
	}
}
