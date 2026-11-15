package relay

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// newTunnelClientUnstarted builds a relay client whose loop is not running, so
// a test can arrange settings and kicks before the first pass reads them.
func newTunnelClientUnstarted(t *testing.T, lan string, port int) *Client {
	t.Helper()
	c, err := New(Options{StateDir: t.TempDir(), Settings: settings.New("")})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.lanHost = func() string { return lan }
	c.tunnelPort = port
	return c
}

// newTunnelClient builds a relay client that runs its loop, with a LAN host
// the test controls and a port the test owns, so nothing binds 7777 here.
func newTunnelClient(t *testing.T, lan string, port int) *Client {
	t.Helper()
	c := newTunnelClientUnstarted(t, lan, port)
	c.Start(context.Background())
	t.Cleanup(c.Close)
	return c
}

// StartTunnel listens on every interface, dials its own relay, and returns a
// LAN URL for phones. StopTunnel releases the port and restores settings.
func TestTunnelStartListensAndStopReleasesPort(t *testing.T) {
	c := newTunnelClient(t, "192.168.1.42", 0)

	st, err := c.StartTunnel(context.Background())
	if err != nil {
		t.Fatalf("start tunnel: %v", err)
	}
	if !st.Listening || st.Port == 0 {
		t.Fatalf("start must report a listening port, got %+v", st)
	}
	want := "ws://192.168.1.42:" + strconv.Itoa(st.Port)
	if st.LANURL != want {
		t.Fatalf("LANURL = %q, want %q", st.LANURL, want)
	}
	if got := c.TunnelStatus(); got != st {
		t.Fatalf("status must match the start reply, got %+v want %+v", got, st)
	}
	if !c.Status().Connected {
		t.Fatal("start returns only once the daemon socket is up")
	}
	if got := c.Status(); got.URL != "ws://127.0.0.1:"+strconv.Itoa(st.Port) {
		t.Fatalf("the live socket must be the tunnel's own, url = %q", got.URL)
	}

	if err := c.StopTunnel(); err != nil {
		t.Fatalf("stop tunnel: %v", err)
	}
	if got := c.TunnelStatus(); got.Listening {
		t.Fatalf("stop must clear the tunnel, got %+v", got)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(st.Port)))
	if err != nil {
		t.Fatalf("stop must release the port: %v", err)
	}
	_ = ln.Close()
}

// A kick queued before the loop read the settings is already satisfied by the
// dial it causes, so it must be consumed by that read. A token that reaches the
// dial instead aborts the socket it just brought up, which is how a successful
// tunnel start used to report a socket that was already gone.
func TestTunnelConsumesItsEnableKickBeforeDialing(t *testing.T) {
	c := newTunnelClientUnstarted(t, "192.168.1.42", 0)
	t.Cleanup(c.Close)

	// The production ordering: the settings are saved and the loop kicked
	// before the loop's first pass reads either. openTunnel needs no loop.
	url, fresh, err := c.openTunnel()
	if err != nil {
		t.Fatalf("open tunnel: %v", err)
	}
	if !fresh {
		t.Fatal("a first openTunnel must report a fresh tunnel")
	}

	dialing := make(chan struct{})
	release := make(chan struct{})
	var first sync.Once
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	// Close joins the loop, which is parked in the dial until released.
	t.Cleanup(unblock)
	c.dial = func(ctx context.Context, target string, opts *websocket.DialOptions) (*websocket.Conn, *http.Response, error) {
		first.Do(func() { close(dialing) })
		<-release
		return websocket.Dial(ctx, target, opts)
	}

	c.Start(context.Background())
	select {
	case <-dialing:
	case <-time.After(tunnelConnectWait):
		t.Fatal("the loop never dialed the tunnel")
	}
	if n := len(c.kick); n != 0 {
		t.Fatalf("the kick that enabled the relay reached the dial: %d still queued", n)
	}
	unblock()

	st, err := c.StartTunnel(context.Background())
	if err != nil {
		t.Fatalf("start tunnel: %v", err)
	}
	if !st.Listening {
		t.Fatalf("start must report the running tunnel, got %+v", st)
	}
	if got := c.Status(); !got.Connected || got.URL != url {
		t.Fatalf("start returned with connected=%v url=%q, want the tunnel %q up", got.Connected, got.URL, url)
	}
}

// A tunnel start may only count a socket dialed for its own relay. A live
// socket to a previous URL must never satisfy the wait.
func TestTunnelWaitConnectedMatchesTheTunnelsURL(t *testing.T) {
	c, err := New(Options{StateDir: t.TempDir(), Settings: settings.New("")})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.mu.Lock()
	c.setUplinkLocked(&uplink{}, "ws://127.0.0.1:1")
	c.mu.Unlock()

	// A cancelled context keeps the check to the predicate: a satisfied wait
	// returns nil before it looks at the context, anything else reports it.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.WaitConnectedFor(ctx, "ws://127.0.0.1:2"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a socket to another URL satisfied the wait: %v", err)
	}
	if err := c.WaitConnectedFor(ctx, "ws://127.0.0.1:1"); err != nil {
		t.Fatalf("the wait must accept the live socket for its own URL: %v", err)
	}
}

