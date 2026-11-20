// Web mode: `agenttunnel --web <dir>` (and its `agenttunnel web <dir>`
// spelling) serves the AgentTunnel web UI from a loopback HTTP server, backed
// either by an in-process daemon it owns or by a daemon someone else already
// left running — the same two shapes the desktop app uses.

package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/daemonlock"
	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/prefs"
	"github.com/agenttunnel/agenttunnel-go/internal/webui"
	"github.com/agenttunnel/agenttunnel-go/pkg/daemon"
)

func init() {
	Register(Command{
		Name:    "web",
		Summary: "Serve the AgentTunnel web UI on localhost",
		Usage:   "agenttunnel web [dir]",
		Run:     runWebCommand,
	})
}

// openBrowser opens the user's default browser on the web UI. It is a package
// var so tests never spawn one; failure is reported to the caller but never
// fatal — the URL is printed regardless.
var openBrowser = openBrowserForOS

// openBrowserForOS launches the platform's default-browser command without
// waiting for it.
func openBrowserForOS(raw string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", raw)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", raw)
	default:
		cmd = exec.Command("xdg-open", raw)
	}
	return cmd.Start()
}

// runWebCommand is the `agenttunnel web [dir]` command spelling; the arguments
// follow the desktop rule: at most one directory, never a flag.
func runWebCommand(ctx context.Context, env *Env, args []string) error {
	if len(args) > 1 {
		return usageError("usage: agenttunnel web [dir]")
	}
	dir := "."
	if len(args) == 1 {
		if args[0] == "" || strings.HasPrefix(args[0], "-") {
			return usageError("usage: agenttunnel web [dir]")
		}
		dir = args[0]
	}
	return runWeb(ctx, env, dir)
}

// runWeb boots the web server and blocks until it stops. Ctrl-C and the
// daemon.shutdown RPC both take it down.
func runWeb(ctx context.Context, env *Env, dir string) error {
	// The agent environment is exported to child agents, so a stray invocation
	// from inside a session must not start the web UI (and a daemon with it).
	if os.Getenv("AT_AGENT_ID") != "" {
		return errors.New("refusing to start the web UI inside an agent session")
	}

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := startWeb(sigCtx, env, dir)
	if err != nil {
		return err
	}
	defer srv.Close()

	if err := openBrowser(srv.URL()); err != nil {
		fmt.Fprintf(env.Err, "agenttunnel: open a browser: %v (the web UI is at %s)\n", err, srv.URL())
	}
	fmt.Fprintf(env.Out, "AgentTunnel web UI at %s\n", srv.URL())
	<-srv.Done()
	return srv.ServeErr()
}

// startWeb resolves dir and picks the mode: a daemon that already answers
// discovery gets a web surface in front of it (attach mode, its discovery file
// never touched); no daemon anywhere means this server owns one (owned mode).
func startWeb(ctx context.Context, env *Env, dir string) (*webServer, error) {
	resolved, err := resolveProjectDir(dir)
	if err != nil {
		return nil, err
	}
	stateDir := env.StateDir()

	// A daemon someone else owns (an `atd`, or a desktop left running in the
	// background) answers discovery. Attach to it: never start a second daemon
	// and never touch its discovery file.
	if c, info, cerr := env.connectExisting(ctx); cerr == nil {
		c.Close()
		return startWebAttached(ctx, info, resolved)
	}

	// Serialize the "no daemon here yet, so I may boot one" decision across
	// processes sharing this state dir, mirroring atd.
	lock, err := daemonlock.Acquire(ctx, stateDir, 50*time.Millisecond)
	if err != nil {
		return nil, err
	}
	// Re-check under the lock: a competing starter may have brought a daemon
	// up while this process waited for it.
	if c, info, cerr := env.connectExisting(ctx); cerr == nil {
		c.Close()
		lock.Release()
		return startWebAttached(ctx, info, resolved)
	}
	return startWebOwned(ctx, env, resolved, lock)
}

// webPreferredAddr is the loopback address the web UI tries first. A browser
// keys notification permission to the origin, port included, so a stable port
// keeps the permission the user granted across runs. Any other free port is
// the fallback.
const webPreferredAddr = "127.0.0.1:7878"

