package relay

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// Outbound bounds, from the protocol note: a per-socket queue of 256 frames or
// 4 MiB. Overflow drops the oldest thread.event and leaves a resync hint for
// that client. RPC replies are never dropped; if nothing can be dropped, the
// socket is closed and the daemon reconnects.
const (
	queueFrames = 256
	queueBytes  = 4 << 20
	eventMethod = "thread.event"
)

// queued is one outbound frame. Event frames are the only ones overflow may
// drop; cid names the client a dropped event belonged to.
type queued struct {
	data  []byte
	event bool
	cid   string
}

// outbox is a bounded FIFO shared by every session on one daemon socket. Resync
// hints live beside the queue, one per client, so a hint never takes a slot
// that a frame needs.
type outbox struct {
	mu     sync.Mutex
	q      []queued
	bytes  int
	hints  []string
	hinted map[string]bool
	closed bool
	wake   chan struct{}
}

func newOutbox() *outbox {
	return &outbox{hinted: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// push queues a frame for client cid. When the queue is full it drops the
// oldest event frames to make room, and each affected client gets one resync
// hint. It reports false when the frame cannot fit: the outbox is closed, or
// it is full of frames that may not be dropped.
func (o *outbox) push(data []byte, event bool, cid string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return false
	}
	for len(o.q) >= queueFrames || o.bytes+len(data) > queueBytes {
		idx := o.oldestEvent()
		if idx < 0 {
			return false
		}
		dropped := o.q[idx]
		o.q = append(o.q[:idx], o.q[idx+1:]...)
		o.bytes -= len(dropped.data)
		if !o.hinted[dropped.cid] {
			o.hinted[dropped.cid] = true
			o.hints = append(o.hints, dropped.cid)
		}
	}
	o.q = append(o.q, queued{data: data, event: event, cid: cid})
	o.bytes += len(data)
	o.signal()
	return true
}

func (o *outbox) oldestEvent() int {
	for i, item := range o.q {
		if item.event {
			return i
		}
	}
	return -1
}

func (o *outbox) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// pop returns the next frame, blocking until one is queued or the outbox closes.
// Resync hints go first: they are small and tell a client to re-read.
func (o *outbox) pop(done <-chan struct{}) ([]byte, bool) {
	for {
		o.mu.Lock()
		if len(o.hints) > 0 {
			cid := o.hints[0]
			o.hints = o.hints[1:]
			delete(o.hinted, cid)
			o.mu.Unlock()
			return frame(resyncHint{T: "status", C: cid, Resync: true}), true
		}
		if len(o.q) > 0 {
			item := o.q[0]
			o.q = o.q[1:]
			o.bytes -= len(item.data)
			o.mu.Unlock()
			return item.data, true
		}
		closed := o.closed
		o.mu.Unlock()
		if closed {
			return nil, false
		}
		select {
		case <-o.wake:
		case <-done:
			return nil, false
		}
	}
}

func (o *outbox) close() {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	o.signal()
}

// clientFrame tags a session's frame with the client it belongs to. Response
// fields are inlined, so the relay sees JSON-RPC with a `c` sibling.
type clientFrame struct {
	C string `json:"c"`
	transport.Response
}

// resyncHint tells a client its event stream lost frames, so it should re-read.
type resyncHint struct {
	T      string `json:"t"`
	C      string `json:"c"`
	Resync bool   `json:"resync"`
}

// uplink is one live daemon socket: its outbox, the per-client sessions that
// run on it, and the socket itself.
type uplink struct {
	ws   *websocket.Conn
	out  *outbox
	srv  *transport.Server
	done chan struct{}
	once sync.Once

	mu       sync.Mutex
	sessions map[string]*transport.Session
}

func newUplink(ws *websocket.Conn, srv *transport.Server) *uplink {
	return &uplink{
		ws:       ws,
		out:      newOutbox(),
		srv:      srv,
		done:     make(chan struct{}),
		sessions: map[string]*transport.Session{},
	}
}

// writeLoop drains the outbox onto the socket until the uplink closes.
func (u *uplink) writeLoop() {
	for {
		data, ok := u.out.pop(u.done)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := u.ws.Write(ctx, websocket.MessageText, data)
		cancel()
		if err != nil {
			u.close()
			return
		}
	}
}

// send queues a control frame. Overflow here means the socket cannot keep up,
// so it is closed.
func (u *uplink) send(data []byte) {
	if !u.out.push(data, false, "") {
		u.close()
	}
}

// session returns the virtual client for cid, creating it on first use.
func (u *uplink) session(cid string) *transport.Session {
	u.mu.Lock()
	defer u.mu.Unlock()
	if s := u.sessions[cid]; s != nil {
		return s
	}
	s := u.srv.NewSession(func(r transport.Response) { u.reply(cid, r) })
	u.sessions[cid] = s
	return s
}

// dropSession closes one client's subscriptions. The next frame from that
// client opens a fresh session.
func (u *uplink) dropSession(cid string) {
	u.mu.Lock()
	s := u.sessions[cid]
	delete(u.sessions, cid)
	u.mu.Unlock()
	if s != nil {
		s.Close()
	}
}

// reply tags a session's outbound frame with its client id and queues it.
func (u *uplink) reply(cid string, r transport.Response) {
	data := frame(clientFrame{C: cid, Response: r})
	if !u.out.push(data, r.Method == eventMethod, cid) {
		u.close()
	}
}

// close tears the uplink down once: the socket shuts, the writer stops, and
// every session it hosted is closed.
func (u *uplink) close() {
	u.once.Do(func() {
		close(u.done)
		u.out.close()
		_ = u.ws.CloseNow()
		u.mu.Lock()
		sessions := u.sessions
		u.sessions = map[string]*transport.Session{}
		u.mu.Unlock()
		for _, s := range sessions {
			s.Close()
		}
	})
}
