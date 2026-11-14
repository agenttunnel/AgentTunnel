package main

// Remote daemon: the window can drive a daemon on another machine. The WebView
// cannot set an Authorization header on its WebSocket, and the daemon takes
// ?token= only from loopback peers, so the shell runs a loopback reverse proxy
// that adds the bearer token to every request it forwards. The remote token
// stays in this process; the frontend gets the proxy address and a per-run
// local secret that only this window knows.

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// remoteProbeTimeout bounds the connect check. A remote daemon answers
// project.list immediately; anything slower is reported as unreachable.
const remoteProbeTimeout = 5 * time.Second

// remoteConfig is one saved remote daemon, persisted in desktop-prefs.json.
type remoteConfig struct {
	Name  string `json:"name,omitempty"`
	URL   string `json:"url"`
	Token string `json:"token"`
}

// normalizeRemoteURL accepts an http(s) or ws(s) base URL and returns the http(s)
// base with no trailing slash and no /ws suffix. Plain http is allowed only for
// loopback, so a bearer token never crosses the network in clear text.
func normalizeRemoteURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", errors.New("enter the daemon address, for example https://host:7331")
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopbackHost(u.Hostname()):
	default:
		return "", errors.New("the daemon address must use https:// (plain http is only for localhost)")
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/ws"), "/")
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkRemote confirms the daemon accepts the token: one authenticated
// project.list over the WebSocket. Errors never include the token.
func checkRemote(ctx context.Context, cfg remoteConfig) error {
	base, err := normalizeRemoteURL(cfg.URL)
	if err != nil {
		return err
	}
	if cfg.Token == "" {
		return errors.New("enter the daemon token")
	}
	ctx, cancel := context.WithTimeout(ctx, remoteProbeTimeout)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, wsURLFromHTTP(base)+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + cfg.Token}},
	})
	if err != nil {
		return fmt.Errorf("could not reach the daemon at %s", base)
	}
	defer ws.CloseNow()
	if _, err := rpcCall(ctx, ws, "project.list", nil); err != nil {
		return fmt.Errorf("the daemon rejected the connection: %v", err)
	}
	return nil
}

func wsURLFromHTTP(base string) string {
	if rest, ok := strings.CutPrefix(base, "https://"); ok {
		return "wss://" + rest
	}
	return "ws://" + strings.TrimPrefix(base, "http://")
}

// remoteProxy forwards one window's HTTP and WebSocket traffic to a remote
// daemon. Requests must carry the local secret (bearer, or ?token= for the
// WebSocket the WebView opens); the proxy swaps it for the remote bearer token.
type remoteProxy struct {
	cfg    remoteConfig
	local  string
	srv    *http.Server
	ln     net.Listener
	closed sync.Once
}

// startRemoteProxy listens on a loopback port and serves the remote daemon
// through it.
func startRemoteProxy(cfg remoteConfig) (*remoteProxy, error) {
	base, err := normalizeRemoteURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	target, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	cfg.URL = base
	p := &remoteProxy{cfg: cfg, local: mustToken()}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.Out.Header.Set("Authorization", "Bearer "+cfg.Token)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// The error text can echo the remote host; it never carries the token.
			http.Error(w, "the remote daemon is unreachable", http.StatusBadGateway)
		},
	}
	p.srv = &http.Server{Handler: p.guard(rp), ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p.ln = ln
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// guard rejects any request that does not present the local secret, so other
// local processes and rebound web pages cannot use the remote credentials.
func (p *remoteProxy) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		query := r.URL.Query()
		if !secretMatches(bearer, p.local) && !secretMatches(query.Get("token"), p.local) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		query.Del("token")
		r.URL.RawQuery = query.Encode()
		next.ServeHTTP(w, r)
	})
}

