package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

// RPCError is a JSON-RPC error returned by the daemon.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Message }

// Event is a pushed server frame (method set, no id): thread.event frames and
// shell notifications. Later commands (attach, logs) consume these; the core
// commands only keep the read loop moving.
type Event struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type reply struct {
	result json.RawMessage
	err    *RPCError
}

// readLimit caps one incoming frame on the CLI side. Daemon replies (a
// thread.list over real thread history) can exceed websocket's 32768-byte
// default, which broke every CLI command that resolves a thread ref. It
// mirrors the relay's MaxFrameBytes convention (pkg/relay/protocol.go) and
// only needs to cover the largest reply the daemon legitimately sends.
const readLimit = 4 << 20

// Client is one JSON-RPC connection to a daemon. A single read loop dispatches
// pushed frames and correlates replies by id, so concurrent Call invocations
// never steal each other's frames.
type Client struct {
	ws *websocket.Conn

	writeMu sync.Mutex
	seq     atomic.Int64

	pendingMu sync.Mutex
	pending   map[string]chan reply

	events chan Event
	done   chan struct{}

	readErrMu sync.Mutex
	readErr   error
}

// Dial opens a WebSocket connection, sending the token as an Authorization:
// Bearer header. The token never rides in the URL: query strings end up in
// proxy and access logs.
func Dial(ctx context.Context, wsURL, token string) (*Client, error) {
	var header http.Header
	if token != "" {
		header = http.Header{"Authorization": {"Bearer " + token}}
	}
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(readLimit)
	c := &Client{
		ws:      ws,
		pending: make(map[string]chan reply),
		events:  make(chan Event, 256),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// Events returns pushed frames. The channel closes when the connection does.
func (c *Client) Events() <-chan Event { return c.events }

// Err reports why the connection ended, once it has.
func (c *Client) Err() error {
	c.readErrMu.Lock()
	defer c.readErrMu.Unlock()
	return c.readErr
}

// Close shuts the connection down.
func (c *Client) Close() {
	c.ws.CloseNow()
	<-c.done
}

// readLoop owns the socket's reads. Replies go to their waiting Call; every
// other frame is published as an Event (dropped when the consumer is slow: CLI
// commands that need every frame keep up, and a blocking send would stall the
// reply correlation behind it).
func (c *Client) readLoop() {
	defer close(c.done)
	defer close(c.events)
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			c.fail(err)
			return
		}
		var frame struct {
			ID     string          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		if frame.Method != "" {
			select {
			case c.events <- Event{Method: frame.Method, Params: frame.Params}:
			default:
			}
			continue
		}
		if frame.ID == "" {
			continue
		}
		c.pendingMu.Lock()
		ch, ok := c.pending[frame.ID]
		delete(c.pending, frame.ID)
		c.pendingMu.Unlock()
		if ok {
			ch <- reply{result: frame.Result, err: frame.Error}
		}
	}
}

// fail records the read error and wakes every waiting Call so none hangs after
// the socket dies.
func (c *Client) fail(err error) {
	c.readErrMu.Lock()
	c.readErr = err
	c.readErrMu.Unlock()
	c.pendingMu.Lock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- reply{err: &RPCError{Message: "connection closed: " + err.Error()}}
	}
	c.pendingMu.Unlock()
}

// Call performs one JSON-RPC call and decodes the result into out when non-nil.
// The response is matched by id; pushed event frames read in the meantime are
// published on Events, not mistaken for this call's reply.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	id := fmt.Sprintf("at-%d", c.seq.Add(1))
	ch := make(chan reply, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	req := struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}{ID: id, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		c.forget(id)
		return err
	}
	c.writeMu.Lock()
	err = c.ws.Write(ctx, websocket.MessageText, data)
	c.writeMu.Unlock()
	if err != nil {
		c.forget(id)
		return err
	}

	select {
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if out != nil && len(r.result) > 0 {
			if err := json.Unmarshal(r.result, out); err != nil {
				return fmt.Errorf("%s: decode result: %w", method, err)
			}
		}
		return nil
	}
}

func (c *Client) forget(id string) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

// Ping verifies the daemon answers RPCs. project.list is used because it has no
// side effects and is safe in every fixture (roles.list panics in the transport
// fixtures the daemon tests build).
func (c *Client) Ping(ctx context.Context) error {
	return c.Call(ctx, "project.list", nil, nil)
}

// ErrNoDaemon reports that no daemon answered and none could be started.
var ErrNoDaemon = errors.New("no daemon is running")
