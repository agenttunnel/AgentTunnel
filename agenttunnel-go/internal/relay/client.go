// Package relay is the daemon's outbound client for atrelay. The daemon dials
// the relay and never listens: it opens no inbound port. The feature is off by
// default. Phone JSON-RPC arrives over the socket and is served by a transport
// Session per paired phone. See docs/relay-protocol.md.
package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
	relaywire "github.com/agenttunnel/agenttunnel-go/pkg/relay"
)

// Timing from the protocol note: reconnect backoff runs from 1 s to 60 s with
// full jitter, and resets after a connection has been healthy for 30 s.
const (
	maxBackoff   = 60 * time.Second
	healthyAfter = 30 * time.Second
	dialTimeout  = 10 * time.Second
)

var (
	errKicked       = errors.New("relay settings changed")
	errNotConnected = errors.New("relay is not connected")
	errBusy         = errors.New("relay queue is full")
)

// Options configure the relay client.
type Options struct {
	// StateDir holds relay.json: the instance id, the daemon secret, and the
	// device list.
	StateDir string
	Settings *settings.Store
	// Label is the name phones see for this daemon. Defaults to "AgentTunnel".
	Label string
	Logf  func(format string, args ...any)
}

// Client keeps one outbound socket to the relay while the feature is enabled.
// It implements transport.RelayControl for the pairing RPCs.
type Client struct {
	opts  Options
	path  string
	label string
	logf  func(string, ...any)
	kick  chan struct{}

	srv *transport.Server

	mu      sync.Mutex
	id      *identity
	up      *uplink
	upMoved chan struct{} // closed and replaced whenever up changes
	cancel  context.CancelFunc
	done    chan struct{}
}

// New builds a client from StateDir. It does not connect; call Bind then Start.
func New(opts Options) (*Client, error) {
	path := filepath.Join(opts.StateDir, "relay.json")
	id, err := loadIdentity(path)
	if err != nil {
		return nil, fmt.Errorf("relay identity: %w", err)
	}
	c := &Client{
		opts:    opts,
		path:    path,
		label:   opts.Label,
		logf:    func(string, ...any) {},
		kick:    make(chan struct{}, 1),
		id:      id,
		upMoved: make(chan struct{}),
	}
	if c.label == "" {
		c.label = "AgentTunnel"
	}
	if opts.Logf != nil {
		c.logf = opts.Logf
	}
	return c, nil
}

// Bind gives the client the server that hosts phone sessions. Call before Start.
func (c *Client) Bind(srv *transport.Server) {
	c.srv = srv
}

// Start runs the connection loop until ctx ends or Close is called.
func (c *Client) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.done = make(chan struct{})
	done := c.done
	c.mu.Unlock()
	go func() {
		defer close(done)
		c.run(ctx)
	}()
}

// WaitConnected blocks until the relay socket is up, or ctx ends. It waits on
// a change signal, not a poll.
func (c *Client) WaitConnected(ctx context.Context) error {
	return c.waitFor(ctx, true)
}

// WaitDisconnected blocks until no relay socket is up, or ctx ends.
func (c *Client) WaitDisconnected(ctx context.Context) error {
	return c.waitFor(ctx, false)
}

func (c *Client) waitFor(ctx context.Context, connected bool) error {
	for {
		c.mu.Lock()
		now := c.up != nil
		moved := c.upMoved
		c.mu.Unlock()
		if now == connected {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-moved:
		}
	}
}

// setUplinkLocked swaps the live socket and wakes anyone waiting on it.
// Callers hold c.mu.
func (c *Client) setUplinkLocked(up *uplink) {
	c.up = up
	close(c.upMoved)
	c.upMoved = make(chan struct{})
}

