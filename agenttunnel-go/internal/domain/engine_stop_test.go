package domain

import (
	"context"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// A command sent to a stopped engine must not block its caller. The command
// channel is buffered, so the send alone succeeds, and a loop that is still
// winding down may or may not take it; either way the wait for the result must
// end, or a caller such as a turn's release blocks forever.
func TestDispatchToStoppedEngineReturns(t *testing.T) {
	stoppers := map[string]func(e *Engine, cancel context.CancelFunc){
		"closed":    func(e *Engine, _ context.CancelFunc) { e.Close() },
		"cancelled": func(_ *Engine, cancel context.CancelFunc) { cancel() },
	}
	for name, stop := range stoppers {
		e := NewEngine(NewMemoryStore(), nil)
		ctx, cancel := context.WithCancel(context.Background())
		e.Start(ctx)
		stop(e, cancel)

		errc := make(chan error, 1)
		go func() {
			errc <- e.Dispatch(context.Background(), contracts.Command{ID: "c", Type: contracts.CmdThreadCreate, ThreadID: "t1"})
		}()
		select {
		case <-errc:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: dispatch to a stopped engine never returned", name)
		}
		cancel()
	}
}
