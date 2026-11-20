package transport

import (
	"context"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// threadChange is the slim `thread.changed` frame: which thread moved, to which
// sequence number, and by what kind of event. Clients re-read thread.list from
// it instead of polling; the payload stays on the thread's own subscription.
type threadChange struct {
	ThreadID contracts.ThreadID  `json:"threadId"`
	Seq      int64               `json:"seq"`
	Type     contracts.EventType `json:"type"`
}

// FollowThreadChanges pushes a `thread.changed` frame to every client for each
// durable event on any thread, until ctx ends. Ephemeral agent output (seq 0)
// is not a change. A frame dropped for a slow follower is a missed hint: a
// client's next refresh or reconnect reads the list again.
func (s *Server) FollowThreadChanges(ctx context.Context) {
	events, unsub := s.engine.SubscribeAll()
	go func() {
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-events:
				if ev.Seq == 0 {
					continue
				}
				s.NotifyAll("thread.changed", threadChange{ThreadID: ev.ThreadID, Seq: ev.Seq, Type: ev.Type()})
			}
		}
	}()
}
