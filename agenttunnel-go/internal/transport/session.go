package transport

import (
	"context"
	"encoding/json"
	"strings"
)

// relayMethodPrefix names the RPCs that configure the daemon's relay. A phone
// reaches the daemon through that relay, so it must not reach these: a lost
// phone could mint a pairing code and enroll a stranger's device.
const relayMethodPrefix = "relay."

// codeRelayRestricted is the JSON-RPC error code for a refused relayed call.
const codeRelayRestricted = -32003

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

// Handle processes one client->server JSON-RPC frame. relay.* methods are
// refused here, synchronously, before any dispatch.
func (s *Session) Handle(data []byte) {
	var req Request
	if err := json.Unmarshal(data, &req); err == nil && strings.HasPrefix(req.Method, relayMethodPrefix) {
		s.c.send(Response{ID: req.ID, Error: &RPCError{
			Code:    codeRelayRestricted,
			Message: req.Method + " is not available over a relayed connection; use the desktop app",
		}})
		return
	}
	s.c.handleFrame(s.ctx, data)
}

// Close tears down the session's thread subscriptions and cancels in-flight
// pushes. Replies to requests already running are dropped by the sink's owner.
func (s *Session) Close() {
	s.cancel()
	s.c.unsubscribeAll()
}