// Close stops the loop and any live socket, and waits for it to exit.
func (c *Client) Close() {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// run keeps the socket up while the relay is enabled. It re-reads settings on
// every pass, so enable, disable, and URL changes take effect at once.
func (c *Client) run(ctx context.Context) {
	attempt := 0
	for {
		cfg := c.opts.Settings.Relay()
		if !cfg.Enabled || cfg.URL == "" {
			attempt = 0
			select {
			case <-ctx.Done():
				return
			case <-c.kick:
			}
			continue
		}

		started := time.Now()
		err := c.connectOnce(ctx, cfg.URL)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errKicked) {
			attempt = 0
			continue
		}
		if time.Since(started) >= healthyAfter {
			attempt = 0
		}
		c.logf("relay: disconnected: %v", err)

		timer := time.NewTimer(backoff(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-c.kick:
			timer.Stop()
			attempt = 0
		case <-timer.C:
		}
		attempt++
	}
}

// backoff is full jitter: a uniform delay up to an exponential ceiling.
func backoff(attempt int) time.Duration {
	ceiling := time.Second << min(attempt, 6)
	if ceiling > maxBackoff {
		ceiling = maxBackoff
	}
	return rand.N(ceiling) + time.Millisecond
}

// connectOnce dials the relay, authenticates as this daemon, and serves the
// socket until it closes. It returns errKicked when settings changed underneath.
func (c *Client) connectOnce(ctx context.Context, base string) error {
	id, err := c.ensureIdentity()
	if err != nil {
		return err
	}

	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	ws, _, err := websocket.Dial(dctx, base+"/v1/daemon", nil)
	cancel()
	if err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}
	ws.SetReadLimit(relaywire.MaxFrameBytes)
	defer ws.CloseNow()

	if err := writeOnce(ws, relaywire.Hello{
		T:        "hello",
		V:        relaywire.Version,
		Role:     "daemon",
		Instance: id.Instance,
		Secret:   id.Secret,
		Name:     c.label,
	}); err != nil {
		return err
	}
	ack, err := readAck(ctx, ws)
	if err != nil {
		return err
	}
	if !ack.OK {
		return fmt.Errorf("relay refused this daemon: %s", ack.Error)
	}

	up := newUplink(ws, c.srv)
	c.mu.Lock()
	c.setUplinkLocked(up)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.up == up {
			c.setUplinkLocked(nil)
		}
		c.mu.Unlock()
		up.close()
	}()
	go up.writeLoop()

	stop := make(chan struct{})
	defer close(stop)
	var kicked atomic.Bool
	go func() {
		select {
		case <-ctx.Done():
		case <-c.kick:
			kicked.Store(true)
		case <-stop:
			return
		}
		up.close()
	}()
	go keepalive(up, stop)

	c.logf("relay: connected")
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			if kicked.Load() {
				return errKicked
			}
			return err
		}
		c.handleFrame(up, data)
	}
}

// handleFrame dispatches one relay frame. A frame with `c` and no `t` is a
// phone's JSON-RPC, handed to that phone's session. The rest are control frames.
func (c *Client) handleFrame(up *uplink, data []byte) {
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil {
		return
	}
	if _, enc := m["enc"]; enc {
		c.logf("relay: encrypted frames are not supported in v1")
		return
	}
	var t, cid string
	_ = json.Unmarshal(m["t"], &t)
	_ = json.Unmarshal(m["c"], &cid)

	if t == "" {
		if cid != "" {
			up.session(cid).Handle(data)
		}
		return
	}
	switch t {
	case "ping":
		up.send(frame(map[string]string{"t": "pong"}))
	case "devices":
		var d relaywire.Devices
		if json.Unmarshal(data, &d) == nil {
			c.setDevices(d.Devices)
		}
	case "paired":
		var p relaywire.Paired
		if json.Unmarshal(data, &p) == nil {
			c.addDevice(p.Device)
		}
	case "unpaired":
		var u relaywire.Unpaired
		if json.Unmarshal(data, &u) == nil {
			c.removeDevice(u.ClientID)
			up.dropSession(u.ClientID)
		}
	case "detach":
		var d relaywire.Detach
		if json.Unmarshal(data, &d) == nil {
			up.dropSession(d.ClientID)
		}
	}
}

// keepalive queues a ping on a fixed interval so the relay sees the daemon as live.
func keepalive(up *uplink, stop <-chan struct{}) {
	t := time.NewTicker(relaywire.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-up.done:
			return
		case <-t.C:
			up.send(frame(map[string]string{"t": "ping"}))
		}
	}
}

// --- transport.RelayControl ---

// Status reports the saved configuration and whether a socket is up.
func (c *Client) Status() transport.RelayStatus {
	cfg := c.opts.Settings.Relay()
	c.mu.Lock()
	defer c.mu.Unlock()
	st := transport.RelayStatus{Enabled: cfg.Enabled, URL: cfg.URL, Connected: c.up != nil}
	if c.id != nil {
		st.Instance = c.id.Instance
	}
	return st
}

// Enable validates and saves the relay URL, then connects.
func (c *Client) Enable(raw string) error {
	url, err := relaywire.ValidateURL(raw)
	if err != nil {
		return err
	}
	c.opts.Settings.SetRelay(settings.Relay{Enabled: true, URL: url})
	c.kickLoop()
	return nil
}

