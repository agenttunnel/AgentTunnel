package relay

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// newTunnelClient builds a relay client that runs its loop, with a LAN host
// the test controls and a port the test owns, so nothing binds 7777 here.
func newTunnelClient(t *testing.T, lan string, port int) *Client {
	t.Helper()
	c, err := New(Options{StateDir: t.TempDir(), Settings: settings.New("")})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.lanHost = func() string { return lan }
	c.tunnelPort = port
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