// A second start while the tunnel runs returns the same tunnel and binds nothing new.
func TestTunnelStartIsIdempotent(t *testing.T) {
	c := newTunnelClient(t, "192.168.1.42", 0)

	first, err := c.StartTunnel(context.Background())
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	second, err := c.StartTunnel(context.Background())
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if first != second {
		t.Fatalf("second start changed the tunnel: %+v then %+v", first, second)
	}
	if err := c.StopTunnel(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// A busy preferred port falls back to a free one instead of failing the start.
func TestTunnelFallsBackWhenThePreferredPortIsBusy(t *testing.T) {
	busy, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("hold a port: %v", err)
	}
	t.Cleanup(func() { _ = busy.Close() })
	taken := busy.Addr().(*net.TCPAddr).Port

	c := newTunnelClient(t, "192.168.1.42", taken)
	st, err := c.StartTunnel(context.Background())
	if err != nil {
		t.Fatalf("start with a busy port: %v", err)
	}
	if st.Port == taken || st.Port == 0 {
		t.Fatalf("tunnel must move off the busy port %d, got %d", taken, st.Port)
	}
	if err := c.StopTunnel(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// Without a LAN address no phone can reach the tunnel, so start refuses and
// leaves nothing listening.
func TestTunnelStartRefusesWithoutALANAddress(t *testing.T) {
	c := newTunnelClient(t, "", 0)
	if _, err := c.StartTunnel(context.Background()); err == nil {
		t.Fatal("a tunnel with no LAN address must not start")
	}
	if c.TunnelStatus().Listening {
		t.Fatal("a refused start must leave nothing listening")
	}
}

// Turning the relay off stops a running tunnel too, so no port stays bound for
// a relay nothing dials.
func TestDisableStopsTheTunnel(t *testing.T) {
	c := newTunnelClient(t, "192.168.1.42", 0)
	st, err := c.StartTunnel(context.Background())
	if err != nil {
		t.Fatalf("start tunnel: %v", err)
	}
	if err := c.Disable(); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if c.TunnelStatus().Listening {
		t.Fatal("disabling the relay must stop the tunnel")
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(st.Port)))
	if err != nil {
		t.Fatalf("disable must release the port: %v", err)
	}
	_ = ln.Close()
}

// Pointing the relay at another URL ends the tunnel and frees its port, and a
// later StopTunnel leaves the new setting alone.
func TestEnableOtherURLStopsTheTunnelAndKeepsTheNewSetting(t *testing.T) {
	c := newTunnelClient(t, "192.168.1.42", 0)
	st, err := c.StartTunnel(context.Background())
	if err != nil {
		t.Fatalf("start tunnel: %v", err)
	}

	const other = "ws://127.0.0.1:1"
	if err := c.Enable(other); err != nil {
		t.Fatalf("enable other URL: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), tunnelConnectWait)
	defer cancel()
	if err := c.WaitDisconnected(ctx); err != nil {
		t.Fatalf("the tunnel's socket must drop: %v", err)
	}
	if c.TunnelStatus().Listening {
		t.Fatal("enabling another URL must stop the tunnel")
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(st.Port)))
	if err != nil {
		t.Fatalf("enabling another URL must release the port: %v", err)
	}
	_ = ln.Close()

	if err := c.StopTunnel(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := c.Status(); !got.Enabled || got.URL != other {
		t.Fatalf("a later stop must keep the new setting, got enabled=%v url=%q", got.Enabled, got.URL)
	}
}

// Enabling the tunnel's own loopback URL, as openTunnel does, keeps it running.
func TestEnableOwnURLKeepsTheTunnel(t *testing.T) {
	c := newTunnelClient(t, "192.168.1.42", 0)
	st, err := c.StartTunnel(context.Background())
	if err != nil {
		t.Fatalf("start tunnel: %v", err)
	}
	if err := c.Enable("ws://127.0.0.1:" + strconv.Itoa(st.Port)); err != nil {
		t.Fatalf("enable own URL: %v", err)
	}
	if got := c.TunnelStatus(); got != st {
		t.Fatalf("enabling the tunnel's own URL must keep it, got %+v want %+v", got, st)
	}
	if err := c.StopTunnel(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// Only a busy port falls back; any other bind error is returned.
func TestTunnelListenReturnsErrorsOtherThanAddrInUse(t *testing.T) {
	c := newTunnelClient(t, "192.168.1.42", -1)
	if ln, err := c.listenTunnel(); err == nil {
		_ = ln.Close()
		t.Fatal("an invalid port is not a busy port, so it must not fall back")
	}
}
