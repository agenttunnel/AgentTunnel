package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
	relaywire "github.com/agenttunnel/agenttunnel-go/pkg/relay"
)

// defaultTunnelPort is where the in-app relay listens. A busy port falls back
// to any free one, since the pairing QR carries the full address.
const defaultTunnelPort = 7777

// tunnelConnectWait bounds how long StartTunnel waits for this daemon's socket
// to reach the relay it just started.
const tunnelConnectWait = 10 * time.Second

// winsockAddrInUse is WSAEADDRINUSE. Windows reports a busy port with this code,
// not with the POSIX EADDRINUSE value Go defines.
const winsockAddrInUse = syscall.Errno(10048)

// tunnelRun is one in-app relay: the server, its address and loopback URL, and
// the relay settings it replaced, so Stop can put them back.
type tunnelRun struct {
	ln     net.Listener
	port   int
	lanURL string
	url    string
	cancel context.CancelFunc
	done   chan struct{}
	prev   settings.Relay
}

// StartTunnel serves a relay on every interface and points this daemon at it
// over loopback. It returns once the daemon socket is up, so a pairing code can
// be minted at once. Starting a running tunnel returns its status unchanged.
func (c *Client) StartTunnel(ctx context.Context) (transport.TunnelStatus, error) {
	fresh, err := c.openTunnel()
	if err != nil {
		return transport.TunnelStatus{}, err
	}
	wctx, cancel := context.WithTimeout(ctx, tunnelConnectWait)
	defer cancel()
	// A new tunnel kicked the old socket, so wait for it to drop before waiting
	// for a connection. Otherwise a socket to a previous relay could look connected.
	if fresh {
		err = c.WaitDisconnected(wctx)
	}
	if err == nil {
		err = c.WaitConnected(wctx)
	}
	if err != nil {
		_ = c.StopTunnel()
		return transport.TunnelStatus{}, fmt.Errorf("the tunnel relay did not connect: %w", err)
	}
	return c.TunnelStatus(), nil
}

// StopTunnel closes the in-app relay, waits for its port to be released, and
// restores the relay settings from before StartTunnel. It is a no-op when no
// tunnel is running.
func (c *Client) StopTunnel() error {
	c.tunnelMu.Lock()
	defer c.tunnelMu.Unlock()
	c.stopTunnelLocked()
	return nil
}

// stopTunnelLocked is StopTunnel for callers that already hold tunnelMu.
func (c *Client) stopTunnelLocked() {
	t := c.tunnel
	if t == nil {
		return
	}
	c.tunnel = nil
	c.opts.Settings.SetRelay(t.prev)
	c.kickLoop()
	t.cancel()
	_ = t.ln.Close()
	<-t.done
}

// TunnelStatus reports whether the in-app relay is listening and its phone URL.
func (c *Client) TunnelStatus() transport.TunnelStatus {
	c.tunnelMu.Lock()
	defer c.tunnelMu.Unlock()
	if c.tunnel == nil {
		return transport.TunnelStatus{}
	}
	return transport.TunnelStatus{Listening: true, Port: c.tunnel.port, LANURL: c.tunnel.lanURL}
}

// openTunnel starts the relay server and enables the daemon's relay client
// against it. It reports whether it opened a new tunnel. It holds tunnelMu only
// for the setup, never while waiting for a connection, so status and stop are
// never blocked by a slow start.
func (c *Client) openTunnel() (bool, error) {
	c.tunnelMu.Lock()
	defer c.tunnelMu.Unlock()
	if c.tunnel != nil {
		return false, nil
	}
	ln, err := c.listenTunnel()
	if err != nil {
		return false, err
	}
	lan := relaywire.LANURLWith(ln.Addr(), c.lanHost)
	if lan == "" {
		_ = ln.Close()
		return false, errors.New("no LAN address for a phone to reach; connect this computer to a local network")
	}
	srv, err := relaywire.New(relaywire.Options{
		StatePath: filepath.Join(c.opts.StateDir, "tunnel-relay.json"),
		Logf:      c.logf,
	})
	if err != nil {
		_ = ln.Close()
		return false, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, ln)
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	url := "ws://127.0.0.1:" + strconv.Itoa(port)
	c.tunnel = &tunnelRun{
		ln:     ln,
		port:   port,
		lanURL: lan,
		url:    url,
		cancel: cancel,
		done:   done,
		prev:   c.opts.Settings.Relay(),
	}
	// Save directly rather than through Enable: Enable stops a tunnel whose URL
	// differs, and this is the tunnel's own URL, which must keep it running.
	c.saveRelay(url)
	return true, nil
}

// listenTunnel binds the default port on every interface. Only a busy port
// falls back to any free one; other bind errors are returned.
func (c *Client) listenTunnel() (net.Listener, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(c.tunnelPort)))
	if err == nil {
		return ln, nil
	}
	if !errors.Is(err, syscall.EADDRINUSE) && !errors.Is(err, winsockAddrInUse) {
		return nil, err
	}
	return net.Listen("tcp", "0.0.0.0:0")
}
