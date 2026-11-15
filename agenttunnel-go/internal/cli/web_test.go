package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/daemonlock"
	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/pkg/daemon"
)

// stubOpenBrowser replaces the browser launcher with a recorder, so tests
// never spawn one.
func stubOpenBrowser(t *testing.T) *[]string {
	t.Helper()
	var opened []string
	orig := openBrowser
	openBrowser = func(u string) error {
		opened = append(opened, u)
		return nil
	}
	t.Cleanup(func() { openBrowser = orig })
	return &opened
}

// bootWeb starts an owned-mode web server in a fresh state dir and returns it
// with that state dir. The caller's t.Cleanup(w.Close) is registered here.
func bootWeb(t *testing.T, dir string) (*webServer, string) {
	t.Helper()
	stateDir := t.TempDir()
	env := &Env{Out: io.Discard, Err: io.Discard, HomeDir: stateDir, Version: "webtest"}
	w, err := startWeb(context.Background(), env, dir)
	if err != nil {
		t.Fatalf("startWeb: %v", err)
	}
	t.Cleanup(w.Close)
	return w, stateDir
}

// newWebTestDaemon boots a minimal in-process daemon like the e2e fixtures —
// with a scripted harness but no turns — and publishes it in the state dir's
// discovery file, so startWeb's attach probe finds it. It returns the
// daemon's ws URL, http URL, and token.
func newWebTestDaemon(t *testing.T, stateDir string) (wsURL, httpURL, token string) {
	t.Helper()
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{
			Binary: "sh", Args: []string{"-c", "printf '{\"type\":\"text\",\"text\":\"ok\"}\\n'"}, PromptVia: "stdin",
		}, nil
	}, providers.JSONLineTranslator{})
	d, err := daemon.New(context.Background(), daemon.Options{
		StateDir: stateDir,
		Token:    "webtest",
		Workdir:  t.TempDir(),
		Registry: providers.NewRegistry(driver),
		Config:   orchestrator.Config{MaxPasses: 1, MaxConcurrency: 1},
	})
	if err != nil {
		t.Fatalf("fixture daemon: %v", err)
	}
	srv := httptest.NewServer(d.Server.Handler())
	t.Cleanup(d.Close)
	t.Cleanup(srv.Close)
	wsURL = "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	if err := discovery.Write(stateDir, discovery.Info{
		WSURL: wsURL, HTTPURL: srv.URL, Token: "webtest",
		PID: os.Getpid(), Version: "webtest", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("fixture discovery: %v", err)
	}
	return wsURL, srv.URL, "webtest"
}

// getConn fetches and decodes GET /conn from a running web server.
func getConn(t *testing.T, w *webServer) map[string]json.RawMessage {
	t.Helper()
	res, err := http.Get(w.URL() + "/conn")
	if err != nil {
		t.Fatalf("GET /conn: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /conn = %s", res.Status)
	}
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("decode /conn: %v", err)
	}
	return payload
}

// connString decodes one /conn payload member as a string.
func connString(t *testing.T, payload map[string]json.RawMessage, key string) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(payload[key], &s); err != nil {
		t.Fatalf("decode /conn %s: %v", key, err)
	}
	return s
}

// dialWebQueryToken dials a ws URL with the token as ?token= instead of an
// Authorization header — the loopback form a browser client uses.
func dialWebQueryToken(ctx context.Context, wsURL, token string) (*websocket.Conn, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	ws, _, err := websocket.Dial(ctx, u.String(), nil)
	return ws, err
}

