package relay

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// link is one live socket, daemon or client. Writes go through a bounded queue
// drained by one goroutine, so a slow peer never blocks routing for others.
type link struct {
	ws       *websocket.Conn
	out      chan []byte
	done     chan struct{}
	writeMu  sync.Mutex
	once     sync.Once
	instance string
	clientID string // empty for a daemon link
}

func newLink(ws *websocket.Conn, instance, clientID string) *link {
	l := &link{
		ws:       ws,
		out:      make(chan []byte, OutboundQueue),
		done:     make(chan struct{}),
		instance: instance,
		clientID: clientID,
	}
	go l.writeLoop()
	go l.keepalive(PingInterval)
	return l
}

func (l *link) writeLoop() {
	for {
		select {
		case <-l.done:
			return
		case data := <-l.out:
			if err := l.write(data); err != nil {
				_ = l.ws.CloseNow()
				return
			}
		}
	}
}

// keepalive queues a ping on a fixed interval so idle sockets survive proxies.
func (l *link) keepalive(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-t.C:
			l.send(frame(ping{T: tPing}))
		}
	}
}

func (l *link) write(data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	return l.ws.Write(ctx, websocket.MessageText, data)
}

// send queues a frame. It reports false when the link is closed or its queue
// is full; the caller decides what overflow means for its peer.
func (l *link) send(data []byte) bool {
	select {
	case <-l.done:
		return false
	default:
	}
	select {
	case l.out <- data:
		return true
	default:
		return false
	}
}

// stop closes the link once. An optional final frame is written first, then the
// socket is closed in the background so a caller on the read loop never waits
// for the peer's close reply.
func (l *link) stop(final []byte, code websocket.StatusCode, reason string) {
	l.once.Do(func() {
		close(l.done)
		if final != nil {
			_ = l.write(final)
		}
		go func() { _ = l.ws.Close(code, reason) }()
	})
}