func secretMatches(got, want string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// conn is the connection the window receives while this proxy is active. The
// window is attached to a daemon it did not start, and it never sees the remote
// token.
func (p *remoteProxy) conn() shellConn {
	addr := p.ln.Addr().String()
	return shellConn{
		wsURL:      "ws://" + addr + "/ws",
		httpURL:    "http://" + addr,
		token:      p.local,
		host:       hostFromWS(wsURLFromHTTP(p.cfg.URL)),
		attached:   true,
		remoteName: p.cfg.Name,
		remoteURL:  p.cfg.URL,
	}
}

// close stops serving. Safe to call more than once.
func (p *remoteProxy) close() {
	p.closed.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = p.srv.Shutdown(ctx)
	})
}

// connHolder is the window's current daemon connection. It holds the local
// daemon (nil when this launch is a remote launch) and, while connected, the
// remote proxy that replaces it.
type connHolder struct {
	mu     sync.Mutex
	local  *shellConn
	remote *remoteProxy
}

func (h *connHolder) current() shellConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.remote != nil {
		return h.remote.conn()
	}
	if h.local != nil {
		return *h.local
	}
	return shellConn{}
}

// setRemote makes p the active remote and closes the previous one.
func (h *connHolder) setRemote(p *remoteProxy) {
	h.mu.Lock()
	old := h.remote
	h.remote = p
	h.mu.Unlock()
	if old != nil {
		old.close()
	}
}

// dropRemote returns the window to the local daemon. It reports false, and
// keeps the remote connection, when this launch has no local daemon: the
// window cannot be left with no daemon at all.
func (h *connHolder) dropRemote() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.local == nil {
		return false
	}
	if h.remote != nil {
		h.remote.close()
		h.remote = nil
	}
	return true
}

// installRemoteEvents wires Connect and Disconnect. Each answers with
// at:remote-result; the window reloads on success so it re-reads its connection.
func installRemoteEvents(app *application.App, holder *connHolder, state string) {
	app.Event.On("at:remote-connect", func(e *application.CustomEvent) {
		data, _ := e.Data.(map[string]any)
		cfg := remoteConfig{
			Name:  strings.TrimSpace(stringField(data, "name")),
			URL:   stringField(data, "url"),
			Token: strings.TrimSpace(stringField(data, "token")),
		}
		go func() {
			err := connectRemote(holder, state, cfg)
			app.Event.Emit("at:remote-result", remoteResult(false, err))
		}()
	})
	app.Event.On("at:remote-disconnect", func(*application.CustomEvent) {
		go func() {
			restart, err := disconnectRemote(holder, state)
			app.Event.Emit("at:remote-result", remoteResult(restart, err))
		}()
	})
}

func stringField(data map[string]any, key string) string {
	v, _ := data[key].(string)
	return v
}

func remoteResult(restart bool, err error) map[string]any {
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "restart": restart}
}

// connectRemote verifies the daemon and token, saves the connection, and makes
// it the window's daemon. Errors never include the token.
func connectRemote(holder *connHolder, state string, cfg remoteConfig) error {
	base, err := normalizeRemoteURL(cfg.URL)
	if err != nil {
		return err
	}
	cfg.URL = base
	if cfg.Name == "" {
		cfg.Name = hostFromWS(wsURLFromHTTP(base))
	}
	if err := checkRemote(context.Background(), cfg); err != nil {
		return err
	}
	proxy, err := startRemoteProxy(cfg)
	if err != nil {
		return err
	}
	if err := updatePrefs(state, func(p *prefs) { p.Remote = &cfg }); err != nil {
		proxy.close()
		return fmt.Errorf("save the connection: %w", err)
	}
	holder.setRemote(proxy)
	return nil
}

// disconnectRemote forgets the saved remote daemon. restart reports that the
// window stays on the remote until the next launch, which starts the local daemon.
func disconnectRemote(holder *connHolder, state string) (restart bool, err error) {
	if err := updatePrefs(state, func(p *prefs) { p.Remote = nil }); err != nil {
		return false, fmt.Errorf("save the connection: %w", err)
	}
	return !holder.dropRemote(), nil
}