// wsProjectList performs one project.list round trip over a raw WebSocket,
// skipping the pushed event frames.
func wsProjectList(ctx context.Context, ws *websocket.Conn) error {
	req, err := json.Marshal(map[string]any{"id": "web-test", "method": "project.list"})
	if err != nil {
		return err
	}
	if err := ws.Write(ctx, websocket.MessageText, req); err != nil {
		return err
	}
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		var resp struct {
			ID    string `json:"id"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &resp) != nil || resp.ID != "web-test" {
			continue
		}
		if resp.Error != nil {
			return fmt.Errorf("project.list: %s", resp.Error.Message)
		}
		return nil
	}
}

func TestParseGlobalWebFlagForms(t *testing.T) {
	cases := []struct {
		name string
		args []string
		web  string
		json bool
		rest []string
	}{
		{"bare", []string{"--web"}, ".", false, nil},
		{"bare with dir", []string{"--web", "."}, ".", false, nil},
		{"equals", []string{"--web=x"}, "x", false, nil},
		{"equals empty", []string{"--web="}, ".", false, nil},
		{"bare before flag", []string{"--web", "--json"}, ".", true, nil},
		{"dir then command", []string{"--web", ".", "ps"}, ".", false, []string{"ps"}},
		{"command wins over path", []string{"--web", "ps"}, ".", false, []string{"ps"}},
	}
	for _, tc := range cases {
		env := &Env{}
		rest, help, err := parseGlobal(tc.args, env)
		if err != nil || help {
			t.Fatalf("%s: err=%v help=%v", tc.name, err, help)
		}
		if env.Web != tc.web || env.JSON != tc.json || !reflect.DeepEqual(rest, tc.rest) {
			t.Fatalf("%s: web=%q json=%v rest=%v, want %q %v %v", tc.name, env.Web, env.JSON, rest, tc.web, tc.json, tc.rest)
		}
	}

	// The global flags help block documents the new spelling.
	var out bytes.Buffer
	if code := Run([]string{"--help"}, "dev", &out, &out); code != 0 {
		t.Fatalf("Run(--help) = %d", code)
	}
	if !strings.Contains(out.String(), "--web <dir>") {
		t.Fatalf("help is missing the --web line:\n%s", out.String())
	}
}

func TestRunWebFlagWithCommandIsUsageError(t *testing.T) {
	opened := stubOpenBrowser(t)
	state := t.TempDir()

	run := func(args ...string) (int, string) {
		var out, errOut bytes.Buffer
		code := Run(args, "dev", &out, &errOut)
		return code, errOut.String()
	}

	code, stderr := run("--web", "--home-dir", state, ".", "ps")
	if code != 2 || !strings.Contains(stderr, "takes no command") {
		t.Fatalf("Run(--web . ps) = %d, stderr %q", code, stderr)
	}
	// A directory landing after other flags is a flag-ordering mistake, not a
	// command: the error must name the --web spelling instead of quoting the
	// directory as if it were one.
	code, stderr = run("--web", "--home-dir", state, ".")
	if code != 2 || !strings.Contains(stderr, "takes no command") || !strings.Contains(stderr, "--web=<dir>") {
		t.Fatalf("Run(--web --home-dir X .) = %d, stderr %q", code, stderr)
	}
	code, stderr = run("--web", "--home-dir", state, "--daemon", "ws://127.0.0.1:1/ws")
	if code != 2 || !strings.Contains(stderr, "--daemon") {
		t.Fatalf("Run(--web --daemon …) = %d, stderr %q", code, stderr)
	}
	if len(*opened) != 0 {
		t.Fatalf("a web server booted on a usage error: %v", *opened)
	}
	if _, err := os.Stat(discovery.Path(state)); !os.IsNotExist(err) {
		t.Fatalf("a usage error still wrote %s", discovery.Path(state))
	}
}

func TestWebCommandRejectsBadArgs(t *testing.T) {
	opened := stubOpenBrowser(t)
	env, _ := newUsageTestEnv()
	env.HomeDir = t.TempDir()
	for _, args := range [][]string{{""}, {"-x"}, {"a", "b"}} {
		err := runWebCommand(context.Background(), env, args)
		var ee exitError
		if !errors.As(err, &ee) || ee.code != 2 {
			t.Fatalf("runWebCommand(%q) = %v, want a usage error", args, err)
		}
	}
	if len(*opened) != 0 {
		t.Fatalf("a web server booted on bad args: %v", *opened)
	}
}

func TestRunWebRefusesInsideAgentSession(t *testing.T) {
	t.Setenv("AT_AGENT_ID", "agent-web")
	env := &Env{Out: io.Discard, Err: io.Discard, HomeDir: t.TempDir()}
	err := runWeb(context.Background(), env, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "agent session") {
		t.Fatalf("runWeb inside an agent session = %v, want the refusal", err)
	}
	var ee exitError
	if errors.As(err, &ee) {
		t.Fatal("the refusal must be a real error, not a usage error")
	}
	if _, serr := os.Stat(discovery.Path(env.HomeDir)); !os.IsNotExist(serr) {
		t.Fatalf("the refusal still wrote %s", discovery.Path(env.HomeDir))
	}
}

func TestStartWebOwnedConnContract(t *testing.T) {
	dir := t.TempDir()
	w, stateDir := bootWeb(t, dir)

	payload := getConn(t, w)
	want := []string{"wsUrl", "httpUrl", "token", "launchDir", "launchThread", "host", "attached", "keepRunning"}
	if len(payload) != len(want) {
		t.Fatalf("/conn has %d keys (%v), want exactly the %d-key contract", len(payload), payloadKeys(payload), len(want))
	}
	for _, k := range want {
		if _, ok := payload[k]; !ok {
			t.Fatalf("/conn is missing %q: %v", k, payloadKeys(payload))
		}
	}

	u, err := url.Parse(w.URL())
	if err != nil {
		t.Fatal(err)
	}
	// httpUrl is always the web server's own origin, and wsUrl rides the same
	// socket in owned mode.
	if got := connString(t, payload, "httpUrl"); got != w.URL() {
		t.Fatalf("httpUrl = %q, want the web origin %q", got, w.URL())
	}
	if got := connString(t, payload, "wsUrl"); got != "ws://"+u.Host+"/ws" {
		t.Fatalf("wsUrl = %q, want ws://%s/ws", got, u.Host)
	}
	if got := connString(t, payload, "token"); got == "" {
		t.Fatal("token is empty")
	}
	if got := connString(t, payload, "launchDir"); got != resolvedDir(t, dir) {
		t.Fatalf("launchDir = %q, want the resolved %q", got, resolvedDir(t, dir))
	}
	if got := connString(t, payload, "launchThread"); got != "" {
		t.Fatalf("launchThread = %q, want empty", got)
	}
	if got := connString(t, payload, "host"); got != u.Host {
		t.Fatalf("host = %q, want %q", got, u.Host)
	}
	if string(payload["attached"]) != "false" {
		t.Fatalf("attached = %s, want false", payload["attached"])
	}
	if string(payload["keepRunning"]) != "false" {
		t.Fatalf("keepRunning = %s, want false", payload["keepRunning"])
	}

	// The daemon answers through the web origin.
	res, err := http.Get(w.URL() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("GET /healthz = %d %q", res.StatusCode, body)
	}

	// The SPA: index at /, and the client-side-route fallback.
	for _, path := range []string{"/", "/threads/xyz"} {
		res, err := http.Get(w.URL() + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "needs building") {
			t.Fatalf("GET %s = %d, want the placeholder index (%d bytes)", path, res.StatusCode, len(body))
		}
	}

	// Until shutdown, the discovery file names this server.
	info, err := discovery.Read(stateDir)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if info.WSURL != connString(t, payload, "wsUrl") {
		t.Fatalf("discovery wsUrl = %q, want the /conn handoff", info.WSURL)
	}
}

func payloadKeys(payload map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	return keys
}

// webConnToken reads the daemon token the page receives from /conn.
func webConnToken(t *testing.T, w *webServer) string {
	t.Helper()
	res, err := http.Get(w.URL() + "/conn")
	if err != nil {
		t.Fatalf("GET /conn: %v", err)
	}
	defer res.Body.Close()
	var conn struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&conn); err != nil || conn.Token == "" {
		t.Fatalf("GET /conn token: %v", err)
	}
	return conn.Token
}

func TestStartWebPreferencesSurviveRestart(t *testing.T) {
	stateDir := t.TempDir()
	env := &Env{Out: io.Discard, Err: io.Discard, HomeDir: stateDir, Version: "webtest"}
	dir := t.TempDir()

	w, err := startWeb(context.Background(), env, dir)
	if err != nil {
		t.Fatalf("startWeb: %v", err)
	}
	// The daemon owns the preference store, so writes need the daemon token
	// the page itself was handed.
	req, _ := http.NewRequest(http.MethodPost, w.URL()+"/prefs", strings.NewReader(`{"at.theme":"nord","at.logsOpen":"false"}`))
	req.Header.Set("Authorization", "Bearer "+webConnToken(t, w))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /prefs: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /prefs = %d, want 204", res.StatusCode)
	}
	w.Close()

	// The next run binds a different loopback port; the saved preferences must
	// still be in the page before the app's scripts run.
	again, err := startWeb(context.Background(), env, dir)
	if err != nil {
		t.Fatalf("restart startWeb: %v", err)
	}
	t.Cleanup(again.Close)
	if again.URL() == w.URL() {
		t.Log("the restart reused the origin; the test still checks the seed path")
	}
	page, err := http.Get(again.URL() + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	for _, want := range []string{`"at.theme":"nord"`, `"at.logsOpen":"false"`, "localStorage.setItem"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("restarted page does not seed %q: %s", want, body)
		}
	}
}

func TestStartWebConnFetchSiteGate(t *testing.T) {
	w, _ := bootWeb(t, t.TempDir())

	res, err := http.Post(w.URL()+"/conn", "", nil)
	if err != nil {
		t.Fatalf("POST /conn: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /conn = %d, want 405", res.StatusCode)
	}

	for site, want := range map[string]int{
		"cross-site":  http.StatusForbidden,
		"same-origin": http.StatusOK,
		"none":        http.StatusOK,
		"":            http.StatusOK,
		"same-site":   http.StatusForbidden,
	} {
		req, err := http.NewRequest(http.MethodGet, w.URL()+"/conn", nil)
		if err != nil {
			t.Fatal(err)
		}
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /conn (Sec-Fetch-Site: %q): %v", site, err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("GET /conn with Sec-Fetch-Site %q = %d, want %d", site, res.StatusCode, want)
		}
		for k := range res.Header {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Fatalf("/conn emitted %s; it never sends CORS headers", k)
			}
		}
	}
}

// TestWebLoopbackHostHeader pins the Host spellings the web surface answers.
func TestWebLoopbackHostHeader(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"", true}, // HTTP/1.0 with no Host header: nothing to forge
		{"127.0.0.1", true},
		{"127.0.0.1:8080", true},
		{"localhost", true},
		{"localhost:8080", true},
		{"LOCALHOST:8080", true},
		{"[::1]", true},
		{"[::1]:8080", true},
		{"evil.com", false},
		{"evil.com:8080", false},
		{"127.0.0.2:8080", false},
		{"localhost.evil.com", false},
		{"[::2]:8080", false},
		{"[::1", false},
	} {
		if got := loopbackHostHeader(tc.host); got != tc.want {
			t.Errorf("loopbackHostHeader(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// TestStartWebLoopbackHostGate: a request whose Host header names a rebound
// hostname is refused on every path of the web surface — DNS rebinding makes
// the fetch look same-origin to the browser, so the Sec-Fetch-Site gate
// cannot catch it; the Host header still carries the attacker's name.
func TestStartWebLoopbackHostGate(t *testing.T) {
	w, _ := bootWeb(t, t.TempDir())
	u, err := url.Parse(w.URL())
	if err != nil {
		t.Fatal(err)
	}
	port := u.Port()

	// Host overrides on the live server: an http client sends the override
	// verbatim, exactly like a rebound browser fetch would.
	for _, tc := range []struct {
		host string
		want int
	}{
		{"evil.com:" + port, http.StatusForbidden},
		{"evil.com", http.StatusForbidden},
		{"127.0.0.1:" + port, http.StatusOK},
		{"localhost:" + port, http.StatusOK},
		{"LOCALHOST:" + port, http.StatusOK},
		{"[::1]:" + port, http.StatusOK},
	} {
		for _, path := range []string{"/conn", "/"} {
			req, err := http.NewRequest(http.MethodGet, w.URL()+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = tc.host
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET %s (Host %q): %v", path, tc.host, err)
			}
			res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("GET %s with Host %q = %d, want %d", path, tc.host, res.StatusCode, tc.want)
			}
		}
	}

	// An absent Host header (HTTP/1.0) carries no hostname to forge.
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, "GET /conn HTTP/1.0\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	status, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, " 200 ") {
		t.Fatalf("HTTP/1.0 /conn without a Host header = %q, want 200", status)
	}
}

// The attach-mode surface answers the same loopback-host gate.
func TestStartWebAttachedLoopbackHostGate(t *testing.T) {
	stateDir := t.TempDir()
	newWebTestDaemon(t, stateDir)

	env := &Env{Out: io.Discard, Err: io.Discard, HomeDir: stateDir, Version: "webtest"}
	w, err := startWeb(context.Background(), env, t.TempDir())
	if err != nil {
		t.Fatalf("startWeb: %v", err)
	}
	t.Cleanup(w.Close)

	u, err := url.Parse(w.URL())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host string
		want int
	}{
		{"evil.com:" + u.Port(), http.StatusForbidden},
		{"evil.com", http.StatusForbidden},
		{"localhost:" + u.Port(), http.StatusOK},
	} {
		req, err := http.NewRequest(http.MethodGet, w.URL()+"/conn", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = tc.host
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /conn (Host %q): %v", tc.host, err)
		}
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Fatalf("GET /conn with Host %q = %d, want %d", tc.host, res.StatusCode, tc.want)
		}
	}
}

func TestStartWebDelegatesDaemonRoutes(t *testing.T) {
	w, _ := bootWeb(t, t.TempDir())

	res, err := http.Post(w.URL()+"/tool/call", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /tool/call: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /tool/call unauthed = %d %q, want 401 from the daemon handler", res.StatusCode, body)
	}

	res, err = http.Get(w.URL() + "/attachments")
	if err != nil {
		t.Fatalf("GET /attachments: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /attachments unauthed = %d, want 401", res.StatusCode)
	}
}

func TestStartWebDiscoveryReusedByCLI(t *testing.T) {
	w, stateDir := bootWeb(t, t.TempDir())
	payload := getConn(t, w)
	wsURL := connString(t, payload, "wsUrl")
	token := connString(t, payload, "token")

	// `agenttunnel daemon status` reuses the web mode's daemon.
	var out, errOut bytes.Buffer
	code := Run([]string{"--home-dir", stateDir, "daemon", "status"}, "webtest", &out, &errOut)
	if code != 0 {
		t.Fatalf("Run(daemon status) = %d, stderr %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "daemon running at "+wsURL) {
		t.Fatalf("status output = %q, want the web daemon at %s", out.String(), wsURL)
	}

	// The browser's ?token= loopback query path connects through the web
	// origin's /ws.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, err := dialWebQueryToken(ctx, wsURL, token)
	if err != nil {
		t.Fatalf("dial %s?token=…: %v", wsURL, err)
	}
	defer ws.CloseNow()
	if err := wsProjectList(ctx, ws); err != nil {
		t.Fatalf("project.list over the query-token connection: %v", err)
	}
}

func TestStartWebShutdownRemovesDiscovery(t *testing.T) {
	w, stateDir := bootWeb(t, t.TempDir())
	payload := getConn(t, w)
	wsURL := connString(t, payload, "wsUrl")
	token := connString(t, payload, "token")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, wsURL, token)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	var reply struct {
		OK bool `json:"ok"`
	}
	if err := c.Call(ctx, "daemon.shutdown", nil, &reply); err != nil {
		// The server may close the socket before the reply is read; Done()
		// below is the real assertion.
		t.Logf("daemon.shutdown reply: %v", err)
	}
	c.Close()

	select {
	case <-w.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("the web server did not stop after daemon.shutdown")
	}
	if _, err := os.Stat(discovery.Path(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("discovery file survived shutdown: %v", err)
	}
}

func TestStartWebAttachedReturnsExistingDaemon(t *testing.T) {
	stateDir := t.TempDir()
	wsURL, _, token := newWebTestDaemon(t, stateDir)

	env := &Env{Out: io.Discard, Err: io.Discard, HomeDir: stateDir, Version: "webtest"}
	w, err := startWeb(context.Background(), env, t.TempDir())
	if err != nil {
		t.Fatalf("startWeb: %v", err)
	}
	t.Cleanup(w.Close)

	payload := getConn(t, w)
	if got := connString(t, payload, "wsUrl"); got != wsURL {
		t.Fatalf("wsUrl = %q, want the existing daemon's %q", got, wsURL)
	}
	if got := connString(t, payload, "token"); got != token {
		t.Fatalf("token = %q, want the existing daemon's", got)
	}
	if string(payload["attached"]) != "true" {
		t.Fatalf("attached = %s, want true", payload["attached"])
	}
	// httpUrl stays the web server's own origin, so uploads are same-origin.
	if got := connString(t, payload, "httpUrl"); got != w.URL() {
		t.Fatalf("httpUrl = %q, want the web origin %q", got, w.URL())
	}
	if got := connString(t, payload, "host"); got != hostFromWSURL(wsURL) {
		t.Fatalf("host = %q, want the existing daemon's %q", got, hostFromWSURL(wsURL))
	}

	// Attach mode serves only the web surface: the daemon's routes on this
	// origin are dead, not SPA pages.
	for _, path := range []string{"/healthz", "/ws"} {
		res, err := http.Get(w.URL() + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404 in attach mode", path, res.StatusCode)
		}
	}
}

func TestStartWebAttachedProxyForwarded(t *testing.T) {
	stateDir := t.TempDir()
	_, httpURL, token := newWebTestDaemon(t, stateDir)

	env := &Env{Out: io.Discard, Err: io.Discard, HomeDir: stateDir, Version: "webtest"}
	w, err := startWeb(context.Background(), env, t.TempDir())
	if err != nil {
		t.Fatalf("startWeb: %v", err)
	}
	t.Cleanup(w.Close)

	// The same junk upload twice: once straight to the daemon with its token,
	// once through the web origin with no credentials at all.
	post := func(u, auth string) (int, string) {
		req, err := http.NewRequest(http.MethodPost, u+"?threadId=x&name=y", strings.NewReader("junk"))
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", u, err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode, string(body)
	}
	directStatus, directBody := post(httpURL+"/attachments", token)
	viaStatus, viaBody := post(w.URL()+"/attachments", "")

	if viaStatus == http.StatusUnauthorized {
		t.Fatal("the proxy did not inject the daemon's token")
	}
	if viaStatus != directStatus || viaBody != directBody {
		t.Fatalf("proxy forwarded %d %q, the daemon answered %d %q", viaStatus, viaBody, directStatus, directBody)
	}
}

func TestStartWebAttachFallsBackToOwnedOnStaleDiscovery(t *testing.T) {
	// A discovery file pointing at a dead loopback port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	stateDir := t.TempDir()
	if err := discovery.Write(stateDir, discovery.Info{
		WSURL: "ws://" + addr + "/ws", HTTPURL: "http://" + addr, Token: "stale",
	}); err != nil {
		t.Fatalf("discovery: %v", err)
	}

	env := &Env{Out: io.Discard, Err: io.Discard, HomeDir: stateDir, Version: "webtest"}
	w, err := startWeb(context.Background(), env, t.TempDir())
	if err != nil {
		t.Fatalf("startWeb: %v", err)
	}
	t.Cleanup(w.Close)

	payload := getConn(t, w)
	if string(payload["attached"]) != "false" {
		t.Fatal("a stale discovery file must fall back to owned mode")
	}
	// And the stale entry was overwritten by the owned daemon's.
	info, err := discovery.Read(stateDir)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if info.WSURL != connString(t, payload, "wsUrl") {
		t.Fatalf("discovery wsUrl = %q, want the owned server's %q", info.WSURL, connString(t, payload, "wsUrl"))
	}
}

func TestStartWebOwnedUnderDaemonlock(t *testing.T) {
	stateDir := t.TempDir()
	lock, err := daemonlock.Acquire(context.Background(), stateDir, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("daemonlock: %v", err)
	}

	env := &Env{Out: io.Discard, Err: io.Discard, HomeDir: stateDir, Version: "webtest"}
	type result struct {
		w   *webServer
		err error
	}
	ch := make(chan result, 1)
	go func() {
		w, err := startWeb(context.Background(), env, t.TempDir())
		ch <- result{w, err}
	}()

	// While the lock is held, a daemon comes up in this state dir — the
	// atd-vs-web race: under the lock, web mode must attach to it.
	wsURL, _, token := newWebTestDaemon(t, stateDir)
	lock.Release()

	var r result
	select {
	case r = <-ch:
	case <-time.After(15 * time.Second):
		t.Fatal("startWeb never finished after the lock was released")
	}
	if r.err != nil {
		t.Fatalf("startWeb: %v", r.err)
	}
	w := r.w
	t.Cleanup(w.Close)

	payload := getConn(t, w)
	if string(payload["attached"]) != "true" || connString(t, payload, "wsUrl") != wsURL {
		t.Fatalf("startWeb double-booted instead of attaching under the lock: %s", payload)
	}
	info, err := discovery.Read(stateDir)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if info.WSURL != wsURL || info.Token != token {
		t.Fatalf("discovery was overwritten by a second daemon: %+v", info)
	}
}

// The web dir accepts a symlinked spelling and resolves it for launchDir, the
// same way the desktop open does.
func TestStartWebResolvesLaunchDirThroughSymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	w, _ := bootWeb(t, link)
	payload := getConn(t, w)
	if got := connString(t, payload, "launchDir"); got != resolvedDir(t, real) {
		t.Fatalf("launchDir = %q, want %q", got, resolvedDir(t, real))
	}
}