func webListen() (net.Listener, error) {
	if ln, err := net.Listen("tcp", webPreferredAddr); err == nil {
		return ln, nil
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

// startWebOwned boots an in-process daemon and serves the web UI and the
// daemon's own routes from one loopback listener, so the browser's WebSocket
// and uploads stay same-origin. The lock is released as soon as discovery
// names this server; it is never held while serving.
func startWebOwned(ctx context.Context, env *Env, resolved string, lock *daemonlock.Lock) (*webServer, error) {
	stateDir := env.StateDir()
	webCtx, cancel := context.WithCancel(ctx)

	daemon.EnrichPATH()

	token, err := newWebToken()
	if err != nil {
		cancel()
		lock.Release()
		return nil, err
	}
	d, err := daemon.New(webCtx, daemon.Options{
		StateDir: stateDir,
		Token:    token,
		Workdir:  resolved,
		Config:   orchestrator.Config{MaxPasses: 3, MaxConcurrency: 4},
		Version:  env.Version,
		// `agenttunnel daemon stop` must take the web server down with the
		// daemon, like a signal does.
		OnShutdown: cancel,
	})
	if err != nil {
		cancel()
		lock.Release()
		return nil, err
	}

	ln, err := webListen()
	if err != nil {
		cancel()
		lock.Release()
		d.Close()
		return nil, err
	}
	// Agents call the tool endpoint at this address.
	d.SetEndpoint("http://" + ln.Addr().String())

	addr := ln.Addr().String()
	origin := "http://" + addr
	conn := webConn{
		wsURL:     "ws://" + addr + "/ws",
		httpURL:   origin,
		token:     d.Token,
		launchDir: resolved,
		host:      addr,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/conn", conn.serveHTTP)
	daemonH := d.Server.Handler()
	for _, p := range []string{"/ws", "/healthz", "/tool/call", "/attachments", "/prefs"} {
		mux.Handle(p, daemonH)
	}
	mux.Handle("/", webui.HandlerWithPrefs(d.Prefs.Snapshot))

	srv := &http.Server{Handler: loopbackHostGate(mux), ReadHeaderTimeout: 10 * time.Second}
	w := &webServer{
		url:       origin,
		cancel:    cancel,
		serveDone: make(chan struct{}),
	}
	// Stop serving when the context ends: a signal, Close, or OnShutdown from
	// daemon.shutdown.
	go func() {
		<-webCtx.Done()
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = srv.Shutdown(shutdownCtx)
	}()
	// Serve before publishing discovery: a client that reads daemon.json
	// immediately must find a server that answers.
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			w.mu.Lock()
			w.serveErr = err
			w.mu.Unlock()
		}
		// This server owns its discovery file: it goes away with the server.
		// The daemonlock was released long before serving began.
		_ = discovery.Remove(stateDir)
		d.Close()
		close(w.serveDone)
	}()

	if err := discovery.Write(stateDir, discovery.Info{
		WSURL:     conn.wsURL,
		HTTPURL:   origin,
		Token:     d.Token,
		PID:       os.Getpid(),
		Version:   env.Version,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		cancel()
		<-w.serveDone
		lock.Release()
		return nil, fmt.Errorf("write %s: %w", discovery.FileName, err)
	}
	// The lock only covers the first-daemon decision; release it now that a
	// waiter can see this daemon in discovery and attach to it.
	lock.Release()
	return w, nil
}

// startWebAttached serves the web surface for a daemon this process does not
// own. The existing daemon's discovery file is read, never written or removed;
// the browser reaches the daemon through this server's /conn handoff and a
// token-injecting /attachments and /prefs proxy, so everything stays same-origin and the
// daemon keeps no CORS surface.
func startWebAttached(ctx context.Context, info discovery.Info, resolved string) (*webServer, error) {
	webCtx, cancel := context.WithCancel(ctx)

	ln, err := webListen()
	if err != nil {
		cancel()
		return nil, err
	}
	addr := ln.Addr().String()
	origin := "http://" + addr

	base := info.HTTPURL
	if base == "" {
		base = httpURLFromWS(info.WSURL)
	}
	target, err := url.Parse(base)
	if err != nil {
		cancel()
		ln.Close()
		return nil, fmt.Errorf("parse daemon http url %s: %w", base, err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	defaultDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		defaultDirector(r)
		r.Host = target.Host
		r.Header.Set("Authorization", "Bearer "+info.Token)
	}

	conn := webConn{
		wsURL:     info.WSURL,
		httpURL:   origin, // always the web server's own origin, in both modes
		token:     info.Token,
		launchDir: resolved,
		host:      hostFromWSURL(info.WSURL),
		attached:  true,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/conn", conn.serveHTTP)
	mux.Handle("/prefs", proxy)
	mux.Handle("/attachments", proxy)
	// The daemon-owned routes are served by the existing daemon, not this
	// server: they 404 rather than falling into the SPA, so a client pointed at
	// the wrong origin sees a dead route instead of a page.
	for _, p := range []string{"/ws", "/healthz", "/tool/call"} {
		mux.Handle(p, http.NotFoundHandler())
	}
	mux.Handle("/", webui.HandlerWithPrefs(prefs.FetchFunc(base, info.Token)))

	srv := &http.Server{Handler: loopbackHostGate(mux), ReadHeaderTimeout: 10 * time.Second}
	w := &webServer{
		url:       origin,
		cancel:    cancel,
		serveDone: make(chan struct{}),
	}
	go func() {
		<-webCtx.Done()
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = srv.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			w.mu.Lock()
			w.serveErr = err
			w.mu.Unlock()
		}
		close(w.serveDone)
	}()
	return w, nil
}

// loopbackHostGate wraps the whole web mux in both modes and rejects requests
// whose Host header does not name a loopback host. DNS rebinding lets a page
// at evil.com point its own hostname at 127.0.0.1: the browser then considers
// its fetch same-origin, so the Sec-Fetch-Site gate passes — but the request
// still carries evil.com in Host, which is what this gate checks.
func loopbackHostGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHostHeader(r.Host) {
			http.Error(w, "refused: unknown host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackHostHeader reports whether a Host header value names this loopback
// server: 127.0.0.1, localhost, or [::1], each optionally with a port,
// case-insensitive. An empty value (an HTTP/1.0 request without a Host
// header) carries no hostname to forge.
func loopbackHostHeader(host string) bool {
	if host == "" {
		return true
	}
	if strings.HasPrefix(host, "[") {
		end := strings.Index(host, "]")
		return end != -1 && strings.EqualFold(host[:end+1], "[::1]")
	}
	if i := strings.LastIndex(host, ":"); i != -1 {
		host = host[:i]
	}
	return strings.EqualFold(host, "127.0.0.1") || strings.EqualFold(host, "localhost")
}

// webServer is a running web mode server: the web UI plus, in owned mode, the
// daemon it owns and the daemon-served routes on the same listener.
type webServer struct {
	url string

	cancel    context.CancelFunc
	serveDone chan struct{}

	mu       sync.Mutex
	serveErr error
}

// URL is the web server's http origin.
func (w *webServer) URL() string { return w.url }

// Done is closed once serving has ended and cleanup has run.
func (w *webServer) Done() <-chan struct{} { return w.serveDone }

// Close stops the server and waits for it to finish. It is idempotent.
func (w *webServer) Close() {
	w.cancel()
	<-w.serveDone
}

// ServeErr waits for the server to finish and reports why serving ended. A
// clean shutdown reports nil.
func (w *webServer) ServeErr() error {
	<-w.serveDone
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.serveErr
}

// webConn is the connection handoff the browser UI receives from GET /conn. It
// mirrors the desktop shellConn payload exactly — the same eight keys, so the
// frontend's Conn shape never branches on the host.
type webConn struct {
	wsURL     string
	httpURL   string
	token     string
	launchDir string
	host      string
	attached  bool
}

func (c webConn) payload() map[string]any {
	return map[string]any{
		"wsUrl":        c.wsURL,
		"httpUrl":      c.httpURL,
		"token":        c.token,
		"launchDir":    c.launchDir,
		"launchThread": "", // web mode has no --thread form
		"host":         c.host,
		"attached":     c.attached,
		"keepRunning":  false,
	}
}

// serveHTTP answers GET /conn. Only the same origin (or a non-browser client
// with no Sec-Fetch-Site header) may read it, no CORS header is ever emitted:
// the browser must connect same-origin, and the loopback bind is the trust
// boundary.
func (c webConn) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameSiteRequest(r) {
		http.Error(w, "cross-site requests are refused", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(c.payload())
}

// sameSiteRequest rejects browser requests a page on another origin initiated.
// A non-browser client sends no Sec-Fetch-Site and is allowed; the loopback
// bind plus the daemon token remain the trust boundary.
func sameSiteRequest(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	return site == "" || site == "same-origin" || site == "none"
}

// newWebToken mints the owned daemon's bearer token.
func newWebToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate web daemon token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// hostFromWSURL returns the host:port a ws:// URL points at, falling back to
// the raw URL when it cannot be parsed.
func hostFromWSURL(ws string) string {
	u, err := url.Parse(ws)
	if err != nil || u.Host == "" {
		return ws
	}
	return u.Host
}

// httpURLFromWS converts a WebSocket URL into the HTTP base it rides on.
func httpURLFromWS(ws string) string {
	switch {
	case strings.HasPrefix(ws, "wss://"):
		return "https://" + strings.TrimSuffix(strings.TrimPrefix(ws, "wss://"), "/ws")
	case strings.HasPrefix(ws, "ws://"):
		return "http://" + strings.TrimSuffix(strings.TrimPrefix(ws, "ws://"), "/ws")
	default:
		return strings.TrimSuffix(ws, "/ws")
	}
}