// Disable turns the relay off and closes the socket. The saved URL is kept.
func (c *Client) Disable() error {
	cfg := c.opts.Settings.Relay()
	cfg.Enabled = false
	c.opts.Settings.SetRelay(cfg)
	c.kickLoop()
	return nil
}

// PairStart asks the relay for a pairing code. The code is generated here, and
// the relay stores it for one use, within its TTL.
func (c *Client) PairStart(context.Context) (transport.PairOffer, error) {
	up := c.uplink()
	if up == nil {
		return transport.PairOffer{}, errNotConnected
	}
	code, err := relaywire.NewCode()
	if err != nil {
		return transport.PairOffer{}, err
	}
	if !up.out.push(frame(relaywire.PairOffer{T: "pair-offer", Code: code}), false, "") {
		return transport.PairOffer{}, errBusy
	}
	return transport.PairOffer{Code: code, Expires: time.Now().Add(relaywire.CodeTTL)}, nil
}

// Devices lists the phones paired with this instance.
func (c *Client) Devices() []transport.RelayDevice {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []transport.RelayDevice{}
	if c.id == nil {
		return out
	}
	for _, d := range c.id.Devices {
		at, _ := time.Parse(time.RFC3339, d.PairedAt)
		out = append(out, transport.RelayDevice{ClientID: d.ClientID, Name: d.Name, PairedAt: at})
	}
	return out
}

// Unpair asks the relay to revoke one phone, or all of them. The relay closes
// the sockets and refuses reconnects. The local list is updated at once.
func (c *Client) Unpair(_ context.Context, clientID string, all bool) error {
	up := c.uplink()
	if up == nil {
		return errNotConnected
	}
	if !up.out.push(frame(relaywire.Revoke{T: "revoke", ClientID: clientID, All: all}), false, "") {
		return errBusy
	}
	c.mu.Lock()
	var dropped []string
	if c.id != nil {
		kept := c.id.Devices[:0]
		for _, d := range c.id.Devices {
			if all || d.ClientID == clientID {
				dropped = append(dropped, d.ClientID)
				continue
			}
			kept = append(kept, d)
		}
		c.id.Devices = kept
		c.persistLocked()
	}
	c.mu.Unlock()
	for _, id := range dropped {
		up.dropSession(id)
	}
	return nil
}

func (c *Client) kickLoop() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func (c *Client) uplink() *uplink {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.up
}

// ensureIdentity mints the instance id and secret on first use and returns a
// copy of the credentials for one connection.
func (c *Client) ensureIdentity() (identity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id == nil {
		id, err := newIdentity()
		if err != nil {
			return identity{}, err
		}
		c.id = id
		c.persistLocked()
	}
	return identity{Instance: c.id.Instance, Secret: c.id.Secret}, nil
}

func (c *Client) setDevices(list []relaywire.Device) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id == nil {
		return
	}
	c.id.Devices = append([]relaywire.Device(nil), list...)
	c.persistLocked()
}

func (c *Client) addDevice(d relaywire.Device) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id == nil {
		return
	}
	for i, existing := range c.id.Devices {
		if existing.ClientID == d.ClientID {
			c.id.Devices[i] = d
			c.persistLocked()
			return
		}
	}
	c.id.Devices = append(c.id.Devices, d)
	c.persistLocked()
}

func (c *Client) removeDevice(clientID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id == nil {
		return
	}
	kept := c.id.Devices[:0]
	for _, d := range c.id.Devices {
		if d.ClientID != clientID {
			kept = append(kept, d)
		}
	}
	c.id.Devices = kept
	c.persistLocked()
}

// persistLocked writes the identity. A failure is logged, not fatal: the
// in-memory state stays correct for this run. Callers hold c.mu.
func (c *Client) persistLocked() {
	if err := saveIdentity(c.path, c.id); err != nil {
		c.logf("relay: save identity: %v", err)
	}
}

// writeOnce sends one frame on a socket that has no uplink yet.
func writeOnce(ws *websocket.Conn, v any) error {
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	return ws.Write(ctx, websocket.MessageText, frame(v))
}

// readAck reads the relay's answer to hello.
func readAck(ctx context.Context, ws *websocket.Conn) (relaywire.Ack, error) {
	rctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	_, data, err := ws.Read(rctx)
	if err != nil {
		return relaywire.Ack{}, fmt.Errorf("relay hello: %w", err)
	}
	var ack relaywire.Ack
	if err := json.Unmarshal(data, &ack); err != nil || ack.T != "ack" {
		return relaywire.Ack{}, errors.New("relay sent an unexpected first frame")
	}
	return ack, nil
}

// frame encodes a control or tagged frame for the socket.
func frame(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}
