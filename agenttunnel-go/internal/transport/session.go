package transport

import "context"

// Session is a virtual JSON-RPC client with no socket of its own. The relay
// client holds one per phone: frames the phone sends arrive through Handle,
// and replies and thread.event pushes leave through the sink it was built
// with. It gets the same dispatch and subscription behavior as a /ws client.
type Session struct {
	c      *conn
	ctx    context.Context
	cancel context.CancelFunc
}

// NewSession opens a session whose outbound frames go to sink. Sinks are called
// from the dispatcher's goroutines and must not block for long.
func (s *Server) NewSession(sink func(Response)) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	c := &conn{sink: sink, engine: s.engine, disp: s.disp, srv: s}
	return &Session{c: c, ctx: ctx, cancel: cancel}
}

// Handle processes one client->server JSON-RPC frame.
func (s *Session) Handle(data []byte) {
	s.c.handleFrame(s.ctx, data)
}

// Close tears down the session's thread subscriptions and cancels in-flight
// pushes. Replies to requests already running are dropped by the sink's owner.
func (s *Session) Close() {
	s.cancel()
	s.c.unsubscribeAll()
}
