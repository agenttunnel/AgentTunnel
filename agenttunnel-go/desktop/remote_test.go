package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const remoteTestToken = "remote-secret-token"

// fakeRemote stands in for a remote daemon. It requires the remote bearer token
// on every request and echoes WebSocket frames. It records what it saw so a
// test can assert what the proxy forwarded.
type fakeRemote struct {
	mu      sync.Mutex
	auth    []string
	queries []string
}

func newFakeRemote(t *testing.T) (*httptest.Server, *fakeRemote) {
	t.Helper()
	rec := &fakeRemote{}
	mux := http.NewServeMux()
	authorize := func(w http.ResponseWriter, r *http.Request) bool {
		rec.mu.Lock()
		rec.auth = append(rec.auth, r.Header.Get("Authorization"))
		rec.queries = append(rec.queries, r.URL.RawQuery)
		rec.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+remoteTestToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("/conn", func(w http.ResponseWriter, r *http.Request) {
		if authorize(w, r) {
			fmt.Fprint(w, "ok")
		}
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r) {
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			typ, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), typ, data); err != nil {
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, rec
}

func (f *fakeRemote) snapshot() (auth, queries []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auth...), append([]string(nil), f.queries...)
}

func startTestProxy(t *testing.T, base string) *remoteProxy {
	t.Helper()
	p, err := startRemoteProxy(remoteConfig{Name: "test", URL: base, Token: remoteTestToken})
	if err != nil {
		t.Fatalf("startRemoteProxy: %v", err)
	}
	t.Cleanup(p.close)
	return p
}

func TestNormalizeRemoteURL(t *testing.T) {
	ok := map[string]string{
		"https://host:7331/":     "https://host:7331",
		"wss://host/ws":          "https://host",
		"https://host/base/ws/":  "https://host/base",
		"ws://127.0.0.1:7331/ws": "http://127.0.0.1:7331",
		"http://localhost:7331":  "http://localhost:7331",
		"  https://host:7331  ":  "https://host:7331",
	}
	for in, want := range ok {
		got, err := normalizeRemoteURL(in)
		if err != nil || got != want {
			t.Errorf("normalizeRemoteURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// Plain http to a non-loopback host would send the bearer token in clear text.
	for _, in := range []string{"", "https://", "http://example.com", "ftp://host", "not a url"} {
		if got, err := normalizeRemoteURL(in); err == nil {
			t.Errorf("normalizeRemoteURL(%q) = %q; want an error", in, got)
		}
	}
}

func TestRemoteProxyInjectsBearerToken(t *testing.T) {
	remote, rec := newFakeRemote(t)
	p := startTestProxy(t, remote.URL)

	req, _ := http.NewRequest(http.MethodGet, p.conn().httpURL+"/conn", nil)
	req.Header.Set("Authorization", "Bearer "+p.local)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("proxied GET = %d %q; want 200 ok", res.StatusCode, body)
	}

	auth, _ := rec.snapshot()
	if len(auth) != 1 || auth[0] != "Bearer "+remoteTestToken {
		t.Fatalf("remote saw Authorization %q; want only the remote bearer token", auth)
	}
}

func TestRemoteProxyRejectsMissingLocalSecret(t *testing.T) {
	remote, rec := newFakeRemote(t)
	p := startTestProxy(t, remote.URL)

	for _, tc := range []struct{ name, header, query string }{
		{name: "no secret"},
		{name: "wrong bearer", header: "Bearer not-the-secret"},
		{name: "wrong query", query: "?token=not-the-secret"},
		// The remote token itself must not open the proxy: it is only ever sent upstream.
		{name: "remote token as local secret", header: "Bearer " + remoteTestToken},
	} {
		req, _ := http.NewRequest(http.MethodGet, p.conn().httpURL+"/conn"+tc.query, nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d; want 401", tc.name, res.StatusCode)
		}
	}
	if auth, _ := rec.snapshot(); len(auth) != 0 {
		t.Fatalf("remote was reached without the local secret: %q", auth)
	}
}

func TestRemoteProxyWebSocketRoundTrip(t *testing.T) {
	remote, rec := newFakeRemote(t)
	p := startTestProxy(t, remote.URL)

	// The WebView cannot set headers on a WebSocket, so the local secret rides in the query.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, p.conn().wsURL+"?token="+url.QueryEscape(p.local), nil)
	if err != nil {
		t.Fatalf("dial through proxy: %v", err)
	}
	defer ws.CloseNow()

	const frame = `{"id":"1","method":"project.list"}`
	if err := ws.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		t.Fatal(err)
	}
	_, got, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != frame {
		t.Fatalf("echo = %q; want %q", got, frame)
	}

	auth, queries := rec.snapshot()
	if len(auth) != 1 || auth[0] != "Bearer "+remoteTestToken {
		t.Fatalf("remote saw Authorization %q; want the remote bearer token", auth)
	}
	if strings.Contains(queries[0], "token") {
		t.Fatalf("local secret leaked upstream in query %q", queries[0])
	}
}

func TestRemoteConnectionNeverExposesRemoteToken(t *testing.T) {
	remote, _ := newFakeRemote(t)
	p := startTestProxy(t, remote.URL)

	conn := p.conn()
	for _, v := range []string{conn.wsURL, conn.httpURL, conn.token, fmt.Sprint(conn.payload())} {
		if strings.Contains(v, remoteTestToken) {
			t.Fatalf("remote token reached the frontend handoff: %q", v)
		}
	}
	if conn.token != p.local || !conn.attached || conn.remoteURL != remote.URL {
		t.Fatalf("handoff = %+v; want the local secret, attached, and the remote URL", conn)
	}
}

func TestCheckRemoteErrorsNeverEchoToken(t *testing.T) {
	remote, _ := newFakeRemote(t)

	err := checkRemote(context.Background(), remoteConfig{URL: remote.URL, Token: "wrong-token"})
	if err == nil {
		t.Fatal("checkRemote accepted a wrong token")
	}
	if strings.Contains(err.Error(), "wrong-token") {
		t.Fatalf("error echoed the token: %v", err)
	}

	if err := checkRemote(context.Background(), remoteConfig{URL: remote.URL, Token: remoteTestToken}); err != nil {
		t.Fatalf("checkRemote with the right token: %v", err)
	}
}

func TestDropRemoteKeepsRemoteWithoutLocalDaemon(t *testing.T) {
	remote, _ := newFakeRemote(t)
	p := startTestProxy(t, remote.URL)

	// A remote launch has no local daemon: dropping the remote must not blank the window.
	remoteOnly := &connHolder{}
	remoteOnly.setRemote(p)
	if remoteOnly.dropRemote() {
		t.Fatal("dropRemote reported a local daemon to fall back to")
	}
	if remoteOnly.current().remoteURL == "" {
		t.Fatal("dropRemote removed the only connection")
	}

	local := shellConn{wsURL: "ws://127.0.0.1:1/ws", token: "local-token"}
	both := &connHolder{local: &local}
	both.setRemote(p)
	if !both.dropRemote() {
		t.Fatal("dropRemote refused to fall back to the local daemon")
	}
	if got := both.current(); got.remoteURL != "" || got.token != "local-token" {
		t.Fatalf("after drop the window shows %+v; want the local daemon", got)
	}
}
